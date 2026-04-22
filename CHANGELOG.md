# Changelog

## v1.1.0

Ergonomics release focused on clearer execution APIs and easier config
construction.

Highlights:

- added domain-specific selector helpers for adapter, client, and tenant
  selection
- improved execution config validation coverage in the core and framework
  integrations
- added additive `Exec(...)` and `RootTx(...)` builders plus
  `TxConfigFromExecution(...)`
- renamed ambient managed execution from `Do(...)` to `Run(...)` and added
  `Attach(...)` as a zero-config bind shorthand
- updated examples and docs to reflect the new preferred API surface

## v1.0.0

Initial production release.

Highlights:

- framework-agnostic Unit of Work and transaction manager core
- first-party `database/sql` and GORM adapters
- first-party `net/http` and Fiber v2 integration packages
- deterministic binding resolution with tenant-aware selection
- strict or emulated nested transaction semantics
- rollback-only state, hooks, interceptors, and managed ambient execution
- runnable end-to-end examples for common integration patterns
