// Package sqladapter provides a first-party uow adapter for database/sql.
//
// The adapter keeps the core uow package dependency-free while giving users a
// concrete path for stdlib SQL integrations. Root transactions use *sql.DB as
// the registered client and expose *sql.Tx as the transactional current handle.
package sqladapter
