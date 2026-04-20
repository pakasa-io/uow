// Package fiberuow provides native Fiber v2 integration for
// github.com/pakasa-io/uow.
//
// The package uses the core Manager ambient execution path and bridges Fiber's
// request lifecycle through c.UserContext()/c.SetUserContext().
package fiberuow
