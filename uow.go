package uow

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
)

type scopeMode int

const (
	scopeModePhysical scopeMode = iota
	scopeModeEmulated
	scopeModeEmulatedInherited
)

type txStatus int

const (
	txStatusNone txStatus = iota
	txStatusActive
	txStatusCommitted
	txStatusRolledBack
	txStatusFailed
)

type scopeState struct {
	tx     Tx
	handle any
	depth  int
	label  string
	mode   scopeMode
	status txStatus
}

type commitOutcome struct {
	err          error
	rollbackable bool
}

type rollbackOutcome struct {
	err        error
	beforeErr  error
	attemptErr error
}

type logicalTx struct {
	name  string
	depth int
	raw   any
}

func (t *logicalTx) Name() string { return t.name }
func (t *logicalTx) Depth() int   { return t.depth }
func (t *logicalTx) Raw() any     { return t.raw }

type unitOfWork struct {
	opMu sync.Mutex
	mu   sync.RWMutex

	binding      ResolvedBinding
	cfg          Config
	hooks        Hooks
	interceptors []Interceptor

	rootStatus     txStatus
	rootCtx        context.Context
	txID           string
	rootOptions    BeginOptions
	stack          []*scopeState
	rollbackOnly   bool
	rollbackReason error
}

type txScope struct {
	u     *unitOfWork
	state *scopeState
}

func newUnitOfWork(binding ResolvedBinding, cfg Config, opts ManagerOptions) *unitOfWork {
	interceptors := make([]Interceptor, len(opts.Interceptors))
	copy(interceptors, opts.Interceptors)
	return &unitOfWork{
		binding:      binding,
		cfg:          cfg,
		hooks:        opts.Hooks,
		interceptors: interceptors,
	}
}

// Binding implements UnitOfWork.
func (u *unitOfWork) Binding() BindingInfo {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.binding.BindingInfo
}

// InTransaction implements UnitOfWork.
func (u *unitOfWork) InTransaction() bool {
	u.markCancelledIfNeeded()
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.rootStatus == txStatusActive
}

// Root implements UnitOfWork.
func (u *unitOfWork) Root() (Tx, bool) {
	u.markCancelledIfNeeded()
	u.mu.RLock()
	defer u.mu.RUnlock()
	if u.rootStatus != txStatusActive || len(u.stack) == 0 {
		return nil, false
	}
	return u.stack[0].tx, true
}

// Current implements UnitOfWork.
func (u *unitOfWork) Current() (Tx, bool) {
	u.markCancelledIfNeeded()
	u.mu.RLock()
	defer u.mu.RUnlock()
	if u.rootStatus != txStatusActive || len(u.stack) == 0 {
		return nil, false
	}
	current := u.stack[len(u.stack)-1]
	return current.tx, true
}

// CurrentHandle implements UnitOfWork.
func (u *unitOfWork) CurrentHandle() any {
	u.markCancelledIfNeeded()
	u.mu.RLock()
	defer u.mu.RUnlock()
	if u.rootStatus != txStatusActive || len(u.stack) == 0 {
		return u.binding.Client
	}
	return u.stack[len(u.stack)-1].handle
}

// BeginNested implements UnitOfWork.
func (u *unitOfWork) BeginNested(ctx context.Context, opts NestedOptions) (TxScope, error) {
	return u.beginNestedScope(ctx, opts)
}

// SetRollbackOnly implements UnitOfWork.
func (u *unitOfWork) SetRollbackOnly(reason error) error {
	u.markCancelledIfNeeded()
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.rootStatus != txStatusActive {
		return withSentinel(ErrKindState, ErrNoActiveTransaction)
	}
	u.rollbackOnly = true
	if u.rollbackReason == nil && reason != nil {
		u.rollbackReason = reason
	}
	return nil
}

