package uow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync/atomic"
)

// ManagerOptions configures optional collaborators for Manager.
type ManagerOptions struct {
	TenantPolicy TenantResolutionPolicy
	Hooks        Hooks
	Interceptors []Interceptor
	Logger       *slog.Logger
}

// Manager owns binding resolution and managed execution.
type Manager struct {
	registry *Registry
	cfg      Config
	opts     ManagerOptions
	seq      atomic.Uint64
}

// NewManager constructs a Manager.
func NewManager(registry *Registry, cfg Config, opts ManagerOptions) (*Manager, error) {
	if registry == nil {
		return nil, classifyError(ErrKindConfig, errNilRegistry)
	}
	cfg = cfg.normalized()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	copied := make([]Interceptor, len(opts.Interceptors))
	copy(copied, opts.Interceptors)
	opts.Interceptors = copied

	return &Manager{
		registry: registry,
		cfg:      cfg,
		opts:     opts,
	}, nil
}

// ResolveInfo resolves public binding metadata.
func (m *Manager) ResolveInfo(ctx context.Context, req ResolutionRequest) (BindingInfo, error) {
	binding, err := m.ResolveBinding(ctx, req)
	if err != nil {
		return BindingInfo{}, err
	}
	return binding.BindingInfo, nil
}

// ResolveBinding resolves an owner-facing binding.
func (m *Manager) ResolveBinding(ctx context.Context, req ResolutionRequest) (ResolvedBinding, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if m == nil || m.registry == nil {
		return ResolvedBinding{}, classifyError(ErrKindConfig, errNilRegistry)
	}
	override, _ := BindingOverrideFrom(ctx)
	selectors, err := mergeSelectors(req, override, m.cfg)
	if err != nil {
		return ResolvedBinding{}, err
	}
	tenant, err := m.resolveTenant(ctx, selectors.tenant)
	if err != nil {
		return ResolvedBinding{}, err
	}

	regs := m.registry.Registrations()
	if len(regs) == 0 {
		return ResolvedBinding{}, withSentinel(ErrKindResolver, ErrNoAdapterRegistered)
	}

	if selectors.adapter.value != "" {
		regs = filterByAdapter(regs, selectors.adapter.value)
		if len(regs) == 0 {
			return ResolvedBinding{}, withSentinel(ErrKindResolver, ErrAdapterNotFound, fmt.Errorf("uow: adapter %q not registered", selectors.adapter.value))
		}
	}
	if selectors.client.value != "" {
		regs = filterByClient(regs, selectors.client.value)
		if len(regs) == 0 {
			return ResolvedBinding{}, withSentinel(ErrKindResolver, ErrClientNotFound, fmt.Errorf("uow: client %q not registered", selectors.client.value))
		}
	}

	if tenant.explicitNone || tenant.value == "" {
		global := make([]Registration, 0, len(regs))
		for _, reg := range regs {
			if reg.TenantID == "" {
				global = append(global, reg)
			}
		}
		reg, err := chooseRegistration(global, selectors)
		if err != nil {
			return ResolvedBinding{}, err
		}
		if reg.Adapter == nil {
			return ResolvedBinding{}, resolutionMiss(selectors, tenant)
		}
		return resolvedBindingFromRegistration(reg, ""), nil
	}

	exact, global := splitTenantCandidates(regs, tenant)
	reg, err := chooseRegistration(exact, selectors)
	if err != nil {
		return ResolvedBinding{}, err
	}
	if reg.Adapter != nil {
		return resolvedBindingFromRegistration(reg, tenant.value), nil
	}

	allowFallback := tenant.derived && !m.cfg.RequireTenantResolution
	if !allowFallback {
		return ResolvedBinding{}, withSentinel(ErrKindTenant, ErrTenantBindingNotFound, fmt.Errorf("uow: no binding registered for tenant %q", tenant.value))
	}

	reg, err = chooseRegistration(global, selectors)
	if err != nil {
		return ResolvedBinding{}, err
	}
	if reg.Adapter == nil {
		return ResolvedBinding{}, withSentinel(ErrKindTenant, ErrTenantBindingNotFound, fmt.Errorf("uow: no tenant fallback binding registered for tenant %q", tenant.value))
	}
	return resolvedBindingFromRegistration(reg, tenant.value), nil
}

