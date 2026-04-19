## UNIT OF WORK & TRANSACTION MANAGER

### Framework-Agnostic, Extensible, Multi-Tenant-Capable

### Version: v1.1 (Final Hardened)

---

# 1. PURPOSE

Define a **framework-agnostic, extensible, multi-tenant-aware Unit of Work (UOW) and Transaction Manager** that:

* manages **root and nested transaction lifecycles**
* supports **multiple adapters** and **multiple concrete clients**
* supports **tenant-aware client resolution**
* integrates with HTTP frameworks through **optional adapters only**
* provides **strict, configurable transaction semantics**
* supports both **automatic** and **explicit** transaction activation modes
* ensures **observability, correctness, safety, and minimal ambiguity**
* is suitable for use in:

    * HTTP request flows
    * background jobs
    * CLI commands
    * event consumers
    * schedulers
    * worker processes

---

# 2. DESIGN PRINCIPLES

## 2.1 Core Principles

* **Framework-agnostic core**
* **Context-first propagation**
* **At most one root transaction per execution context**
* **Explicit and deterministic transaction semantics**
* **Strict ownership rules**
* **Extensibility as a primary priority**
* **Multi-tenancy as a first-class concern**
* **No hidden downgrade behavior unless explicitly configured**
* **Immutable binding for the lifetime of a UnitOfWork**
* **Deterministic, testable behavior**

## 2.2 Architectural Priorities

Priority order:

1. **Correctness**
2. **Extensibility**
3. **Clarity of semantics**
4. **Framework independence**
5. **Operational observability**
6. **Convenience ergonomics**

---

# 3. HIGH-LEVEL ARCHITECTURE

The system is composed of the following layers:

1. **Registry**

    * stores adapters, client registrations, tenant-aware registrations, defaults
2. **Resolver**

    * resolves the effective binding for an execution context
3. **Binding**

    * public binding metadata plus internal resolved backend binding
4. **Transaction Manager**

    * starts, nests, commits, rolls back, and finalizes transactions
5. **UnitOfWork**

    * execution-scoped façade exposed to consumers
6. **Executor**

    * convenience API for root and nested transactional execution
7. **Framework Adapter**

    * optional integration layer for Fiber or other frameworks
8. **Observability / Hooks / Interceptors**

    * pluggable instrumentation and policy surfaces

---

# 4. CORE CONCEPTS

## 4.1 UnitOfWork

A **single execution-scoped binding and transaction coordinator** that:

* owns exactly one immutable **binding** for the execution
* may own one **root transaction** over that binding
* tracks the **current active handle**, transactional or non-transactional
* manages **nested transaction scopes** when a root transaction exists
* tracks **rollback-only state** when transactional
* exposes resolved **binding metadata**
* exposes the **current adapter-specific handle**
* enforces state-machine and lifecycle rules

## 4.2 Adapter

An abstraction over a transactional backend.

Examples include:

* GORM
* `database/sql`
* SQLx
* bun
* ent
* mock/in-memory adapter
* future non-SQL transactional systems

## 4.3 Client

A concrete adapter-specific backend instance.

Examples:

* primary relational DB
* analytics DB
* tenant-sharded DB
* tenant pooled DB
* read replica
* test client

## 4.4 Binding

A binding has two representations:

* `BindingInfo`: public metadata only
* `ResolvedBinding`: owner-facing resolved backend binding

`BindingInfo` contains only:

* adapter name
* client name
* tenant context

`ResolvedBinding` contains:

* `BindingInfo`
* adapter
* client

A `ResolvedBinding` is resolved before the root transaction begins and remains immutable for the lifetime of the UnitOfWork.

## 4.5 Resolver

A component responsible for determining binding metadata or the final owner-facing `ResolvedBinding` for an execution context.

It may consider:

* global configuration
* route/job/command execution config
* tenant identity
* explicit `BindingOverride` values from context
* resolver policy
* registry defaults

## 4.6 Tenant

A logical tenant identity or tenant routing scope used to resolve the correct client for execution.

Examples:

* `tenant_acme`
* `tenant_123`
* `region_us_east_tenant_42`

Tenant awareness is built into the resolution model and is not an afterthought.

---

# 5. CONFIGURATION MODEL

## 5.1 Global Configuration

```go
type Config struct {
	NestedMode               NestedMode
	TransactionMode          TransactionMode
	DefaultAdapterName       string
	DefaultClientName        string
	DefaultFinalizePolicy    FinalizePolicy
	StrictOptionEnforcement  bool
	AllowOptionDowngrade     bool
	RequireTenantResolution  bool
}
```

## 5.2 Configuration Sources

Configuration may be supplied through:

* application startup code
* environment variables
* configuration file
* dependency injection container

Application startup configuration and environment variables MUST map to the same effective model.

## 5.3 Effective Configuration Precedence

When multiple config sources exist, precedence for binding selection is:

For ambient execution driven by `ExecutionConfig`:

1. **Selector fields with `Set = true` from `BindingOverride`**, for adapter/client/tenant selection only
2. **ResolutionRequest derived from `ExecutionConfig`**
3. **Application startup configuration**
4. **Environment-derived defaults**
5. **Built-in defaults**

For explicit transactional execution driven by `TxConfig`:

1. **Selector fields with `Set = true` from `TxConfig`**
2. **`BindingOverride` from context**, but only for selector fields with `Set = false` in `TxConfig`
3. **Application startup configuration**
4. **Environment-derived defaults**
5. **Built-in defaults**

If explicit `TxConfig` and `BindingOverride` provide conflicting selector fields with `Set = true` and different `Value`, resolution MUST return `ErrBindingOverrideConflict` or equivalent.

This precedence model MUST be applied consistently.

Root begin options such as `ReadOnly`, `IsolationLevel`, `Timeout`, and `Label` are not affected by `BindingOverride`.

---

# 6. TRANSACTION ACTIVATION MODES

## 6.1 TransactionMode

```go
type TransactionMode int

const (
	ExplicitOnly TransactionMode = iota
	GlobalAuto
)
```

## 6.2 ExplicitOnly (DEFAULT)

Transactions are created only when explicitly requested.

Examples:

* calling `Executor.InTx(...)`
* framework adapter explicitly marked transactional
* explicit execution wrapper in CLI/job flow

In this mode:

* no transaction is created automatically
* for managed execution, an owner MUST create or inject a non-transactional `UnitOfWork` bound to the resolved client for the execution context when no root transaction is started
* if `Executor.InTx(...)` is invoked and no root transaction exists, a root transaction MUST be created
* if a root transaction already exists, `Executor.InTx(...)` MUST behave as nested execution