// IsRollbackOnly implements UnitOfWork.
func (u *unitOfWork) IsRollbackOnly() bool {
	u.markCancelledIfNeeded()
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.rootStatus == txStatusActive && u.rollbackOnly
}

// RollbackReason implements UnitOfWork.
func (u *unitOfWork) RollbackReason() error {
	u.markCancelledIfNeeded()
	u.mu.RLock()
	defer u.mu.RUnlock()
	if u.rootStatus != txStatusActive {
		return nil
	}
	return u.rollbackReason
}

// CommitRoot implements RootController.
func (u *unitOfWork) CommitRoot(ctx context.Context) error {
	outcome := u.commitRootManaged(ctx)
	if outcome.err == nil {
		return nil
	}
	if !outcome.rollbackable {
		return outcome.err
	}
	return commitFailureError(outcome.err, u.rollbackRootManaged(ctx, outcome.err))
}

// RollbackRoot implements RootController.
func (u *unitOfWork) RollbackRoot(ctx context.Context) error {
	outcome := u.rollbackRootManaged(ctx, errManualRollback)
	if outcome.err != nil {
		return outcome.err
	}
	if outcome.attemptErr == nil {
		return outcome.beforeErr
	}
	if outcome.beforeErr == nil {
		return withSentinel(ErrKindTransaction, ErrRollbackFailed, outcome.attemptErr)
	}
	return withSentinel(ErrKindTransaction, ErrFinalizationFailed, outcome.beforeErr, outcome.attemptErr)
}

// Tx implements TxScope.
func (s *txScope) Tx() Tx {
	if s == nil {
		return nil
	}
	return s.state.tx
}

// Commit implements TxScope.
func (s *txScope) Commit(ctx context.Context) error {
	if s == nil || s.u == nil {
		return withSentinel(ErrKindState, ErrTxAlreadyClosed)
	}
	outcome := s.u.commitScopeManaged(ctx, s.state)
	if outcome.err == nil {
		return nil
	}
	if !outcome.rollbackable {
		return outcome.err
	}
	return commitFailureError(outcome.err, s.u.rollbackScopeManaged(ctx, s.state, outcome.err))
}

// Rollback implements TxScope.
func (s *txScope) Rollback(ctx context.Context) error {
	if s == nil || s.u == nil {
		return withSentinel(ErrKindState, ErrTxAlreadyClosed)
	}
	outcome := s.u.rollbackScopeManaged(ctx, s.state, errManualRollback)
	if outcome.err != nil {
		return outcome.err
	}
	if outcome.attemptErr == nil {
		return outcome.beforeErr
	}
	if outcome.beforeErr == nil {
		return withSentinel(ErrKindTransaction, ErrRollbackFailed, outcome.attemptErr)
	}
	return withSentinel(ErrKindTransaction, ErrFinalizationFailed, outcome.beforeErr, outcome.attemptErr)
}

func (u *unitOfWork) validateReentrantRoot(opts BeginOptions) error {
	u.mu.RLock()
	defer u.mu.RUnlock()
	if u.rootStatus != txStatusActive {
		return withSentinel(ErrKindState, ErrNoActiveTransaction)
	}
	if opts.ReadOnly && !u.rootOptions.ReadOnly {
		return withSentinel(ErrKindState, ErrBindingImmutable, fmt.Errorf("uow: re-entrant transaction cannot require read-only when the root is read-write"))
	}
	if opts.IsolationLevel != "" && opts.IsolationLevel != u.rootOptions.IsolationLevel {
		return withSentinel(ErrKindState, ErrBindingImmutable, fmt.Errorf("uow: re-entrant transaction cannot change root isolation level"))
	}
	if opts.Timeout > 0 && opts.Timeout != u.rootOptions.Timeout {
		return withSentinel(ErrKindState, ErrBindingImmutable, fmt.Errorf("uow: re-entrant transaction cannot change root timeout"))
	}
	if opts.Label != "" && opts.Label != u.rootOptions.Label {
		return withSentinel(ErrKindState, ErrBindingImmutable, fmt.Errorf("uow: re-entrant transaction cannot change root label"))
	}
	return nil
}

