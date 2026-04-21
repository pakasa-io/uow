// Package httpuow provides net/http integration for github.com/pakasa-io/uow.
//
// The package keeps transport-specific policy, such as status-based rollback
// decisions, out of the core uow package while using the same ambient execution
// path as other owners.
//
// Transactional handlers are response-buffered until finalization so commit or
// rollback failures can still affect the HTTP result. Streaming, hijacking, and
// similar long-lived response patterns should opt out of managed transactions.
package httpuow