## 6.3 GlobalAuto

Transactions are automatically created for all eligible execution contexts unless explicitly opted out.

In this mode:

* framework adapters or execution wrappers SHOULD start a root transaction by default
* execution may opt out by setting `ExecutionConfig.Transactional = TransactionalOff`
* when execution opts out, managed execution owners MUST create or inject a non-transactional `UnitOfWork` bound to the resolved client for the execution context

---

# 7. NESTED TRANSACTION MODES

## 7.1 NestedMode

```go
type NestedMode int

const (
	NestedStrict NestedMode = iota
	NestedEmulated
)
```

## 7.2 NestedStrict (DEFAULT)

Nested transactions require true nested transaction support or savepoint support.

Rules:

* adapter MUST support nested/savepoint behavior
* if unsupported, `BeginNested` MUST return `ErrNestedTxUnsupported`
* nested rollback MUST rollback to the nested scope boundary
* nested commit MUST be logical only and MUST NOT commit the root

## 7.3 NestedEmulated

Nested transactions are emulated without savepoints.

Rules:

* nested begin MUST succeed even without savepoint support, provided a root transaction exists
* nested rollback MUST set rollback-only on the UnitOfWork
* nested commit MUST be a logical no-op
* final root commit MUST fail or convert to rollback if rollback-only is set

## 7.4 NestedEmulated Rollback-Only Inheritance

When running in `NestedEmulated` mode and the UnitOfWork is already rollback-only:

* `BeginNested` MUST succeed
* the nested scope MUST inherit rollback-only state
* nested `Commit()` MUST be a no-op
* nested `Rollback()` MUST be a no-op
* no additional savepoint or backend nested state is created

---

# 8. ADAPTER CONTRACT

## 8.1 Capabilities

```go
type Capabilities struct {
	RootTransaction   bool
	NestedTransaction bool
	Savepoints        bool
	ReadOnlyTx        bool
	IsolationLevels   bool
	Timeouts          bool
	MultiTenantAware  bool
}
```

## 8.2 Adapter Interface

```go
type Adapter interface {
	Name() string
	Capabilities() Capabilities

	Begin(ctx context.Context, client any, opts BeginOptions) (Tx, error)
	BeginNested(ctx context.Context, parent Tx, opts NestedOptions) (Tx, error)

	Commit(ctx context.Context, tx Tx) error
	Rollback(ctx context.Context, tx Tx) error

	Unwrap(tx Tx) any
}
```

## 8.3 Adapter Option and Capability Enforcement

If a requested option is unsupported by the adapter:

* when `StrictOptionEnforcement = true`, the adapter or manager MUST return an error
* when `StrictOptionEnforcement = false` and `AllowOptionDowngrade = true`, the unsupported option MAY be ignored
* downgrade behavior MUST be observable through hooks/logging
* silent downgrade without policy allowance is forbidden

If transactional execution is requested and the selected adapter reports `Capabilities().RootTransaction == false`:

* manager/executor MUST return `ErrRootTxUnsupported` or equivalent before invoking user callback code
* manager/executor MUST NOT silently downgrade the execution to a non-transactional `UnitOfWork`

If nested execution is requested:

* `NestedStrict` MUST enforce true nested capability according to section 7 and return `ErrNestedTxUnsupported` when required nested support is unavailable
* `NestedEmulated` MAY proceed without backend nested support because rollback semantics are defined at the `UnitOfWork` level

---

# 9. TRANSACTION OPTIONS

## 9.1 Root Begin Options

```go
type BeginOptions struct {
	ReadOnly       bool
	IsolationLevel IsolationLevel
	Timeout        time.Duration
	Label          string
}
```

## 9.2 Nested Begin Options

```go
type NestedOptions struct {
	Label string
}
```

## 9.3 Enforcement Rules

* unsupported `ReadOnly` requests MUST follow strict/downgrade policy
* unsupported `IsolationLevel` requests MUST follow strict/downgrade policy
* unsupported `Timeout` requests MUST follow strict/downgrade policy
* label is informational and MUST NOT affect correctness

---

# 10. TRANSACTION INTERFACE

```go
type Tx interface {
	Name() string
	Depth() int
	Raw() any
}
```

Rules:

* `Name()` identifies the backend transaction or savepoint scope
* `Depth()` is `0` for root transaction
* `Raw()` returns the backend-specific representation
* callers MUST NOT assume the type of `Raw()` without adapter validation

---

# 11. BINDING MODEL

## 11.1 Public BindingInfo Structure

```go
type BindingInfo struct {
	AdapterName string
	ClientName  string
	TenantID    string
}
```

## 11.2 Internal ResolvedBinding Structure

```go
type ResolvedBinding struct {
	BindingInfo
	Adapter     Adapter
	Client      any
}
```

## 11.3 Binding Rules

* `UnitOfWork.Binding()` MUST return `BindingInfo` only
* `ResolvedBinding` is resolved before root transaction creation
* `ResolvedBinding` is immutable for the entire lifetime of the UnitOfWork
* adapter switching mid-UOW is forbidden
* client switching mid-UOW is forbidden
* tenant switching mid-UOW is forbidden
* application code MUST access backend operations through `CurrentHandle()`, not through a root client binding
* `ResolvedBinding` is an owner/internal surface and MUST NOT be used as the application-facing repository/service access path

Any attempt to mutate binding after UOW creation MUST return an error.

---

# 12. REGISTRY

## 12.1 Purpose

Registry stores:

* adapter registrations
* client registrations
* tenant-specific registrations
* defaults
* optional metadata for resolution

## 12.2 Registration Model

```go
type Registration struct {
	AdapterName string
	ClientName  string
	TenantID    string
	Adapter     Adapter
	Client      any
	Default     bool
	Tags        map[string]string
}
```

## 12.3 Tenant Registration Semantics

A registration may be:

* global non-tenant-specific
* tenant-specific
* tenant-pattern-specific, if supported by resolver policy

Tenant-specific registrations take precedence over non-tenant-specific defaults when tenant resolution is enabled.

## 12.4 Thread Safety

Registry MUST be thread-safe for concurrent reads.

Registry MAY support writes after application startup, but production deployments SHOULD treat registrations as effectively immutable after initialization.

If dynamic registration is allowed, the concurrency and consistency model MUST be explicitly documented by the implementation.

---

# 13. MULTI-TENANCY

## 13.1 First-Class Requirement

Multi-tenancy is a first-class concern of the system.