// Bind resolves or reuses a non-transactional execution-scoped UnitOfWork.
func (m *Manager) Bind(ctx context.Context, cfg ExecutionConfig) (context.Context, UnitOfWork, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := cfg.Validate(); err != nil {
		return ctx, nil, err
	}
	binding, err := m.ResolveBinding(ctx, requestFromExecution(cfg))
	if err != nil {
		return ctx, nil, err
	}

	if existing, ok := From(ctx); ok {
		if !bindingInfoEqual(existing.Binding(), binding.BindingInfo) {
			return ctx, nil, withSentinel(ErrKindState, ErrBindingImmutable, fmt.Errorf("uow: execution already bound to adapter=%q client=%q tenant=%q", existing.Binding().AdapterName, existing.Binding().ClientName, existing.Binding().TenantID))
		}
		return With(ctx, existing), existing, nil
	}

	u := newUnitOfWork(binding, m.cfg, m.opts)
	return With(ctx, u), u, nil
}

// Do executes a managed ambient callback with a propagated UnitOfWork.
func (m *Manager) Do(ctx context.Context, cfg ExecutionConfig, fn func(ctx context.Context) error) error {
	if fn == nil {
		return classifyError(ErrKindConfig, errNilCallback)
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if m.shouldStartTransaction(cfg) {
		return m.executeManagedRoot(ctx, requestFromExecution(cfg), beginOptionsFromExecution(cfg), fn)
	}
	nextCtx, _, err := m.Bind(ctx, cfg)
	if err != nil {
		return err
	}
	return fn(nextCtx)
}

// InTx executes fn in a root transaction or nested scope.
func (m *Manager) InTx(ctx context.Context, cfg TxConfig, fn func(ctx context.Context) error) error {
	if fn == nil {
		return classifyError(ErrKindConfig, errNilCallback)
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	return m.executeManagedRoot(ctx, requestFromTx(cfg), beginOptionsFromTx(cfg), fn)
}

// InNestedTx executes fn in a nested transaction scope.
func (m *Manager) InNestedTx(ctx context.Context, opts NestedOptions, fn func(ctx context.Context) error) error {
	if fn == nil {
		return classifyError(ErrKindConfig, errNilCallback)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	existing, ok := From(ctx)
	if !ok || !existing.InTransaction() {
		return withSentinel(ErrKindState, ErrNoActiveTransaction)
	}
	if managed, ok := existing.(*unitOfWork); ok {
		return m.executeNestedManaged(ctx, managed, opts, fn)
	}
	return m.executeNestedGeneric(ctx, existing, opts, fn)
}

type defaultFinalizePolicy struct{}

func (defaultFinalizePolicy) ShouldRollback(_ context.Context, input FinalizeInput) bool {
	switch {
	case input.PanicValue != nil:
		return true
	case input.Err != nil:
		return true
	case input.UOW != nil && input.UOW.IsRollbackOnly():
		return true
	case input.ContextCancelled:
		return true
	default:
		return false
	}
}

func (m *Manager) executeManagedRoot(ctx context.Context, req ResolutionRequest, beginOpts BeginOptions, fn func(context.Context) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if existing, ok := From(ctx); ok && existing.InTransaction() {
		binding, err := m.ResolveBinding(ctx, req)
		if err != nil {
			return err
		}
		if !bindingInfoEqual(existing.Binding(), binding.BindingInfo) {
			return withSentinel(ErrKindState, ErrMultipleRootBindingsForbidden, fmt.Errorf("uow: existing root binding adapter=%q client=%q tenant=%q conflicts with requested adapter=%q client=%q tenant=%q", existing.Binding().AdapterName, existing.Binding().ClientName, existing.Binding().TenantID, binding.AdapterName, binding.ClientName, binding.TenantID))
		}
		if managed, ok := existing.(*unitOfWork); ok {
			if err := managed.validateReentrantRoot(beginOpts); err != nil {
				return err
			}
			return m.executeNestedManaged(ctx, managed, NestedOptions{Label: beginOpts.Label}, fn)
		}
		if hasAssertiveBeginOptions(beginOpts) {
			return withSentinel(ErrKindState, ErrRootOwnershipViolation, fmt.Errorf("uow: cannot validate re-entrant root options against an external UnitOfWork implementation"))
		}
		return m.executeNestedGeneric(ctx, existing, NestedOptions{Label: beginOpts.Label}, fn)
	}

	binding, err := m.ResolveBinding(ctx, req)
	if err != nil {
		return err
	}
	if !binding.Adapter.Capabilities().RootTransaction {
		return withSentinel(ErrKindAdapter, ErrRootTxUnsupported, fmt.Errorf("uow: adapter %q does not support root transactions", binding.AdapterName))
	}
	beginOpts, err = m.applyBeginOptionsPolicy(ctx, binding, beginOpts)
	if err != nil {
		return err
	}

	var u *unitOfWork
	if existing, ok := From(ctx); ok {
		if !bindingInfoEqual(existing.Binding(), binding.BindingInfo) {
			return withSentinel(ErrKindState, ErrMultipleRootBindingsForbidden, fmt.Errorf("uow: existing UnitOfWork binding adapter=%q client=%q tenant=%q conflicts with requested adapter=%q client=%q tenant=%q", existing.Binding().AdapterName, existing.Binding().ClientName, existing.Binding().TenantID, binding.AdapterName, binding.ClientName, binding.TenantID))
		}
		managed, ok := existing.(*unitOfWork)
		if !ok {
			return withSentinel(ErrKindState, ErrRootOwnershipViolation, fmt.Errorf("uow: cannot promote an external UnitOfWork implementation into transactional mode"))
		}
		u = managed
	} else {
		u = newUnitOfWork(binding, m.cfg, m.opts)
		ctx = With(ctx, u)
	}

	if err := u.beginRoot(ctx, m.nextTxID(), beginOpts); err != nil {
		return err
	}

	cbCtx := With(ctx, u)
	var fnErr error
	var panicValue any
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				panicValue = recovered
			}
		}()
		fnErr = fn(cbCtx)
	}()

	finalizeErr := m.finalizeRoot(ctx, u, fnErr, panicValue)
	if panicValue != nil {
		if finalizeErr != nil {
			m.logFinalizeFailure(ctx, "panic root finalization failed", finalizeErr)
		}
		panic(panicValue)
	}
	return finalizeErr
}

