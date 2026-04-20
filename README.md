# uow

`uow` is a framework-agnostic Unit of Work and transaction manager for Go.

It provides:

- immutable execution-scoped binding resolution
- explicit and ambient transaction execution
- strict or emulated nested transactions
- tenant-aware client selection
- rollback-only semantics
- interceptor and hook-based observability

The package is designed for service code that should depend on a small,
stable `UnitOfWork` contract while leaving concrete adapter behavior at the
edge of the application.

## Why This Exists

Transactional code often drifts into framework-specific middleware, adapter
types leaking into application services, or inconsistent nested semantics.
`uow` centralizes those rules in a transport-neutral package:

- owners resolve one binding per execution
- repositories fetch the current backend handle through `CurrentHandle()`
- explicit and ambient execution use the same resolver and transaction model
- tenant and override precedence remain deterministic under test

## Installation

```bash
go get github.com/pakasa-io/uow
```

## Quick Start

The module ships first-party adapters for:

- `database/sql` in `github.com/pakasa-io/uow/adapters/sql`
- GORM in `github.com/pakasa-io/uow/adapters/gorm`

```go
package main

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pakasa-io/uow"
	sqladapter "github.com/pakasa-io/uow/adapters/sql"
)

func main() {
	db, err := sql.Open("driver-name", "dsn")
	if err != nil {
		panic(err)
	}
	defer db.Close()

	registry := uow.NewRegistry()
	registry.MustRegister(uow.Registration{
		Adapter:    sqladapter.New("sql"),
		Client:     db,
		ClientName: "primary",
		Default:    true,
	})

	manager, err := uow.NewManager(registry, uow.DefaultConfig(), uow.ManagerOptions{})
	if err != nil {
		panic(err)
	}

	err = manager.InTx(context.Background(), uow.TxConfig{}, func(ctx context.Context) error {
		current := sqladapter.MustCurrent(uow.MustFrom(ctx))
		fmt.Printf("%T\n", current)
		return nil
	})
	if err != nil {
		panic(err)
	}
}
```

## Core Concepts

### `Manager`

`Manager` is the entry point for binding resolution and managed execution.

Use:

- `ResolveInfo` or `ResolveBinding` for owner-side lookup
- `Bind` to create a non-transactional execution-scoped `UnitOfWork`
- `Do` for ambient request/job/command execution
- `InTx` and `InNestedTx` for explicit transactional execution

### `UnitOfWork`

Application code should depend on `UnitOfWork`, not adapter-specific clients.

Key rules:

- `Binding()` exposes metadata only
- `CurrentHandle()` returns the live transactional handle when a root exists
- `CurrentHandle()` returns the bound client in non-transactional flows
- repositories should acquire the current handle at call time

### Binding Resolution

Binding resolution is deterministic and mode-aware:

- ambient resolution applies `BindingOverride` before `ExecutionConfig`
- explicit resolution applies `TxConfig` before `BindingOverride`
- tenant-specific registrations win over non-tenant registrations
- tenant fallback is allowed only when tenant resolution is not required

### Nested Transactions

`NestedStrict`:

- requires adapter nested transaction or savepoint support
- returns `ErrNestedTxUnsupported` when unavailable

`NestedEmulated`:

- never requires adapter nested support
- nested rollback marks the root rollback-only
- nested commit is logical only

## Configuration

Start with `uow.DefaultConfig()` and override only the fields your
application needs:

```go
cfg := uow.DefaultConfig()
cfg.TransactionMode = uow.GlobalAuto
cfg.NestedMode = uow.NestedEmulated
cfg.RequireTenantResolution = true
```

You can also load the serializable subset from environment variables:

```go
cfg, err := uow.ConfigFromEnv("UOW")
```

Supported keys:

- `UOW_NESTED_MODE`
- `UOW_TRANSACTION_MODE`
- `UOW_DEFAULT_ADAPTER_NAME`
- `UOW_DEFAULT_CLIENT_NAME`
- `UOW_STRICT_OPTION_ENFORCEMENT`
- `UOW_ALLOW_OPTION_DOWNGRADE`
- `UOW_REQUIRE_TENANT_RESOLUTION`

Custom finalize policies remain code-only because they are Go interfaces.

## Context Propagation

Managed execution should always propagate the `UnitOfWork`:

```go
u := uow.MustFrom(ctx)
handle := u.CurrentHandle()
```

Optional context helpers:

- `WithBindingOverride` / `BindingOverrideFrom`
- `WithTenantID` / `TenantIDFromContext`
- `ContextTenantPolicy`

## First-Party `database/sql` Adapter

The `sqladapter` package expects a registered `*sql.DB` client and exposes:

- `sqladapter.New(name)` for adapter construction
- `sqladapter.Current(uow)` to obtain a `database/sql` query handle
- `sqladapter.MustCurrent(uow)` when repository code prefers panic-on-miswire
- `sqladapter.CurrentTx(uow)` for transaction-specific paths

The adapter supports:

- root transactions
- `ReadOnly` begin options
- standard `database/sql` isolation levels

The adapter intentionally does not advertise:

- nested/savepoint transactions
- backend transaction timeout semantics

That keeps the capability contract aligned with what `database/sql` can
guarantee portably.

## First-Party GORM Adapter

The `gormadapter` package expects a registered `*gorm.DB` client and exposes:

- `gormadapter.New(name, options...)` for adapter construction
- `gormadapter.Current(uow)` to obtain the current `*gorm.DB`
- `gormadapter.MustCurrent(uow)` for panic-on-miswire repository code
- `gormadapter.CurrentTx(uow)` for transaction-only paths

By default the GORM adapter is conservative:

- root transactions are supported
- `ReadOnly` and isolation preferences are passed through `gorm.DB.Begin`
- nested transactions are reported as unsupported

When the backing dialect supports savepoints reliably, nested strict mode can
be enabled explicitly:

```go
adapter := gormadapter.New("gorm", gormadapter.WithNestedSavepoints(true))
```

This keeps the default capability contract stable across databases while still
allowing savepoint-backed nesting for deployments that have validated it.

## Error Model

The package returns wrapped errors that work with `errors.Is` and
`errors.As`.

Typical checks:

```go
if errors.Is(err, uow.ErrRollbackOnly) { ... }
if errors.Is(err, uow.ErrNestedTxUnsupported) { ... }

var uerr *uow.UOWError
if errors.As(err, &uerr) && uerr.Kind == uow.ErrKindResolver { ... }
```

## Thread Safety

- `Registry` supports concurrent reads and serialized writes.
- `UnitOfWork` state transitions are internally synchronized.
- Nested scopes are lexical and must be finalized in LIFO order.
- `TxScope` values are not designed for long-lived asynchronous use.

## Development

```bash
gofmt -w *.go
go test ./...
golangci-lint run
```

GitHub Actions CI runs `go test ./...` on pushes and pull requests. A
repository-local [`.golangci.yml`](.golangci.yml) is included and an optional
manual lint workflow is available without making linting a required publish
gate yet.

## Compatibility Notes

- The public API is intentionally small and concrete.
- The module avoids framework and ORM dependencies in the core package.
- No distributed transaction support is provided.

## Non-Goals

- two-phase commit / distributed transactions
- cross-database atomicity
- framework-specific middleware in the core package
- automatic adapter implementations for every ORM