The design MUST support:

* per-tenant client resolution
* shared adapter with multiple tenant clients
* default client fallback when policy allows
* strict no-fallback mode when tenant resolution is mandatory

## 13.2 Tenant Identity Source

Tenant identity may be provided by:

* explicit context value
* framework route metadata
* request headers resolved by application code
* JWT claims resolved by upstream middleware
* job payload metadata
* CLI flags or command context

The core system MUST NOT depend on any specific transport-specific tenant source.

## 13.3 Tenant Resolution Policy

```go
type TenantResolutionPolicy interface {
	ResolveTenant(ctx context.Context) (string, error)
}
```

## 13.4 Tenant Requirement Rules

If `RequireTenantResolution = true`:

* absence of tenant identity when tenant resolution is required MUST return an error
* fallback to non-tenant default is forbidden unless explicitly configured by resolver policy

If `RequireTenantResolution = false`:

* tenant resolution MAY fall back to:

    * explicit client selection
    * global default client
    * non-tenant registration

## 13.5 Tenant Binding Invariance

Once a Binding is resolved for tenant `X`, all root and nested transaction behavior MUST remain bound to tenant `X`.

Cross-tenant transaction switching within a single UnitOfWork is forbidden.

## 13.6 Multi-Tenant Safety Rule

A single UnitOfWork MUST NOT span multiple tenants.

Any attempt to open or resolve a different tenant binding while a root transaction is active MUST return an error.

---

# 14. RESOLVER

## 14.1 Purpose

Resolver is responsible for producing binding resolution results for an execution context.

The public `Resolver` surface returns metadata only.

The owner-only `BindingResolver` surface returns the resolved backend binding required by executor and framework integration code.

## 14.2 Selector

```go
type Selector struct {
	Set   bool
	Value string
}
```

Selector semantics:

* `Set = false` means no explicit selection for this field
* `Set = true` with non-empty `Value` means explicit selection of that value
* `Set = true` with empty `Value` means explicit selection of the default or empty binding for that field
* for `TenantID`, `Set = true` with empty `Value` means explicitly resolve against non-tenant binding behavior when policy allows

## 14.3 ResolutionMode

```go
type ResolutionMode int

const (
	ResolutionAmbient ResolutionMode = iota
	ResolutionExplicit
)
```

`ResolutionAmbient` is used for framework adapters and other ambient execution flows derived from `ExecutionConfig`.

`ResolutionExplicit` is used for explicit transactional execution flows derived from `TxConfig`.

## 14.4 ResolutionRequest

```go
type ResolutionRequest struct {
	Mode        ResolutionMode
	AdapterName Selector
	ClientName  Selector
	TenantID    Selector
}
```

## 14.5 Public Resolver Interface

```go
type Resolver interface {
	ResolveInfo(ctx context.Context, req ResolutionRequest) (BindingInfo, error)
}
```

## 14.6 Owner-Only BindingResolver Interface

```go
type BindingResolver interface {
	ResolveBinding(ctx context.Context, req ResolutionRequest) (ResolvedBinding, error)
}
```

`BindingResolver` is an owner/internal capability and MUST NOT be the application-facing repository/service access path.

## 14.7 Resolution Inputs

Resolver may use:

* explicit `ResolutionRequest`
* `BindingOverride` from context
* configured adapter/client defaults from the effective `Config`
* tenant identity
* registry registrations
* global config
* startup defaults

## 14.8 Required Resolution Order

Resolver MUST apply precedence according to `ResolutionRequest.Mode`.

For `ResolutionAmbient`, resolver precedence is:

1. selector fields with `Set = true` from `BindingOverride` context
2. selector fields with `Set = true` from `ResolutionRequest` derived from `ExecutionConfig`
3. configured adapter/client defaults derived from the effective `Config`, but only for selector fields still unset after steps 1-2
4. tenant-aware registration lookup
5. global default registration
6. built-in default fallback, if any

For `ResolutionExplicit`, resolver precedence is field-wise:

1. selector fields with `Set = true` from `ResolutionRequest` derived from `TxConfig`
2. `BindingOverride` from context MAY fill only selector fields with `Set = false` in the explicit request
3. configured adapter/client defaults derived from the effective `Config`, but only for selector fields still unset after steps 1-2
4. tenant-aware registration lookup
5. global default registration
6. built-in default fallback, if any

If explicit transactional resolution encounters conflicting selector fields where both `ResolutionRequest` and `BindingOverride` have `Set = true` and different `Value`, resolver MUST return `ErrBindingOverrideConflict` or equivalent.

## 14.9 Resolver Output Rules

`Resolver` MUST either:

* return one fully resolved `BindingInfo`
* or return an error

`BindingResolver` MUST either:

* return one fully resolved `ResolvedBinding`
* or return an error

Neither resolver interface may return a partially resolved binding.

---

# 15. EXECUTION CONFIG

## 15.1 TransactionalMode

```go
type TransactionalMode int

const (
	TransactionalInherit TransactionalMode = iota
	TransactionalOff
	TransactionalOn
)
```

`TransactionalInherit` is the zero value and means "follow the effective execution-mode default."

## 15.2 ExecutionConfig Structure

```go
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
```

## 15.3 ExecutionConfig Meaning

* `AdapterName`: requested adapter override selector
* `ClientName`: requested client override selector
* `TenantID`: explicit tenant override selector
* `Transactional`: whether this execution context inherits, disables, or explicitly requests transactional execution
* `ReadOnly`: root transaction read-only preference
* `IsolationLevel`: root transaction isolation preference
* `Timeout`: root transaction timeout preference
* `Label`: execution label for observability

`ExecutionConfig` is used for ambient execution wrappers, framework adapters, and auto-transaction evaluation.

In managed non-transactional execution, it also determines the binding for the required non-transactional `UnitOfWork`.

## 15.4 Effective Behavior

In `ExplicitOnly` mode:

* `Transactional = TransactionalOn` requests a root transaction
* `Transactional = TransactionalInherit` means no automatic transaction unless an owner explicitly chooses transactional execution
* `Transactional = TransactionalOff` means no automatic transaction
* when no automatic transaction is started in managed execution, the owner MUST create a non-transactional `UnitOfWork`

In `GlobalAuto` mode:

* if `Transactional = TransactionalInherit` or `TransactionalOn`, transaction SHOULD start automatically
* if `Transactional = TransactionalOff`, transaction MUST NOT start automatically
* if no transaction is started in managed execution, the owner MUST create a non-transactional `UnitOfWork`

