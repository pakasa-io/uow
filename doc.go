// Package uow provides a framework-agnostic Unit of Work and transaction
// manager for single-binding execution flows.
//
// The package is designed around a small public contract:
//
//   - owners resolve one immutable binding per execution
//   - repositories and services consume UnitOfWork from context
//   - explicit and ambient execution paths share the same resolver and
//     transaction semantics
//   - nested transactions are either strict or emulated, depending on Config
//
// The core package is transport-neutral. HTTP and ORM integrations belong in
// separate packages built on top of Manager, Resolver, and UnitOfWork.
package uow