func (u *unitOfWork) beginRoot(ctx context.Context, txID string, opts BeginOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	u.opMu.Lock()
	defer u.opMu.Unlock()

	u.mu.Lock()
	switch u.rootStatus {
	case txStatusActive:
		u.mu.Unlock()
		return withSentinel(ErrKindState, ErrRootOwnershipViolation, fmt.Errorf("uow: root transaction already active"))
	case txStatusCommitted, txStatusRolledBack, txStatusFailed:
		u.mu.Unlock()
		return withSentinel(ErrKindState, ErrRootOwnershipViolation, fmt.Errorf("uow: this UnitOfWork already finalized a root transaction"))
	}
	meta := u.meta(txID, 0, opts.Label, nil)
	client := u.binding.Client
	adapter := u.binding.Adapter
	u.mu.Unlock()

	beforeErr := u.runBeforeBegin(ctx, meta)
	opErr := beforeErr
	var tx Tx
	if opErr == nil {
		tx, opErr = adapter.Begin(ctx, client, opts)
	}
	u.runAfterBegin(ctx, meta, opErr)
	if opErr != nil {
		if beforeErr != nil {
			return withSentinel(ErrKindTransaction, ErrBeginAborted, beforeErr)
		}
		return withSentinel(ErrKindTransaction, ErrBeginFailed, opErr)
	}

	scope := &scopeState{
		tx:     tx,
		handle: adapter.Unwrap(tx),
		depth:  0,
		label:  opts.Label,
		mode:   scopeModePhysical,
		status: txStatusActive,
	}

	u.mu.Lock()
	u.rootStatus = txStatusActive
	u.rootCtx = ctx
	u.txID = txID
	u.rootOptions = opts
	u.stack = []*scopeState{scope}
	u.rollbackOnly = false
	u.rollbackReason = nil
	u.mu.Unlock()

	u.runOnBegin(ctx, meta)
	return nil
}

func (u *unitOfWork) beginNestedScope(ctx context.Context, opts NestedOptions) (*txScope, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	u.opMu.Lock()
	defer u.opMu.Unlock()

	u.markCancelledIfNeeded()

	u.mu.Lock()
	if u.rootStatus != txStatusActive || len(u.stack) == 0 {
		u.mu.Unlock()
		return nil, withSentinel(ErrKindState, ErrNoActiveTransaction)
	}
	if u.cfg.NestedMode == NestedStrict && u.rollbackOnly {
		reason := u.rollbackReason
		u.mu.Unlock()
		if reason != nil {
			return nil, withSentinel(ErrKindTransaction, ErrRollbackOnly, reason)
		}
		return nil, withSentinel(ErrKindTransaction, ErrRollbackOnly)
	}
	parent := u.stack[len(u.stack)-1]
	depth := len(u.stack)
	meta := u.meta(u.txID, depth, opts.Label, nil)
	adapter := u.binding.Adapter
	rollbackOnly := u.rollbackOnly
	currentHandle := parent.handle
	u.mu.Unlock()

	beforeErr := u.runBeforeBegin(ctx, meta)
	opErr := beforeErr
	var scope *scopeState
	if opErr == nil {
		switch u.cfg.NestedMode {
		case NestedStrict:
			caps := adapter.Capabilities()
			if !caps.NestedTransaction && !caps.Savepoints {
				opErr = withSentinel(ErrKindAdapter, ErrNestedTxUnsupported, fmt.Errorf("uow: adapter %q does not support nested transactions", u.binding.AdapterName))
			} else {
				tx, err := adapter.BeginNested(ctx, parent.tx, opts)
				opErr = err
				if err == nil {
					scope = &scopeState{
						tx:     tx,
						handle: adapter.Unwrap(tx),
						depth:  depth,
						label:  opts.Label,
						mode:   scopeModePhysical,
						status: txStatusActive,
					}
				}
			}
		case NestedEmulated:
			mode := scopeModeEmulated
			if rollbackOnly {
				mode = scopeModeEmulatedInherited
			}
			scope = &scopeState{
				tx: &logicalTx{
					name:  u.txID + ".nested." + strconv.Itoa(depth),
					depth: depth,
					raw:   currentHandle,
				},
				handle: currentHandle,
				depth:  depth,
				label:  opts.Label,
				mode:   mode,
				status: txStatusActive,
			}
		default:
			opErr = classifyError(ErrKindConfig, fmt.Errorf("uow: invalid nested mode %d", u.cfg.NestedMode))
		}
	}
	u.runAfterBegin(ctx, meta, opErr)
	if opErr != nil {
		if beforeErr != nil {
			return nil, withSentinel(ErrKindTransaction, ErrBeginAborted, beforeErr)
		}
		if errors.Is(opErr, ErrNestedTxUnsupported) {
			return nil, opErr
		}
		return nil, withSentinel(ErrKindTransaction, ErrBeginFailed, opErr)
	}

	u.mu.Lock()
	u.stack = append(u.stack, scope)
	u.mu.Unlock()

	u.runOnNestedBegin(ctx, meta)
	return &txScope{u: u, state: scope}, nil
}

