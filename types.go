package uow

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// NestedMode controls how nested transactions behave once a root transaction
// exists.
type NestedMode int

const (
	// NestedStrict requires adapter-level nested transaction or savepoint
	// support.
	NestedStrict NestedMode = iota
	// NestedEmulated keeps nesting at the UnitOfWork layer and turns nested
	// rollback into root rollback-only.
	NestedEmulated
)

// TransactionMode controls whether ambient execution starts transactions
// automatically.
type TransactionMode int

const (
	// ExplicitOnly starts a transaction only when an explicit transactional API
	// is invoked.
	ExplicitOnly TransactionMode = iota
	// GlobalAuto starts a transaction for ambient executions unless they opt out.
	GlobalAuto
)

// TransactionalMode controls transaction activation for ambient execution.
type TransactionalMode int

const (
	// TransactionalInherit follows the effective transaction mode.
	TransactionalInherit TransactionalMode = iota
	// TransactionalOff disables automatic root transaction creation.
	TransactionalOff
	// TransactionalOn forces automatic root transaction creation for the
	// managed ambient execution.
	TransactionalOn
)

// ResolutionMode controls selector precedence during binding resolution.
type ResolutionMode int

const (
	// ResolutionAmbient applies BindingOverride before an ambient request.
	ResolutionAmbient ResolutionMode = iota
	// ResolutionExplicit applies explicit selectors before BindingOverride.
	ResolutionExplicit
)

// IsolationLevel is a backend-agnostic transaction isolation preference.
//
// The zero value leaves isolation unspecified.
type IsolationLevel string

const (
	// IsolationReadUncommitted requests read-uncommitted semantics.
	IsolationReadUncommitted IsolationLevel = "read_uncommitted"
	// IsolationReadCommitted requests read-committed semantics.
	IsolationReadCommitted IsolationLevel = "read_committed"
	// IsolationRepeatableRead requests repeatable-read semantics.
	IsolationRepeatableRead IsolationLevel = "repeatable_read"
	// IsolationSnapshot requests snapshot semantics.
	IsolationSnapshot IsolationLevel = "snapshot"
	// IsolationSerializable requests serializable semantics.
	IsolationSerializable IsolationLevel = "serializable"
)

// Selector represents an optional explicit binding selection.
//
// The zero value leaves the selector unspecified. A set selector with an empty
// value explicitly requests the default or non-tenant choice for that field.
// Use Select to lock a concrete value, DefaultSelection to request the default
// selection explicitly, and NoTenant as a clearer tenant-specific alias for an
// explicit empty selection.
type Selector struct {
	Set   bool
	Value string
}

// Select returns a Selector locked to one trimmed value.
//
// Prefer SelectAdapter, SelectClient, or SelectTenant when the target field is
// known — they communicate intent at the call site.
func Select(value string) Selector {
	return Selector{Set: true, Value: strings.TrimSpace(value)}
}

// SelectAdapter returns a Selector that explicitly chooses a named adapter.
func SelectAdapter(name string) Selector { return Select(name) }

// SelectClient returns a Selector that explicitly chooses a named client.
func SelectClient(name string) Selector { return Select(name) }

// SelectTenant returns a Selector that explicitly chooses a named tenant.
func SelectTenant(id string) Selector { return Select(id) }

// DefaultSelection returns a Selector that explicitly chooses the default
// binding for adapter or client resolution.
func DefaultSelection() Selector {
	return Selector{Set: true}
}

// NoTenant returns a Selector that explicitly opts out of tenant selection.
//
// It is equivalent to DefaultSelection but reads more clearly when used for
// tenant selectors.
func NoTenant() Selector {
	return DefaultSelection()
}

// BindingInfo exposes resolved execution metadata without backend objects.
type BindingInfo struct {
	AdapterName string
	ClientName  string
	TenantID    string
}

// ResolvedBinding is the owner-facing resolved binding used to create a
// UnitOfWork.
type ResolvedBinding struct {
	BindingInfo
	Adapter Adapter
	Client  any
}

// Capabilities describes an adapter's transaction features.
type Capabilities struct {
	RootTransaction   bool
	NestedTransaction bool
	Savepoints        bool
	ReadOnlyTx        bool
	IsolationLevels   bool
	Timeouts          bool
	MultiTenantAware  bool
}

// BeginOptions defines root transaction begin preferences.
type BeginOptions struct {
	ReadOnly       bool
	IsolationLevel IsolationLevel
	Timeout        time.Duration
	Label          string
}

// NestedOptions defines nested transaction begin preferences.
type NestedOptions struct {
	Label string
}

// Registration registers an adapter/client pair, optionally scoped to a
// tenant.
type Registration struct {
	AdapterName string
	ClientName  string
	TenantID    string
	Adapter     Adapter
	Client      any
	Default     bool
	Tags        map[string]string
}

// ResolutionRequest describes a binding lookup request.
type ResolutionRequest struct {
	Mode        ResolutionMode
	AdapterName Selector
	ClientName  Selector
	TenantID    Selector
}

// BindingOverride is the context-carried override surface for binding
// selection.
type BindingOverride struct {
	AdapterName Selector
	ClientName  Selector
	TenantID    Selector
}

// ExecutionConfig controls managed ambient execution.
//
// The zero value leaves binding selection unspecified, inherits the Manager's
// ambient transaction mode, and requests default backend transaction options.
type ExecutionConfig struct {
	AdapterName    Selector
	ClientName     Selector
	TenantID       Selector
	Transactional  TransactionalMode
	ReadOnly       bool
	IsolationLevel IsolationLevel
	Timeout        time.Duration
	Label          string
}

