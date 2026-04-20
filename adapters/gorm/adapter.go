package gormadapter

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/pakasa-io/uow"
	"gorm.io/gorm"
)

const (
	// DefaultName is the default adapter registration name.
	DefaultName = "gorm"
)

type txKind int

const (
	txKindRoot txKind = iota
	txKindSavepoint
)

// Option configures Adapter behavior.
type Option func(*Adapter)

// WithNestedSavepoints enables savepoint-backed nested transactions.
//
// This should be enabled only when the selected GORM dialect and database
// support savepoints reliably in production.
func WithNestedSavepoints(enabled bool) Option {
	return func(adapter *Adapter) {
		adapter.nestedSavepoints = enabled
	}
}

// Adapter implements uow.Adapter for GORM.
//
// The zero value is usable and registers as "gorm".
type Adapter struct {
	name             string
	nestedSavepoints bool
	seq              atomic.Uint64
}

// New returns a GORM adapter. Empty names default to "gorm".
func New(name string, options ...Option) *Adapter {
	adapter := &Adapter{name: strings.TrimSpace(name)}
	for _, option := range options {
		if option != nil {
			option(adapter)
		}
	}
	return adapter
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
		NestedTransaction: a != nil && a.nestedSavepoints,
		Savepoints:        a != nil && a.nestedSavepoints,
		ReadOnlyTx:        true,
		IsolationLevels:   true,
		Timeouts:          false,
		MultiTenantAware:  false,
	}
}

// Begin implements uow.Adapter.
func (a *Adapter) Begin(ctx context.Context, client any, opts uow.BeginOptions) (uow.Tx, error) {
	db, ok := client.(*gorm.DB)
	if !ok {
		return nil, fmt.Errorf("gormadapter: client must be *gorm.DB, got %T", client)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	isolation, err := isolationLevel(opts.IsolationLevel)
	if err != nil {
		return nil, err
	}
	tx := db.WithContext(ctx).Begin(&sql.TxOptions{
		ReadOnly:  opts.ReadOnly,
		Isolation: isolation,
	})
	if tx.Error != nil {
		return nil, fmt.Errorf("gormadapter: begin tx: %w", tx.Error)
	}

	return &Tx{
		db:    tx,
		name:  a.nextName(opts.Label),
		depth: 0,
		kind:  txKindRoot,
	}, nil
}

// BeginNested implements uow.Adapter.
func (a *Adapter) BeginNested(ctx context.Context, parent uow.Tx, opts uow.NestedOptions) (uow.Tx, error) {
	if a == nil || !a.nestedSavepoints {
		return nil, fmt.Errorf("%w: gorm nested savepoints are disabled", uow.ErrNestedTxUnsupported)
	}

	parentTx, err := unwrapTx(parent)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	savepoint := a.nextSavepoint(opts.Label, parentTx.depth+1)
	db := parentTx.db.WithContext(ctx)
	if err := db.SavePoint(savepoint).Error; err != nil {
		return nil, fmt.Errorf("%w: gorm savepoint %q: %v", uow.ErrNestedTxUnsupported, savepoint, err)
	}

	return &Tx{
		db:    db,
		name:  savepoint,
		depth: parentTx.depth + 1,
		kind:  txKindSavepoint,
	}, nil
}

// Commit implements uow.Adapter.
func (a *Adapter) Commit(ctx context.Context, tx uow.Tx) error {
	gormTx, err := unwrapTx(tx)
	if err != nil {
		return err
	}
	if gormTx.kind == txKindSavepoint {
		return nil
	}
	if err := gormTx.db.Commit().Error; err != nil {
		return fmt.Errorf("gormadapter: commit tx %q: %w", gormTx.name, err)
	}
	return nil
}

// Rollback implements uow.Adapter.
func (a *Adapter) Rollback(ctx context.Context, tx uow.Tx) error {
	gormTx, err := unwrapTx(tx)
	if err != nil {
		return err
	}
	if gormTx.kind == txKindSavepoint {
		if err := gormTx.db.RollbackTo(gormTx.name).Error; err != nil {
			return fmt.Errorf("gormadapter: rollback to savepoint %q: %w", gormTx.name, err)
		}
		return nil
	}
	if err := gormTx.db.Rollback().Error; err != nil {
		return fmt.Errorf("gormadapter: rollback tx %q: %w", gormTx.name, err)
	}
	return nil
}

// Unwrap implements uow.Adapter.
func (a *Adapter) Unwrap(tx uow.Tx) any {
	gormTx, err := unwrapTx(tx)
	if err != nil {
		return nil
	}
	return gormTx.db
}

// Current returns the current GORM handle from a UnitOfWork.
func Current(work uow.UnitOfWork) (*gorm.DB, error) {
	if work == nil {
		return nil, fmt.Errorf("gormadapter: nil UnitOfWork")
	}
	db, ok := work.CurrentHandle().(*gorm.DB)
	if !ok {
		return nil, fmt.Errorf("gormadapter: current handle is %T, want *gorm.DB", work.CurrentHandle())
	}
	return db, nil
}

// MustCurrent returns the current GORM handle or panics.
func MustCurrent(work uow.UnitOfWork) *gorm.DB {
	db, err := Current(work)
	if err != nil {
		panic(err)
	}
	return db
}

// CurrentTx returns the active transactional GORM handle.
func CurrentTx(work uow.UnitOfWork) (*gorm.DB, bool) {
	if work == nil || !work.InTransaction() {
		return nil, false
	}
	db, ok := work.CurrentHandle().(*gorm.DB)
	return db, ok
}

func (a *Adapter) nextName(label string) string {
	suffix := a.seq.Add(1)
	base := "gorm-tx"
	if name := strings.TrimSpace(label); name != "" {
		base = name
	}
	return fmt.Sprintf("%s-%d", base, suffix)
}

func (a *Adapter) nextSavepoint(label string, depth int) string {
	base := "gorm_sp"
	if name := strings.TrimSpace(label); name != "" {
		base = "gorm_sp_" + sanitizeName(name)
	}
	return fmt.Sprintf("%s_%d_%d", base, depth, a.seq.Add(1))
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
		return sql.LevelDefault, fmt.Errorf("gormadapter: unsupported isolation level %q", level)
	}
}

func sanitizeName(value string) string {
	replacer := strings.NewReplacer(" ", "_", "-", "_", ".", "_", "/", "_", ":", "_")
	return replacer.Replace(strings.TrimSpace(value))
}

func unwrapTx(tx uow.Tx) (*Tx, error) {
	gormTx, ok := tx.(*Tx)
	if !ok {
		return nil, fmt.Errorf("gormadapter: tx must be *Tx, got %T", tx)
	}
	return gormTx, nil
}