func (u *unitOfWork) commitRootManaged(ctx context.Context) commitOutcome {
	u.mu.RLock()
	if u.rootStatus != txStatusActive || len(u.stack) == 0 {
		rootStatus := u.rootStatus
		u.mu.RUnlock()
		if rootStatus == txStatusNone {
			return commitOutcome{err: withSentinel(ErrKindState, ErrNoActiveTransaction)}
		}
		return commitOutcome{err: withSentinel(ErrKindState, ErrTxAlreadyClosed)}
	}
	root := u.stack[0]
	u.mu.RUnlock()
	return u.commitScope(ctx, root)
}

func (u *unitOfWork) rollbackRootManaged(ctx context.Context, cause error) rollbackOutcome {
	u.mu.RLock()
	if u.rootStatus != txStatusActive || len(u.stack) == 0 {
		rootStatus := u.rootStatus
		u.mu.RUnlock()
		if rootStatus == txStatusNone {
			return rollbackOutcome{err: withSentinel(ErrKindState, ErrNoActiveTransaction)}
		}
		return rollbackOutcome{err: withSentinel(ErrKindState, ErrTxAlreadyClosed)}
	}
	root := u.stack[0]
	u.mu.RUnlock()
	return u.rollbackScope(ctx, root, cause)
}

func (u *unitOfWork) commitScopeManaged(ctx context.Context, scope *scopeState) commitOutcome {
	return u.commitScope(ctx, scope)
}

func (u *unitOfWork) rollbackScopeManaged(ctx context.Context, scope *scopeState, cause error) rollbackOutcome {
	return u.rollbackScope(ctx, scope, cause)
}

