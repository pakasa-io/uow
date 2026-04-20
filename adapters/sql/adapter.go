package sqladapter

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/pakasa-io/uow"
)

const (
	// DefaultName is the default adapter registration name.
	DefaultName = "database/sql"
)

// Handle is the common query surface implemented by both *sql.DB and *sql.Tx.
type Handle interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Adapter implements uow.Adapter for database/sql.
//
// The zero value is usable and registers as "database/sql".
type Adapter struct {
	name string
	seq  atomic.Uint64
}

// New returns a database/sql adapter. Empty names default to "database/sql".
func New(name string) *Adapter {
	return &Adapter{name: strings.TrimSpace(name)}
}

// Name implements uow.Adapter.
func (a *Adapter) Name() string {
	if a == nil || strings.TrimSpace(a.name) == "" {
		return DefaultName
	}
	return a.name
}

// Capabilities implements uow.Adapter.
func (a *Adapter) Capabilities() uow.Capabilities {
	return uow.Capabilities{
		RootTransaction:   true,
		NestedTransaction: false,
		Savepoints:        false,
		ReadOnlyTx:        true,
		IsolationLevels:   true,
		Timeouts:          false,
		MultiTenantAware:  false,
	}
}

// Begin implements uow.Adapter.
func (a *Adapter) Begin(ctx context.Context, client any, opts uow.BeginOptions) (uow.Tx, error) {
	db, ok := client.(*sql.DB)
	if !ok {
		return nil, fmt.Errorf("sqladapter: client must be *sql.DB, got %T", client)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	isolation, err := isolationLevel(opts.IsolationLevel)
	if err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{
		Isolation: isolation,
		ReadOnly:  opts.ReadOnly,
	})
	if err != nil {
		return nil, fmt.Errorf("sqladapter: begin tx: %w", err)
	}

	return &Tx{
		tx:    tx,
		name:  a.nextName(opts.Label),
		depth: 0,
	}, nil
}

// BeginNested implements uow.Adapter.
func (a *Adapter) BeginNested(ctx context.Context, parent uow.Tx, opts uow.NestedOptions) (uow.Tx, error) {
	return nil, fmt.Errorf("%w: database/sql does not expose nested transactions or savepoints in the standard library", uow.ErrNestedTxUnsupported)
}

// Commit implements uow.Adapter.
func (a *Adapter) Commit(ctx context.Context, tx uow.Tx) error {
	sqlTx, err := unwrapTx(tx)
	if err != nil {
		return err
	}
	if err := sqlTx.tx.Commit(); err != nil {
		return fmt.Errorf("sqladapter: commit tx %q: %w", sqlTx.name, err)
	}
	return nil
}

// Rollback implements uow.Adapter.
func (a *Adapter) Rollback(ctx context.Context, tx uow.Tx) error {
	sqlTx, err := unwrapTx(tx)
	if err != nil {
		return err
	}
	if err := sqlTx.tx.Rollback(); err != nil {
		return fmt.Errorf("sqladapter: rollback tx %q: %w", sqlTx.name, err)
	}
	return nil
}

// Unwrap implements uow.Adapter.
func (a *Adapter) Unwrap(tx uow.Tx) any {
	sqlTx, err := unwrapTx(tx)
	if err != nil {
		return nil
	}
	return sqlTx.tx
}

// Current returns the current database/sql handle from a UnitOfWork.
//
// The returned value is *sql.Tx inside a transaction and *sql.DB otherwise.
func Current(work uow.UnitOfWork) (Handle, error) {
	if work == nil {
		return nil, fmt.Errorf("sqladapter: nil UnitOfWork")
	}
	switch handle := work.CurrentHandle().(type) {
	case *sql.DB:
		return handle, nil
	case *sql.Tx:
		return handle, nil
	default:
		return nil, fmt.Errorf("sqladapter: current handle is %T, want *sql.DB or *sql.Tx", handle)
	}
}

// MustCurrent returns the current handle or panics.
func MustCurrent(work uow.UnitOfWork) Handle {
	handle, err := Current(work)
	if err != nil {
		panic(err)
	}
	return handle
}

// CurrentTx returns the active *sql.Tx when the UnitOfWork is transactional.
func CurrentTx(work uow.UnitOfWork) (*sql.Tx, bool) {
	if work == nil {
		return nil, false
	}
	tx, ok := work.CurrentHandle().(*sql.Tx)
	return tx, ok
}

func (a *Adapter) nextName(label string) string {
	suffix := a.seq.Add(1)
	base := "sql-tx"
	if name := strings.TrimSpace(label); name != "" {
		base = name
	}
	return fmt.Sprintf("%s-%d", base, suffix)
}

func isolationLevel(level uow.IsolationLevel) (sql.IsolationLevel, error) {
	switch level {
	case "":
		return sql.LevelDefault, nil
	case uow.IsolationReadUncommitted:
		return sql.LevelReadUncommitted, nil
	case uow.IsolationReadCommitted:
		return sql.LevelReadCommitted, nil
	case uow.IsolationRepeatableRead:
		return sql.LevelRepeatableRead, nil
	case uow.IsolationSnapshot:
		return sql.LevelSnapshot, nil
	case uow.IsolationSerializable:
		return sql.LevelSerializable, nil
	default:
		return sql.LevelDefault, fmt.Errorf("sqladapter: unsupported isolation level %q", level)
	}
}

func unwrapTx(tx uow.Tx) (*Tx, error) {
	sqlTx, ok := tx.(*Tx)
	if !ok {
		return nil, fmt.Errorf("sqladapter: tx must be *Tx, got %T", tx)
	}
	return sqlTx, nil
}