func (m *Manager) executeNestedManaged(ctx context.Context, u *unitOfWork, opts NestedOptions, fn func(context.Context) error) error {
	scope, err := u.beginNestedScope(ctx, opts)
	if err != nil {
		return err
	}
	cbCtx := With(ctx, u)
	var fnErr error
	var panicValue any
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				panicValue = recovered
			}
		}()
		fnErr = fn(cbCtx)
	}()
	if panicValue != nil {
		outcome := u.rollbackScopeManaged(ctx, scope.state, panicCause(panicValue))
		if secondary := rollbackSecondaryError(outcome); secondary != nil {
			m.logFinalizeFailure(ctx, "panic nested finalization failed", secondary)
		}
		panic(panicValue)
	}
	if fnErr != nil {
		outcome := u.rollbackScopeManaged(ctx, scope.state, fnErr)
		return composeErrors(fnErr, rollbackSecondaryError(outcome))
	}
	if err := ctx.Err(); err != nil {
		outcome := u.rollbackScopeManaged(ctx, scope.state, context.Canceled)
		primary := withSentinel(ErrKindTransaction, ErrContextCancelled, context.Canceled)
		return composeErrors(primary, rollbackSecondaryError(outcome))
	}
	commit := u.commitScopeManaged(ctx, scope.state)
	if commit.err == nil {
		return nil
	}
	if !commit.rollbackable {
		return commit.err
	}
	return commitFailureError(commit.err, u.rollbackScopeManaged(ctx, scope.state, commit.err))
}

func (m *Manager) executeNestedGeneric(ctx context.Context, u UnitOfWork, opts NestedOptions, fn func(context.Context) error) error {
	scope, err := u.BeginNested(ctx, opts)
	if err != nil {
		return err
	}
	cbCtx := With(ctx, u)
	var fnErr error
	var panicValue any
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				panicValue = recovered
			}
		}()
		fnErr = fn(cbCtx)
	}()
	if panicValue != nil {
		if rollbackErr := scope.Rollback(ctx); rollbackErr != nil {
			m.logFinalizeFailure(ctx, "panic nested finalization failed", rollbackErr)
		}
		panic(panicValue)
	}
	if fnErr != nil {
		return composeErrors(fnErr, scope.Rollback(ctx))
	}
	if ctx != nil && ctx.Err() != nil {
		primary := withSentinel(ErrKindTransaction, ErrContextCancelled, context.Canceled)
		return composeErrors(primary, scope.Rollback(ctx))
	}
	return scope.Commit(ctx)
}