// Validate validates an ambient execution configuration.
func (c ExecutionConfig) Validate() error {
	switch c.Transactional {
	case TransactionalInherit, TransactionalOff, TransactionalOn:
	default:
		return classifyError(ErrKindConfig, fmt.Errorf("uow: invalid transactional mode %d", c.Transactional))
	}
	return validateSharedTxOptions(c.IsolationLevel, c.Timeout)
}

// TxConfig controls explicit root transaction execution.
//
// The zero value selects the default binding and starts a read-write root
// transaction with default backend options.
type TxConfig struct {
	AdapterName    Selector
	ClientName     Selector
	TenantID       Selector
	ReadOnly       bool
	IsolationLevel IsolationLevel
	Timeout        time.Duration
	Label          string
}

// Validate validates an explicit root transaction configuration.
func (c TxConfig) Validate() error {
	return validateSharedTxOptions(c.IsolationLevel, c.Timeout)
}

// FinalizeInput is passed to a root finalization policy.
type FinalizeInput struct {
	Err              error
	PanicValue       any
	ContextCancelled bool
	UOW              UnitOfWork
}

// TxMeta describes a transaction lifecycle event.
type TxMeta struct {
	TxID          string
	TraceID       string
	SpanID        string
	AdapterName   string
	ClientName    string
	TenantID      string
	Depth         int
	Label         string
	RollbackCause error
}

// Tx is the adapter-neutral transaction handle.
type Tx interface {
	Name() string
	Depth() int
	Raw() any
}

// Adapter abstracts a transactional backend.
type Adapter interface {
	Name() string
	Capabilities() Capabilities

	Begin(ctx context.Context, client any, opts BeginOptions) (Tx, error)
	BeginNested(ctx context.Context, parent Tx, opts NestedOptions) (Tx, error)

	Commit(ctx context.Context, tx Tx) error
	Rollback(ctx context.Context, tx Tx) error

	Unwrap(tx Tx) any
}

// Hooks observes transaction lifecycle events after the corresponding
// interceptor phase.
type Hooks interface {
	OnBegin(ctx context.Context, meta TxMeta)
	OnCommit(ctx context.Context, meta TxMeta, err error)
	OnRollback(ctx context.Context, meta TxMeta, err error)
	OnNestedBegin(ctx context.Context, meta TxMeta)
}

// Interceptor participates in transaction lifecycle phases.
type Interceptor interface {
	BeforeBegin(ctx context.Context, meta TxMeta) error
	AfterBegin(ctx context.Context, meta TxMeta, err error)
	BeforeCommit(ctx context.Context, meta TxMeta) error
	AfterCommit(ctx context.Context, meta TxMeta, err error)
	BeforeRollback(ctx context.Context, meta TxMeta) error
	AfterRollback(ctx context.Context, meta TxMeta, err error)
}

// FinalizePolicy decides whether a managed root transaction should rollback.
//
// Rollback-only and context cancellation remain hard rollback conditions even
// when a custom policy is configured.
type FinalizePolicy interface {
	ShouldRollback(ctx context.Context, input FinalizeInput) bool
}

// TenantResolutionPolicy resolves a tenant identity from context.
type TenantResolutionPolicy interface {
	ResolveTenant(ctx context.Context) (string, error)
}

// Resolver resolves public binding metadata.
type Resolver interface {
	ResolveInfo(ctx context.Context, req ResolutionRequest) (BindingInfo, error)
}

// BindingResolver resolves owner-facing backend bindings.
//
// Repository and service code should continue to consume UnitOfWork instead of
// using ResolvedBinding directly.
type BindingResolver interface {
	ResolveBinding(ctx context.Context, req ResolutionRequest) (ResolvedBinding, error)
}

// UnitOfWork is the application-facing execution-scoped transaction facade.
//
// Implementations are safe for concurrent use within one execution, but remain
// execution-scoped values and should not be reused after their owning request
// or job has completed.
type UnitOfWork interface {
	Binding() BindingInfo

	InTransaction() bool
	Root() (Tx, bool)
	Current() (Tx, bool)
	CurrentHandle() any

	BeginNested(ctx context.Context, opts NestedOptions) (TxScope, error)

	SetRollbackOnly(reason error) error
	IsRollbackOnly() bool
	RollbackReason() error
}

// RootController finalizes the active root transaction for owner-managed
// execution.
type RootController interface {
	CommitRoot(ctx context.Context) error
	RollbackRoot(ctx context.Context) error
}

// TxScope is the lexical nested transaction scope handle.
type TxScope interface {
	Tx() Tx
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

// Executor executes explicit transactional callbacks.
type Executor interface {
	InTx(ctx context.Context, cfg TxConfig, fn func(ctx context.Context) error) error
	InNestedTx(ctx context.Context, opts NestedOptions, fn func(ctx context.Context) error) error
}

func validateSharedTxOptions(isolation IsolationLevel, timeout time.Duration) error {
	if err := validateIsolationLevel(isolation); err != nil {
		return err
	}
	if timeout < 0 {
		return classifyError(ErrKindConfig, fmt.Errorf("uow: timeout must be >= 0"))
	}
	return nil
}

func validateIsolationLevel(level IsolationLevel) error {
	switch level {
	case "":
		return nil
	case IsolationReadUncommitted, IsolationReadCommitted, IsolationRepeatableRead, IsolationSnapshot, IsolationSerializable:
		return nil
	default:
		return classifyError(ErrKindConfig, fmt.Errorf("uow: invalid isolation level %q", level))
	}
}