When an ambient owner starts a root transaction from `ExecutionConfig`:

* it MUST derive `BeginOptions` from `ExecutionConfig.ReadOnly`, `ExecutionConfig.IsolationLevel`, `ExecutionConfig.Timeout`, and `ExecutionConfig.Label`
* it MUST apply the same adapter capability enforcement and downgrade policy defined for root begin options elsewhere in this specification
* if no transaction is started, those fields MUST NOT affect the non-transactional `UnitOfWork` beyond observability metadata attached by the owner

## 15.5 Explicit TxConfig

```go
type TxConfig struct {
	AdapterName    Selector
	ClientName     Selector
	TenantID       Selector
	ReadOnly       bool
	IsolationLevel IsolationLevel
	Timeout        time.Duration
	Label          string
}
```

## 15.6 TxConfig Rule

`TxConfig` is used by explicit transactional APIs such as `Executor.InTx(...)`.

`TxConfig` intentionally does not include `Transactional`, because invoking an explicit transactional API is itself a request for transactional execution.

## 15.7 ResolutionRequest Mapping

For binding resolution:

* requests derived from `ExecutionConfig` MUST set `ResolutionRequest.Mode = ResolutionAmbient`
* requests derived from `TxConfig` MUST set `ResolutionRequest.Mode = ResolutionExplicit`
* `ExecutionConfig.AdapterName`, `ExecutionConfig.ClientName`, and `ExecutionConfig.TenantID` map directly to `ResolutionRequest`
* `TxConfig.AdapterName`, `TxConfig.ClientName`, and `TxConfig.TenantID` map directly to `ResolutionRequest`
* `Transactional`, `ReadOnly`, `IsolationLevel`, `Timeout`, and `Label` are not themselves binding selection inputs
* when derived from `TxConfig`, selector fields with `Set = true` are authoritative
* `BindingOverride` MAY fill only selector fields with `Set = false` in explicit `TxConfig`
* conflicting selector fields where explicit `TxConfig` and `BindingOverride` both have `Set = true` and different `Value` MUST return `ErrBindingOverrideConflict` or equivalent

---

# 16. UNIT OF WORK CONTRACT

```go
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
```

## 16.1 Owner-Only Root Control

```go
type RootController interface {
	CommitRoot(ctx context.Context) error
	RollbackRoot(ctx context.Context) error
}
```

`RootController` is an owner-only capability intended for executor, framework adapter, job runner, or command runner code.

Application services and repositories MUST receive `UnitOfWork`, not `RootController`.

The presence of `UnitOfWork` does not itself imply an active transaction.

## 16.2 Rules

* `InTransaction()` reports whether a root transaction is currently active
* `Root()` returns the root transaction and `true` when transactional, otherwise `(nil, false)` or the implementation equivalent
* `Current()` returns the active current transaction or scope and `true` when transactional, otherwise `(nil, false)` or the implementation equivalent
* `CurrentHandle()` returns the adapter-specific current backend handle
* `Binding()` returns metadata only and MUST NOT expose backend clients or adapters
* `CurrentHandle()` MUST return the active transactional handle when `InTransaction() == true`
* `CurrentHandle()` MUST return the bound client/backend handle when `InTransaction() == false`
* `BeginNested(...)` MUST return `ErrNoActiveTransaction` or equivalent when no root transaction is active
* `SetRollbackOnly(...)` MUST return `ErrNoActiveTransaction` or equivalent when no root transaction is active
* `IsRollbackOnly()` MUST return `false` when no root transaction is active
* `RollbackReason()` MUST return `nil` when no root transaction is active
* consumers MUST NOT cache `CurrentHandle()` across nested scope boundaries
* repositories/services MUST acquire current handle at call time
* the public `UnitOfWork` contract MUST NOT itself grant root finalization privileges

---

# 17. TX SCOPE

```go
type TxScope interface {
	Tx() Tx
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}
```

## 17.1 Lexical Lifetime Rule

A `TxScope` MUST NOT outlive its execution scope.

It MUST NOT be:

* stored for later asynchronous use
* transferred across goroutines without explicit implementation guarantees
* finalized after the parent UnitOfWork is finalized

## 17.2 Stack Discipline

Nested scopes MUST be strictly LIFO.

Invalid operations:

* committing parent while child is active
* rolling back parent while child is active, except through full root failure handling
* finalizing outer scope before inner scope is closed

## 17.3 Manual Nested Finalization Rules

Manual `TxScope` finalization MUST follow the same nested-scope ordering rules defined in section 24.

For `TxScope.Commit(...)`:

* if `BeforeCommit` vetoes commit or the commit attempt fails, the implementation MUST transition immediately to the rollback path for that nested scope
* if the compensating rollback succeeds, `Commit(...)` MUST return `ErrCommitAborted` or equivalent wrapping the commit/veto cause
* if the compensating rollback also fails, `Commit(...)` MUST return `ErrFinalizationFailed` or equivalent preserving both the commit/veto cause and the rollback failure

For `TxScope.Rollback(...)`:

* `BeforeRollback` errors MUST NOT veto rollback
* rollback MUST still be attempted even when `BeforeRollback` returns an error
* if rollback succeeds and `BeforeRollback` also returned an error, `Rollback(...)` MUST return that preserved pre-rollback failure or equivalent while the scope is still considered rolled back
* if rollback attempt fails and that is the only rollback-phase failure, `Rollback(...)` MUST return `ErrRollbackFailed` or equivalent
* if multiple rollback-phase failures must be preserved together, `Rollback(...)` MUST return `ErrFinalizationFailed` or equivalent preserving all failures

Manual nested rollback MUST follow the active nested mode semantics; in `NestedEmulated`, rollback means marking the root rollback-only and closing the logical nested scope.

---

# 18. TRANSACTION STATE MACHINE

## 18.1 States

```text
NEW → ACTIVE → (COMMITTED | ROLLED_BACK | FAILED)
```

This state machine applies only when `InTransaction() == true`.

A non-transactional `UnitOfWork` has no root transaction state and exposes only its bound non-transactional current handle.

## 18.2 Root Rules

* root begins at `NEW`
* after successful begin → `ACTIVE`
* after successful commit → `COMMITTED`
* after successful rollback → `ROLLED_BACK`
* on irrecoverable finalization failure → `FAILED`

## 18.3 Nested Rules

Each nested scope tracks its own state independently, but parent-child invariants apply.

## 18.4 Invariants

* no terminal state may transition further
* parent MUST NOT reach a terminal state while any nested child is still active
* double commit/rollback MUST be idempotent or return a well-defined terminal-state error
* finalization after closure MUST NOT mutate backend state twice