func (m *Manager) finalizeRoot(ctx context.Context, u *unitOfWork, fnErr error, panicValue any) error {
	input := FinalizeInput{
		Err:              fnErr,
		PanicValue:       panicValue,
		ContextCancelled: ctx != nil && ctx.Err() != nil,
		UOW:              u,
	}
	shouldRollback, cause := m.shouldRollback(ctx, input)
	if shouldRollback {
		outcome := u.rollbackRootManaged(ctx, cause)
		if panicValue != nil {
			return rollbackSecondaryError(outcome)
		}
		if fnErr != nil {
			return composeErrors(fnErr, rollbackSecondaryError(outcome))
		}
		primary := primaryRootRollbackError(cause)
		if outcome.err != nil {
			return outcome.err
		}
		if outcome.attemptErr == nil {
			return composeErrors(primary, outcome.beforeErr)
		}
		if outcome.beforeErr == nil {
			return withSentinel(ErrKindTransaction, ErrRollbackFailed, primary, outcome.attemptErr)
		}
		return withSentinel(ErrKindTransaction, ErrFinalizationFailed, primary, outcome.beforeErr, outcome.attemptErr)
	}

	commit := u.commitRootManaged(ctx)
	if commit.err == nil {
		return fnErr
	}
	if !commit.rollbackable {
		if fnErr != nil {
			return composeErrors(fnErr, commit.err)
		}
		return commit.err
	}
	secondary := commitFailureError(commit.err, u.rollbackRootManaged(ctx, commit.err))
	if fnErr != nil {
		return composeErrors(fnErr, secondary)
	}
	return secondary
}

func (m *Manager) shouldRollback(ctx context.Context, input FinalizeInput) (bool, error) {
	if input.PanicValue != nil {
		return true, panicCause(input.PanicValue)
	}
	policy := m.cfg.DefaultFinalizePolicy
	if policy == nil {
		policy = defaultFinalizePolicy{}
	}
	policyWantsRollback := policy.ShouldRollback(ctx, input)
	if policyWantsRollback && input.Err != nil {
		return true, input.Err
	}
	if input.UOW != nil && input.UOW.IsRollbackOnly() {
		if reason := input.UOW.RollbackReason(); reason != nil {
			return true, reason
		}
		return true, ErrRollbackOnly
	}
	if input.ContextCancelled {
		return true, context.Canceled
	}
	if policyWantsRollback {
		return true, errPolicyRequestedRollback
	}
	return false, nil
}

func (m *Manager) shouldStartTransaction(cfg ExecutionConfig) bool {
	switch cfg.Transactional {
	case TransactionalOn:
		return true
	case TransactionalOff:
		return false
	default:
		return m.cfg.TransactionMode == GlobalAuto
	}
}

func (m *Manager) nextTxID() string {
	return "tx-" + strconv.FormatUint(m.seq.Add(1), 10)
}

func (m *Manager) applyBeginOptionsPolicy(ctx context.Context, binding ResolvedBinding, opts BeginOptions) (BeginOptions, error) {
	caps := binding.Adapter.Capabilities()
	effective := opts

	if opts.ReadOnly && !caps.ReadOnlyTx {
		if err := m.handleUnsupportedOption(ctx, binding, "read_only", opts.ReadOnly); err != nil {
			return BeginOptions{}, err
		}
		effective.ReadOnly = false
	}
	if opts.IsolationLevel != "" && !caps.IsolationLevels {
		if err := m.handleUnsupportedOption(ctx, binding, "isolation_level", opts.IsolationLevel); err != nil {
			return BeginOptions{}, err
		}
		effective.IsolationLevel = ""
	}
	if opts.Timeout > 0 && !caps.Timeouts {
		if err := m.handleUnsupportedOption(ctx, binding, "timeout", opts.Timeout); err != nil {
			return BeginOptions{}, err
		}
		effective.Timeout = 0
	}
	return effective, nil
}