func (u *unitOfWork) commitScope(ctx context.Context, scope *scopeState) commitOutcome {
	if ctx == nil {
		ctx = context.Background()
	}
	u.opMu.Lock()
	defer u.opMu.Unlock()

	u.markCancelledIfNeeded()

	u.mu.Lock()
	if u.rootStatus != txStatusActive || len(u.stack) == 0 {
		rootStatus := u.rootStatus
		u.mu.Unlock()
		if rootStatus == txStatusNone {
			return commitOutcome{err: withSentinel(ErrKindState, ErrNoActiveTransaction)}
		}
		return commitOutcome{err: withSentinel(ErrKindState, ErrTxAlreadyClosed)}
	}
	top := u.stack[len(u.stack)-1]
	if scope == nil || scope.status != txStatusActive {
		u.mu.Unlock()
		return commitOutcome{err: withSentinel(ErrKindState, ErrTxAlreadyClosed)}
	}
	if top != scope {
		u.mu.Unlock()
		return commitOutcome{err: withSentinel(ErrKindState, ErrScopeOrderViolation)}
	}
	if scope.depth == 0 && u.rollbackOnly {
		reason := u.rollbackReason
		u.mu.Unlock()
		if reason != nil {
			return commitOutcome{err: withSentinel(ErrKindTransaction, ErrRollbackOnly, reason)}
		}
		return commitOutcome{err: withSentinel(ErrKindTransaction, ErrRollbackOnly)}
	}
	meta := u.meta(u.txID, scope.depth, scope.label, nil)
	mode := scope.mode
	tx := scope.tx
	adapter := u.binding.Adapter
	u.mu.Unlock()

	beforeErr := u.runBeforeCommit(ctx, meta)
	opErr := beforeErr
	if opErr == nil && mode == scopeModePhysical {
		opErr = adapter.Commit(ctx, tx)
	}
	if opErr == nil {
		u.mu.Lock()
		if scope.depth == 0 {
			u.finishRootLocked(scope, txStatusCommitted)
		} else {
			u.finishNestedLocked(scope, txStatusCommitted)
		}
		u.mu.Unlock()
	}

	u.runAfterCommit(ctx, meta, opErr)
	u.runOnCommit(ctx, meta, opErr)
	if opErr != nil {
		return commitOutcome{err: opErr, rollbackable: true}
	}
	return commitOutcome{}
}

func (u *unitOfWork) rollbackScope(ctx context.Context, scope *scopeState, cause error) rollbackOutcome {
	if ctx == nil {
		ctx = context.Background()
	}
	u.opMu.Lock()
	defer u.opMu.Unlock()

	u.mu.Lock()
	if u.rootStatus != txStatusActive || len(u.stack) == 0 {
		rootStatus := u.rootStatus
		u.mu.Unlock()
		if rootStatus == txStatusNone {
			return rollbackOutcome{err: withSentinel(ErrKindState, ErrNoActiveTransaction)}
		}
		return rollbackOutcome{err: withSentinel(ErrKindState, ErrTxAlreadyClosed)}
	}
	top := u.stack[len(u.stack)-1]
	if scope == nil || scope.status != txStatusActive {
		u.mu.Unlock()
		return rollbackOutcome{err: withSentinel(ErrKindState, ErrTxAlreadyClosed)}
	}
	if top != scope {
		u.mu.Unlock()
		return rollbackOutcome{err: withSentinel(ErrKindState, ErrScopeOrderViolation)}
	}
	meta := u.meta(u.txID, scope.depth, scope.label, cause)
	mode := scope.mode
	tx := scope.tx
	adapter := u.binding.Adapter
	u.mu.Unlock()

	beforeErr := u.runBeforeRollback(ctx, meta)
	var attemptErr error
	if mode == scopeModePhysical {
		attemptErr = adapter.Rollback(ctx, tx)
	}
	if attemptErr == nil {
		u.mu.Lock()
		if scope.depth == 0 {
			u.finishRootLocked(scope, txStatusRolledBack)
		} else {
			if scope.mode == scopeModeEmulated && !u.rollbackOnly {
				u.rollbackOnly = true
				if u.rollbackReason == nil && cause != nil {
					u.rollbackReason = cause
				}
			}
			u.finishNestedLocked(scope, txStatusRolledBack)
		}
		u.mu.Unlock()
	} else {
		u.mu.Lock()
		u.failExecutionLocked(scope)
		u.mu.Unlock()
	}

	effectiveErr := composeErrors(beforeErr, attemptErr)
	u.runAfterRollback(ctx, meta, effectiveErr)
	u.runOnRollback(ctx, meta, effectiveErr)
	return rollbackOutcome{
		beforeErr:  beforeErr,
		attemptErr: attemptErr,
	}
}