---

# 19. ROLLBACK-ONLY MECHANISM

## 19.1 Contract

```go
SetRollbackOnly(reason error) error
IsRollbackOnly() bool
RollbackReason() error
```

## 19.2 Semantics

Rollback-only means:

* root transaction MUST NOT successfully commit
* finalization MUST rollback instead of commit
* if caller attempts explicit commit while rollback-only is set, commit MUST fail with `ErrRollbackOnly` or equivalent
* if no root transaction is active, `SetRollbackOnly(...)` MUST return `ErrNoActiveTransaction` or equivalent
* if no root transaction is active, `IsRollbackOnly()` MUST be `false`
* if no root transaction is active, `RollbackReason()` MUST be `nil`

## 19.3 Use Cases

* business validation failed but error was handled locally
* context cancellation detected
* nested emulated rollback occurred
* policy interceptor marked execution as unsafe to commit

---

# 20. CONTEXT PROPAGATION

## 20.1 Context API

```go
func With(ctx context.Context, uow UnitOfWork) context.Context
func From(ctx context.Context) (UnitOfWork, bool)
func MustFrom(ctx context.Context) UnitOfWork
```

`UnitOfWork` MUST be propagated in both transactional and non-transactional managed execution flows.

Managed execution means any flow where a framework adapter, execution wrapper, job runner, or command runner applies this specification's binding-resolution and propagation model.

`MustFrom(...)` requires that a `UnitOfWork` is present, but it MUST NOT imply that `InTransaction() == true`.

In managed execution flows, owners MUST ensure that `MustFrom(...)` is valid before invoking repository or service logic that depends on UOW propagation.

Any managed callback entered by an owner MUST receive propagated UOW context.

When managed execution enters user callback code, the owner MUST invoke that callback with a context containing the active `UnitOfWork` via `With(...)` or an equivalent propagation mechanism that preserves the same contract.

## 20.2 Binding Override API

```go
type BindingOverride struct {
	AdapterName Selector
	ClientName  Selector
	TenantID    Selector
}

func WithBindingOverride(ctx context.Context, override BindingOverride) context.Context
func BindingOverrideFrom(ctx context.Context) (BindingOverride, bool)
```

`BindingOverride` applies only to binding selection fields.

`BindingOverride` uses the same `Selector` semantics as `ResolutionRequest`.

It MUST NOT be used to override:

* `Transactional`
* `ReadOnly`
* `IsolationLevel`
* `Timeout`
* `Label`

## 20.3 Primary Rule

`context.Context` is the primary propagation mechanism.

Framework adapters MUST bridge their framework context to `context.Context`, not replace the core propagation model.

Only `WithBindingOverride(...)` and `BindingOverrideFrom(...)` are the sanctioned context override mechanism for binding selection.

Implementations MUST NOT rely on undocumented or transport-specific context keys for adapter/client/tenant override behavior.

For explicit transactional execution, `BindingOverride` MUST NOT silently override conflicting selector values provided by `TxConfig` when both sides have `Set = true`.

## 20.4 Cancellation Behavior

If the execution context is cancelled:

* if `InTransaction() == true`, `UnitOfWork` MUST transition to rollback-only
* if `InTransaction() == true`, root finalization MUST rollback
* if `InTransaction() == true`, new nested scopes SHOULD fail or inherit rollback-only according to active nested mode
* if `InTransaction() == false`, no rollback-only transition is required because no root transaction exists
* observability hooks MUST record the cancellation-triggered transactional rollback path when a root transaction exists

---

# 21. EXECUTOR API

## 21.1 Contract

```go
type Executor interface {
	InTx(ctx context.Context, cfg TxConfig, fn func(ctx context.Context) error) error
	InNestedTx(ctx context.Context, opts NestedOptions, fn func(ctx context.Context) error) error
}
```

## 21.2 Re-Entrancy Rules

If `InTx(...)` is called and no root exists:

* resolve the binding derived from `TxConfig` selection fields before creating or promoting the `UnitOfWork`
* if the selected adapter reports `RootTransaction == false`, return `ErrRootTxUnsupported` or equivalent and MUST NOT create a root transaction or invoke `fn`
* if an existing `UnitOfWork` is already bound to a conflicting resolved binding, return `ErrBindingImmutable`, `ErrMultipleRootBindingsForbidden`, or an equivalent error before creating, promoting, or beginning any root transaction
* if no `UnitOfWork` exists, create one using the resolved binding
* if a non-transactional `UnitOfWork` already exists, begin the root transaction on that same `UnitOfWork`
* create the root transaction using the resolved binding and the root begin options derived from `TxConfig`
* if root begin is vetoed by `BeforeBegin`, executor MUST NOT invoke `fn` and MUST return `ErrBeginAborted` or equivalent wrapping the veto cause
* if root begin attempt fails, executor MUST NOT invoke `fn` and MUST return `ErrBeginFailed` or equivalent wrapping the begin failure
* invoke `fn` with a context containing the active `UnitOfWork`

If `InTx(...)` is called and a root already exists:

* behave as nested execution
* any `TxConfig` field that would change the active binding or root begin options MUST return an error
* nested begin MUST succeed before `fn` may be invoked
* if nested begin is vetoed by `BeforeBegin`, executor MUST NOT invoke `fn` and MUST return `ErrBeginAborted` or equivalent wrapping the veto cause
* if nested begin attempt fails, executor MUST NOT invoke `fn` and MUST return `ErrBeginFailed` or equivalent wrapping the begin failure
* invoke `fn` with a context containing the active `UnitOfWork`
* this invocation MUST NOT finalize the existing root directly

If `InNestedTx(...)` is called and no root exists:

* return `ErrNoActiveTransaction` or equivalent unless implementation explicitly documents auto-root creation, which is discouraged by default

If `InNestedTx(...)` is called and a root exists:

* create the nested scope according to the active nested mode
* if nested begin is vetoed by `BeforeBegin`, executor MUST NOT invoke `fn` and MUST return `ErrBeginAborted` or equivalent wrapping the veto cause
* if nested begin attempt fails, executor MUST NOT invoke `fn` and MUST return `ErrBeginFailed` or equivalent wrapping the begin failure
* invoke `fn` with a context containing the active `UnitOfWork`

If multiple begin-phase failures must be preserved for either root or nested begin:

* executor MUST return `ErrBeginFailed` or equivalent preserving all begin-phase failures
* when a `BeforeBegin` veto/error is present, that veto cause MUST remain the primary cause in the preserved error chain or equivalent structure

