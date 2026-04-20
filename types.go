package uow

import (
	"context"
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
// Set=false means the field is unspecified.
// Set=true with Value="" means "explicitly use the default/non-tenant choice".
type Selector struct {
	Set   bool
	Value string
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

// TxConfig controls explicit root transaction execution.
type TxConfig struct {
	AdapterName    Selector
	ClientName     Selector
	TenantID       Selector
	ReadOnly       bool
	IsolationLevel IsolationLevel
	Timeout        time.Duration
	Label          string
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