func (m *Manager) handleUnsupportedOption(ctx context.Context, binding ResolvedBinding, option string, value any) error {
	if m.cfg.StrictOptionEnforcement || !m.cfg.AllowOptionDowngrade {
		return classifyError(ErrKindAdapter, fmt.Errorf("uow: adapter %q does not support %s", binding.AdapterName, option))
	}
	if m.opts.Logger != nil {
		m.opts.Logger.WarnContext(ctx, "uow option downgraded",
			"adapter", binding.AdapterName,
			"client", binding.ClientName,
			"tenant", binding.TenantID,
			"option", option,
			"value", value,
		)
	}
	return nil
}

func (m *Manager) logFinalizeFailure(ctx context.Context, msg string, err error) {
	if err == nil || m.opts.Logger == nil {
		return
	}
	m.opts.Logger.ErrorContext(ctx, msg, "error", err)
}

func requestFromExecution(cfg ExecutionConfig) ResolutionRequest {
	return cfg.shared().resolutionRequest(ResolutionAmbient)
}

func requestFromTx(cfg TxConfig) ResolutionRequest {
	return cfg.shared().resolutionRequest(ResolutionExplicit)
}

func beginOptionsFromExecution(cfg ExecutionConfig) BeginOptions {
	return cfg.shared().beginOptions()
}

func beginOptionsFromTx(cfg TxConfig) BeginOptions {
	return cfg.shared().beginOptions()
}

func hasAssertiveBeginOptions(opts BeginOptions) bool {
	return opts.ReadOnly || opts.IsolationLevel != "" || opts.Timeout > 0 || opts.Label != ""
}

func primaryRootRollbackError(cause error) error {
	switch {
	case cause == nil:
		return errPolicyRequestedRollback
	case errors.Is(cause, context.Canceled):
		return withSentinel(ErrKindTransaction, ErrContextCancelled, context.Canceled)
	case errors.Is(cause, ErrRollbackOnly):
		return withSentinel(ErrKindTransaction, ErrRollbackOnly)
	default:
		return cause
	}
}

func rollbackSecondaryError(outcome rollbackOutcome) error {
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

func commitFailureError(commitErr error, rollback rollbackOutcome) error {
	if rollback.err != nil {
		return withSentinel(ErrKindTransaction, ErrFinalizationFailed, commitErr, rollback.err, rollback.beforeErr, rollback.attemptErr)
	}
	if rollback.attemptErr == nil && rollback.beforeErr == nil {
		return withSentinel(ErrKindTransaction, ErrCommitAborted, commitErr)
	}
	if rollback.attemptErr == nil {
		return withSentinel(ErrKindTransaction, ErrFinalizationFailed, commitErr, rollback.beforeErr)
	}
	return withSentinel(ErrKindTransaction, ErrFinalizationFailed, commitErr, rollback.beforeErr, rollback.attemptErr)
}

func panicCause(value any) error {
	return fmt.Errorf("uow: panic detected: %v", value)
}

func bindingInfoEqual(left, right BindingInfo) bool {
	return left.AdapterName == right.AdapterName &&
		left.ClientName == right.ClientName &&
		left.TenantID == right.TenantID
}

func resolvedBindingFromRegistration(reg Registration, tenantID string) ResolvedBinding {
	return ResolvedBinding{
		BindingInfo: BindingInfo{
			AdapterName: reg.AdapterName,
			ClientName:  reg.ClientName,
			TenantID:    tenantID,
		},
		Adapter: reg.Adapter,
		Client:  reg.Client,
	}
}

func resolutionMiss(selectors resolvedSelectors, tenant tenantChoice) error {
	switch {
	case tenant.value != "":
		return withSentinel(ErrKindTenant, ErrTenantBindingNotFound, fmt.Errorf("uow: no binding registered for tenant %q", tenant.value))
	case selectors.client.value != "":
		return withSentinel(ErrKindResolver, ErrClientNotFound, fmt.Errorf("uow: client %q not registered", selectors.client.value))
	case selectors.adapter.value != "":
		return withSentinel(ErrKindResolver, ErrAdapterNotFound, fmt.Errorf("uow: adapter %q not registered", selectors.adapter.value))
	default:
		return withSentinel(ErrKindResolver, ErrNoAdapterRegistered)
	}
}