## 21.3 Managed Finalization Semantics

`Executor.InTx(...)` and `Executor.InNestedTx(...)` are managed callback APIs.

The executor MUST finalize the scope it created or entered before returning to the caller or re-panicking.

For executor-managed finalization, `BeforeCommit` errors are commit vetoes that MUST redirect execution to the rollback path, and `BeforeRollback` errors MUST NOT veto rollback.

An `Executor.InTx(...)` invocation that observed no root on entry owns root finalization for that root transaction.

An `Executor.InTx(...)` invocation that observed an existing root on entry MUST use nested execution semantics for finalization and MUST NOT apply root finalization policy to the pre-existing root.

For `Executor.InTx(...)` when the current invocation created the root transaction:

* executor MUST capture callback return error, panic value, and context cancellation into `FinalizeInput`
* executor MUST apply section 22 to the active root transaction before returning or re-panicking
* if section 22 selects commit, executor MUST attempt root commit
* if section 22 selects rollback, executor MUST attempt root rollback
* if the commit path is selected and `BeforeCommit` or the commit attempt itself fails, executor MUST transition immediately to the rollback path for that same root transaction
* if the rollback path is selected and `BeforeRollback` returns an error, executor MUST still attempt rollback and MUST preserve both failures through wrapping or equivalent error composition when possible
* if `fn` panics, executor MUST preserve the original panic value, finalize the root transaction first, then re-panic the original value
* if callback execution and root finalization both fail without panic, the returned error MUST preserve both failures through wrapping or equivalent error composition
* on non-panic paths, returned error selection MUST follow section 21.4

The following nested finalization rules apply to both `Executor.InNestedTx(...)` and re-entrant `Executor.InTx(...)` invocations that entered with an existing root transaction:

* if `fn` returns `nil` and no cancellation or panic is observed, executor MUST commit the nested scope
* if `fn` returns an error, executor MUST rollback the nested scope and return the callback error
* if context cancellation is observed before successful nested commit and `fn` did not already return a different error, executor MUST rollback the nested scope and return `ErrContextCancelled`, `context.Canceled`, or equivalent
* if the nested commit path is selected and `BeforeCommit` or the commit attempt itself fails, executor MUST transition immediately to the nested rollback path
* if the nested rollback path is selected and `BeforeRollback` returns an error, executor MUST still attempt rollback and MUST preserve both failures through wrapping or equivalent error composition when possible
* if `fn` panics, executor MUST rollback the nested scope first and then re-panic the original value
* if callback execution and nested finalization both fail without panic, the returned error MUST preserve both failures through wrapping or equivalent error composition
* nested rollback MUST follow the active nested mode semantics; in `NestedEmulated`, rollback means marking the root rollback-only and closing the logical nested scope
* on non-panic paths, returned error selection MUST follow section 21.4

## 21.4 Returned Error Precedence

The following rules govern non-panic return values from `Executor.InTx(...)` and `Executor.InNestedTx(...)` after begin succeeded and `fn` was invoked.

If `fn` returns a non-nil error:

* that callback error MUST be the primary returned error
* if finalization also fails, the returned error MUST preserve both failures through wrapping or equivalent error composition, with the callback error remaining primary

If `fn` returns `nil` and the selected commit path completes successfully:

* executor MUST return `nil`

If `fn` returns `nil` and the selected commit path is vetoed by `BeforeCommit` or fails during commit attempt:

* executor MUST NOT return `nil`
* if compensating rollback succeeds, executor MUST return `ErrCommitAborted` or equivalent wrapping the commit/veto cause
* if compensating rollback also fails, executor MUST return `ErrFinalizationFailed` or equivalent preserving both the commit/veto cause and the rollback failure

If `fn` returns `nil` and rollback is selected by policy before any commit attempt:

* if rollback succeeds because context cancellation selected the rollback path, executor MUST return `ErrContextCancelled`, `context.Canceled`, or equivalent and MUST NOT return `nil`
* if rollback succeeds because rollback-only selected the rollback path, executor MUST return `RollbackReason()` when non-nil, otherwise `ErrRollbackOnly` or equivalent
* if rollback succeeds because some other execution error selected the rollback path, executor MUST return the policy-driving error and MUST NOT return `nil`

If `fn` returns `nil` and the selected rollback path does not complete successfully:

* executor MUST return `ErrRollbackFailed` or equivalent when rollback failure is the only finalization failure
* executor MUST return `ErrFinalizationFailed` or equivalent when multiple finalization failures must be preserved together
* the returned error MUST preserve the policy-driving cause, if any, in addition to rollback failure details

The panic contract remains unchanged:

* when `fn` panics, executor MUST re-panic the original panic value after finalization attempts
* any finalization failure observed during a panic path MUST be preserved through hooks, logging, panic reporting integration, or equivalent observability mechanisms

## 21.5 Explicit API Rule

`Executor.InTx(...)` is an explicit transactional API.

Calling `Executor.InTx(...)` is itself a request for transactional execution.

For binding selection, selector fields with `Set = true` in `TxConfig` are authoritative over `BindingOverride`.

`Executor.InTx(...)` MUST resolve binding using a `ResolutionRequest` with `Mode = ResolutionExplicit`.

Ambient owners that need inherit/on/off semantics MUST evaluate `ExecutionConfig.Transactional` before deciding whether to invoke `InTx(...)`.

Once `InTx(...)` is invoked, the executor MUST attempt transactional execution.

If a managed non-transactional `UnitOfWork` already exists, `Executor.InTx(...)` SHOULD promote that same execution-scoped `UnitOfWork` into transactional mode instead of replacing it.

Both `Executor.InTx(...)` and `Executor.InNestedTx(...)` MUST pass a context to `fn` for which `MustFrom(ctx)` returns the active execution-scoped `UnitOfWork`.

---

# 22. FINALIZATION POLICY

## 22.1 Input

```go
type FinalizeInput struct {
	Err              error
	PanicValue       any
	ContextCancelled bool
	UOW              UnitOfWork
}
```

## 22.2 Interface

```go
type FinalizePolicy interface {
	ShouldRollback(ctx context.Context, input FinalizeInput) bool
}
```

## 22.3 Default Evaluation Order

Owner-managed root callback APIs MUST use the effective root finalization policy.

If `Config.DefaultFinalizePolicy` is non-nil, that configured policy MUST be used.

If `Config.DefaultFinalizePolicy` is nil, the built-in default policy MUST be used.

