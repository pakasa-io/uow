# Examples

This directory contains runnable examples that demonstrate common `uow`
integration patterns without requiring a full application.

Run any example with:

```bash
go run ./examples/<name>
```

Available scenarios:

- `callbacks`: simple synchronous and background callback execution with `uow`
- `http`: `net/http` integration with per-route configuration
- `fiber-selective-routes`: Fiber v2 middleware plus route-specific overrides
- `gorm`: GORM integration with the first-party adapter
- `nested-transactions`: strict nested transactions using GORM savepoints
- `controller-service-repository`: layered application flow where only the
  controller owns managed execution