func (u *unitOfWork) finishRootLocked(scope *scopeState, status txStatus) {
	scope.status = status
	u.rootStatus = status
	u.stack = nil
	u.rootCtx = nil
	u.rootOptions = BeginOptions{}
	u.rollbackOnly = false
	u.rollbackReason = nil
}

func (u *unitOfWork) finishNestedLocked(scope *scopeState, status txStatus) {
	scope.status = status
	u.stack = u.stack[:len(u.stack)-1]
}

func (u *unitOfWork) failExecutionLocked(scope *scopeState) {
	scope.status = txStatusFailed
	if len(u.stack) > 0 {
		u.stack[0].status = txStatusFailed
	}
	u.rootStatus = txStatusFailed
	u.stack = nil
	u.rootCtx = nil
	u.rootOptions = BeginOptions{}
	u.rollbackOnly = false
	u.rollbackReason = nil
}

func (u *unitOfWork) meta(txID string, depth int, label string, rollbackCause error) TxMeta {
	return TxMeta{
		TxID:          txID,
		AdapterName:   u.binding.AdapterName,
		ClientName:    u.binding.ClientName,
		TenantID:      u.binding.TenantID,
		Depth:         depth,
		Label:         label,
		RollbackCause: rollbackCause,
	}
}

func (u *unitOfWork) runBeforeBegin(ctx context.Context, meta TxMeta) error {
	for _, interceptor := range u.interceptors {
		if err := interceptor.BeforeBegin(ctx, meta); err != nil {
			return err
		}
	}
	return nil
}

func (u *unitOfWork) runAfterBegin(ctx context.Context, meta TxMeta, err error) {
	for _, interceptor := range u.interceptors {
		interceptor.AfterBegin(ctx, meta, err)
	}
}

func (u *unitOfWork) runOnBegin(ctx context.Context, meta TxMeta) {
	if u.hooks != nil {
		u.hooks.OnBegin(ctx, meta)
	}
}

func (u *unitOfWork) runOnNestedBegin(ctx context.Context, meta TxMeta) {
	if u.hooks != nil {
		u.hooks.OnNestedBegin(ctx, meta)
	}
}

func (u *unitOfWork) runBeforeCommit(ctx context.Context, meta TxMeta) error {
	for _, interceptor := range u.interceptors {
		if err := interceptor.BeforeCommit(ctx, meta); err != nil {
			return err
		}
	}
	return nil
}

func (u *unitOfWork) runAfterCommit(ctx context.Context, meta TxMeta, err error) {
	for _, interceptor := range u.interceptors {
		interceptor.AfterCommit(ctx, meta, err)
	}
}

func (u *unitOfWork) runOnCommit(ctx context.Context, meta TxMeta, err error) {
	if u.hooks != nil {
		u.hooks.OnCommit(ctx, meta, err)
	}
}

func (u *unitOfWork) runBeforeRollback(ctx context.Context, meta TxMeta) error {
	var errs []error
	for _, interceptor := range u.interceptors {
		if err := interceptor.BeforeRollback(ctx, meta); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return composeErrors(errs[0], errs[1:]...)
}

func (u *unitOfWork) runAfterRollback(ctx context.Context, meta TxMeta, err error) {
	for _, interceptor := range u.interceptors {
		interceptor.AfterRollback(ctx, meta, err)
	}
}

func (u *unitOfWork) runOnRollback(ctx context.Context, meta TxMeta, err error) {
	if u.hooks != nil {
		u.hooks.OnRollback(ctx, meta, err)
	}
}

func (u *unitOfWork) markCancelledIfNeeded() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.rootStatus != txStatusActive || u.rootCtx == nil {
		return
	}
	if u.rootCtx.Err() != nil {
		u.rollbackOnly = true
		if u.rollbackReason == nil {
			u.rollbackReason = context.Canceled
		}
	}
}