The built-in default policy evaluates finalization decisions in this order:

1. panic detected
2. returned execution error
3. rollback-only flag
4. context cancellation
5. otherwise commit

First matching rollback condition wins.

These rules apply only when a root transaction exists.

This root finalization model MUST be used by owner-managed root callback APIs, including `Executor.InTx(...)` and framework-managed root execution wrappers.

## 22.4 Transport-Specific Pre-Finalization Rule

Transport-specific adapters MAY evaluate transport results before invoking core finalization.

If a transport-specific rule requires rollback, the adapter SHOULD express that decision by setting rollback-only or by returning an execution error before delegating to the core finalization policy.

## 22.5 Framework-Neutrality Rule

The core finalization model MUST NOT depend on HTTP status codes.

Status-based rollback logic is a framework adapter concern layered on top of the core model.

---

# 23. FRAMEWORK INTEGRATION

## 23.1 General Rule

Framework integrations are optional adapters on top of the core UOW system.

The core package MUST NOT depend on Fiber or any other framework.

## 23.2 Framework Adapter Responsibilities

A framework adapter MAY:

* extract execution config from route metadata
* resolve tenant identity from transport context through application hooks
* resolve final binding
* create and inject an execution-scoped `UnitOfWork`
* create root transaction when transactional execution is selected
* evaluate transport-specific success/failure signals before core finalization
* finalize transaction on completion

When a framework adapter manages execution according to this specification, it MUST create and inject a `UnitOfWork` into `context.Context` for the full managed execution, even when no root transaction is started.

When a framework adapter starts a root transaction from ambient `ExecutionConfig`, it MUST derive root `BeginOptions` from `ExecutionConfig.ReadOnly`, `ExecutionConfig.IsolationLevel`, `ExecutionConfig.Timeout`, and `ExecutionConfig.Label`.

## 23.3 Fiber Adapter

A Fiber integration MAY provide middleware, but:

* Fiber MUST NOT be required by the core package
* Fiber-specific rollback on status codes MUST live in the Fiber adapter package
* any status-based rollback configuration MUST live in Fiber adapter config, not in core `Config` or `ExecutionConfig`
* Fiber adapter MUST use the same core resolver, executor, and finalization semantics

---

# 24. OBSERVABILITY

## 24.1 Hooks

```go
type Hooks interface {
	OnBegin(ctx context.Context, meta TxMeta)
	OnCommit(ctx context.Context, meta TxMeta, err error)
	OnRollback(ctx context.Context, meta TxMeta, err error)
	OnNestedBegin(ctx context.Context, meta TxMeta)
}
```

## 24.2 Interceptors

```go
type Interceptor interface {
	BeforeBegin(ctx context.Context, meta TxMeta) error
	AfterBegin(ctx context.Context, meta TxMeta, err error)
	BeforeCommit(ctx context.Context, meta TxMeta) error
	AfterCommit(ctx context.Context, meta TxMeta, err error)
	BeforeRollback(ctx context.Context, meta TxMeta) error
	AfterRollback(ctx context.Context, meta TxMeta, err error)
}
```

## 24.3 Ordering Rules

The required begin ordering for root transactions is:

1. `BeforeBegin`
2. adapter `Begin(...)` attempt
3. `AfterBegin`
4. `OnBegin` on successful begin only

The required begin ordering for nested scopes is:

1. `BeforeBegin`
2. nested begin attempt
3. `AfterBegin`
4. `OnNestedBegin` on successful nested begin only

The required commit ordering for both root and nested scopes is:

1. `BeforeCommit`
2. commit attempt
3. `AfterCommit`
4. `OnCommit`

The required rollback ordering for both root and nested scopes is:

1. `BeforeRollback`
2. rollback attempt
3. `AfterRollback`
4. `OnRollback`

Additional ordering rules:

* `AfterBegin`, `AfterCommit`, and `AfterRollback` MUST run whether the corresponding operation succeeds or fails, and MUST receive the operation error if any
* `OnCommit` and `OnRollback` MUST run after the corresponding `After*` callback and MUST receive the same operation error value
* nested scope commit and rollback observability MUST use `OnCommit` and `OnRollback`; `TxMeta.Depth` distinguishes nested from root events
* if `BeforeBegin` returns an error, begin attempt MUST NOT occur, `AfterBegin` MUST still run with that error, neither `OnBegin` nor `OnNestedBegin` may run, and owner-managed execution MUST NOT invoke user callback code
* if `BeforeCommit` returns an error, commit attempt MUST NOT occur, `AfterCommit` MUST still run with that error, `OnCommit` MUST still run after `AfterCommit`, and owner-managed finalization MUST continue on the rollback path
* if `BeforeRollback` returns an error, rollback attempt MUST still occur, `AfterRollback` MUST receive the rollback attempt error when one exists, and `OnRollback` MUST run after `AfterRollback` with the same effective rollback error value
* if root finalization policy selects rollback, rollback ordering applies and commit ordering MUST NOT fire
* if a commit attempt fails and implementation then attempts rollback as recovery, the rollback sequence MUST run as a distinct lifecycle after the failed commit sequence
* during any rollback lifecycle, `TxMeta.RollbackCause` MUST identify the semantic reason the rollback path was selected, even when the rollback attempt itself succeeds
* `AfterRollback` and `OnRollback` MUST observe the same `TxMeta.RollbackCause` value for a given rollback lifecycle

## 24.4 Metadata

```go
type TxMeta struct {
	TxID        string
	TraceID     string
	SpanID      string
	AdapterName string
	ClientName  string
	TenantID    string
	Depth       int
	Label       string
	RollbackCause error
}
```

## 24.5 Identity Requirements

* `TxID` MUST be unique per root transaction
* nested scopes SHOULD carry the same root `TxID` with depth-specific metadata
* tracing fields MAY be empty if tracing is not configured

---

# 25. ERROR MODEL

## 25.1 Canonical Errors

```go
var (
	ErrNoAdapterRegistered
	ErrAdapterNotFound
	ErrClientNotFound
	ErrTenantNotResolved
	ErrTenantBindingNotFound
	ErrUOWNotFound
	ErrTxAlreadyClosed
	ErrNestedTxUnsupported
	ErrRootTxUnsupported
	ErrBeginAborted
	ErrBeginFailed
	ErrCommitAborted
	ErrRollbackFailed
	ErrFinalizationFailed
	ErrBindingOverrideConflict
	ErrRootOwnershipViolation
	ErrNoActiveTransaction
	ErrRollbackOnly
	ErrBindingImmutable
	ErrMultipleRootBindingsForbidden
	ErrScopeOrderViolation
	ErrContextCancelled
)
```

