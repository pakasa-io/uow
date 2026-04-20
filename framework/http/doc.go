// Package httpuow provides net/http integration for github.com/pakasa-io/uow.
//
// The package keeps transport-specific policy, such as status-based rollback
// decisions, out of the core uow package while using the same ambient execution
// path as other owners.
package httpuow