## 25.2 Error Categorization

```go
type ErrorKind int

const (
	ErrKindConfig ErrorKind = iota
	ErrKindAdapter
	ErrKindResolver
	ErrKindTransaction
	ErrKindState
	ErrKindTenant
)
```

## 25.3 Wrapped Error Type

```go
type UOWError struct {
	Kind ErrorKind
	Err  error
}
```

Programmatic consumers SHOULD be able to inspect both:

* category
* underlying cause

---

# 26. MULTI-BINDING AND MULTI-DATABASE POLICY

## 26.1 Single Root Binding Rule

A single UnitOfWork may have exactly one immutable Binding and at most one root transaction over that binding.

## 26.2 Forbidden Behavior

The following are forbidden within a single UnitOfWork:

* switching adapter
* switching client
* switching tenant
* opening multiple root bindings
* spanning multiple databases as one atomic transaction without explicit distributed transaction support

## 26.3 Enforcement

Attempting to open multiple root transactional bindings within the same execution context MUST return `ErrMultipleRootBindingsForbidden` or equivalent.

## 26.4 Distributed Transactions

Distributed transactions / 2PC are out of scope.

Cross-database operations in one execution may still occur outside the UOW atomicity guarantee, but MUST NOT be represented as one atomic UnitOfWork.

---

# 27. ACCESS PATTERNS

## 27.1 Generic

```go
handle := uow.CurrentHandle()
```

Consumers MUST validate the adapter before asserting handle type.

`CurrentHandle()` is valid in both transactional and non-transactional flows.

It returns the active transaction handle when `uow.InTransaction() == true`, otherwise the bound client/backend handle.

Binding metadata remains safe for observability and routing introspection only:

```go
info := uow.Binding()
```

Consumers MUST NOT use binding metadata as a path to bypass the active transaction handle.

## 27.2 Adapter-Specific Helper Example

```go
func MustGorm(uow UnitOfWork) *gorm.DB
```

Adapter-specific helpers are recommended for ergonomics but MUST live outside the generic core contract.

---

# 28. OWNERSHIP RULES

## 28.1 Root Ownership

Only the root owner may finalize the root transaction.

Root finalization MUST occur through `RootController` or an equivalent owner-only internal capability, not through the public `UnitOfWork` interface.

Typically this is:

* framework adapter
* executor root wrapper
* job runner wrapper
* command runner wrapper

## 28.2 Nested Ownership

Nested scopes may be created by service-level logic but:

* MUST NOT commit or rollback root directly
* MUST only finalize their own scope

## 28.3 Repository Rule

Repositories MUST NOT:

* open transactions
* commit transactions
* rollback transactions
* cache current handle across nested scope boundaries

Repositories SHOULD:

* use current handle dynamically at call time
* support both transactional and non-transactional flows through `CurrentHandle()`
* check `InTransaction()` or rely on caller contract when transaction-only semantics are required

In managed execution flows, repository and service code MAY assume a `UnitOfWork` is present regardless of transaction state.

---

# 29. SAFETY GUARANTEES

The implementation MUST guarantee:

* panic-safe rollback path
* single root finalization
* idempotent or explicitly rejected duplicate finalization
* deterministic scope ordering
* binding immutability
* tenant immutability within a UOW
* no silent nested downgrade unless policy allows it
* no silent option downgrade unless policy allows it

---

# 30. DETERMINISM AND TESTABILITY

## 30.1 Deterministic Behavior

The system MUST be deterministic under test conditions, including:

* nested scope ordering
* finalization ordering
* rollback precedence
* hook invocation order
* option downgrade decisions
* tenant resolution precedence

## 30.2 Test Matrix Requirements

Minimum tests MUST cover:

* root begin success/failure
* root begin veto semantics
* callback not invoked on root begin veto/failure
* commit success/failure
* rollback success/failure
* rollback on panic
* rollback on returned error
* rollback-only behavior
* context cancellation behavior
* executor callback UOW propagation
* executor root finalization on success/error/panic
* nested begin veto/failure semantics
* callback not invoked on nested begin veto/failure
* nested callback finalization on success/error/panic
* returned error precedence for callback error vs finalization error
* commit veto/failure followed by successful rollback
* rollback failure return semantics
* strict nested success/failure
* emulated nested behavior
* emulated nested under rollback-only
* explicit nested no-root error
* scope ordering violations
* adapter resolution precedence
* tenant resolution precedence
* tenant-required failure
* root capability enforcement
* multi-binding rejection
* idempotent finalization
* interceptor ordering
* hook metadata correctness

---

# 31. NON-GOALS

The following are explicitly out of scope for this version:

* distributed transactions / two-phase commit
* atomic cross-database transactions
* automatic ORM abstraction across all adapters
* transport-specific tenant resolution in the core package
* implicit background-safe long-lived transaction scopes
* automatic cross-goroutine transaction propagation without explicit implementation support

---

# 32. DEFAULTS

Unless explicitly configured otherwise:

* `NestedMode = NestedStrict`
* `TransactionMode = ExplicitOnly`
* `StrictOptionEnforcement = true`
* `AllowOptionDowngrade = false`
* `RequireTenantResolution = false`
* one root binding per UnitOfWork
* one tenant per UnitOfWork
* `InTx(...)` re-enters as nested if root exists

---

# 33. RECOMMENDED PACKAGE LAYOUT

```text
/uow
  config.go
  errors.go
  types.go
  registry.go
  resolver.go
  binding.go
  uow.go
  executor.go
  state.go
  hooks.go
  interceptor.go
  context.go
  tenant.go

/uow/adapters/gorm
  adapter.go
  helper.go

/uow/framework/fiber
  middleware.go
  config.go
  tenant_resolver.go
  finalize_policy.go
```

This layout is illustrative only. The core requirement is separation between:

* core framework-agnostic package
* adapter packages
* framework integration packages

---

# 34. SUMMARY

This specification defines a **framework-agnostic, extensible, multi-tenant-first Unit of Work and Transaction Manager** with:

* immutable binding resolution
* single-root transaction semantics
* configurable strict or emulated nested transactions
* configurable explicit or auto transaction activation
* resolver-based binding determination
* tenant-aware client selection
* context-first propagation
* strong safety and ownership guarantees
* framework adapters as optional integration layers only
* comprehensive observability, interceptor, and error surfaces

The resulting design is intended to be stable, minimal in ambiguity, and suitable for production implementation across diverse execution environments.
