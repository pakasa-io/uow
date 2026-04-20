package sqladapter

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"testing"

	"github.com/pakasa-io/uow"
)

type driverState struct {
	mu          sync.Mutex
	beginOpts   []driver.TxOptions
	beginErr    error
	commitErr   error
	rollbackErr error
	commits     int
	rollbacks   int
}

type fakeConnector struct {
	state *driverState
}

func (c fakeConnector) Connect(context.Context) (driver.Conn, error) {
	return &fakeConn{state: c.state}, nil
}

func (c fakeConnector) Driver() driver.Driver {
	return fakeDriver(c)
}

type fakeDriver struct {
	state *driverState
}

func (d fakeDriver) Open(name string) (driver.Conn, error) {
	return &fakeConn{state: d.state}, nil
}

type fakeConn struct {
	state *driverState
}

func (c *fakeConn) Prepare(query string) (driver.Stmt, error) {
	return fakeStmt{}, nil
}

func (c *fakeConn) Close() error { return nil }

func (c *fakeConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *fakeConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	if c.state.beginErr != nil {
		return nil, c.state.beginErr
	}
	c.state.beginOpts = append(c.state.beginOpts, opts)
	return &fakeTx{state: c.state}, nil
}

type fakeStmt struct{}

func (fakeStmt) Close() error  { return nil }
func (fakeStmt) NumInput() int { return -1 }
func (fakeStmt) Exec(args []driver.Value) (driver.Result, error) {
	return nil, errors.New("not implemented")
}
func (fakeStmt) Query(args []driver.Value) (driver.Rows, error) {
	return nil, errors.New("not implemented")
}

type fakeTx struct {
	state *driverState
}

func (t *fakeTx) Commit() error {
	t.state.mu.Lock()
	defer t.state.mu.Unlock()
	t.state.commits++
	return t.state.commitErr
}

func (t *fakeTx) Rollback() error {
	t.state.mu.Lock()
	defer t.state.mu.Unlock()
	t.state.rollbacks++
	return t.state.rollbackErr
}

func openTestDB(t *testing.T, state *driverState) *sql.DB {
	t.Helper()
	db := sql.OpenDB(fakeConnector{state: state})
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestAdapterBeginCommitAndRollback(t *testing.T) {
	state := &driverState{}
	db := openTestDB(t, state)
	adapter := New("")

	tx, err := adapter.Begin(context.Background(), db, uow.BeginOptions{
		ReadOnly:       true,
		IsolationLevel: uow.IsolationSerializable,
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	sqlTx, ok := adapter.Unwrap(tx).(*sql.Tx)
	if !ok || sqlTx == nil {
		t.Fatalf("expected *sql.Tx unwrap, got %T", adapter.Unwrap(tx))
	}
	if tx.Name() == "" || tx.Depth() != 0 {
		t.Fatalf("unexpected tx metadata: name=%q depth=%d", tx.Name(), tx.Depth())
	}

	if err := adapter.Commit(context.Background(), tx); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.commits != 1 {
		t.Fatalf("expected 1 commit, got %d", state.commits)
	}
	if len(state.beginOpts) != 1 {
		t.Fatalf("expected 1 begin options entry, got %d", len(state.beginOpts))
	}
	if !state.beginOpts[0].ReadOnly || state.beginOpts[0].Isolation != driver.IsolationLevel(sql.LevelSerializable) {
		t.Fatalf("unexpected begin options: %+v", state.beginOpts[0])
	}
}

func TestAdapterRollback(t *testing.T) {
	state := &driverState{}
	db := openTestDB(t, state)
	adapter := New("")

	tx, err := adapter.Begin(context.Background(), db, uow.BeginOptions{})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := adapter.Rollback(context.Background(), tx); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.rollbacks != 1 {
		t.Fatalf("expected 1 rollback, got %d", state.rollbacks)
	}
}

func TestAdapterRejectsWrongClientType(t *testing.T) {
	adapter := New("")
	_, err := adapter.Begin(context.Background(), "not-a-db", uow.BeginOptions{})
	if err == nil {
		t.Fatalf("expected type error")
	}
}

func TestAdapterRejectsUnsupportedIsolationLevel(t *testing.T) {
	state := &driverState{}
	db := openTestDB(t, state)
	adapter := New("")

	_, err := adapter.Begin(context.Background(), db, uow.BeginOptions{
		IsolationLevel: uow.IsolationLevel("custom"),
	})
	if err == nil {
		t.Fatalf("expected isolation error")
	}
}

func TestBeginNestedUnsupported(t *testing.T) {
	adapter := New("")
	_, err := adapter.BeginNested(context.Background(), &Tx{}, uow.NestedOptions{})
	if !errors.Is(err, uow.ErrNestedTxUnsupported) {
		t.Fatalf("expected nested unsupported, got %v", err)
	}
}

func TestCurrentHelpers(t *testing.T) {
	state := &driverState{}
	db := openTestDB(t, state)
	adapter := New("")

	registry := uow.NewRegistry()
	registry.MustRegister(uow.Registration{
		Adapter:    adapter,
		Client:     db,
		ClientName: "primary",
		Default:    true,
	})
	manager, err := uow.NewManager(registry, uow.DefaultConfig(), uow.ManagerOptions{})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	ctx, bound, err := manager.Bind(context.Background(), uow.ExecutionConfig{})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	handle, err := Current(bound)
	if err != nil {
		t.Fatalf("Current non-tx: %v", err)
	}
	if _, ok := handle.(*sql.DB); !ok {
		t.Fatalf("expected *sql.DB, got %T", handle)
	}
	if _, ok := CurrentTx(bound); ok {
		t.Fatalf("expected no sql tx")
	}

	err = manager.InTx(ctx, uow.TxConfig{}, func(ctx context.Context) error {
		work := uow.MustFrom(ctx)
		handle, err := Current(work)
		if err != nil {
			t.Fatalf("Current tx: %v", err)
		}
		if _, ok := handle.(*sql.Tx); !ok {
			t.Fatalf("expected *sql.Tx, got %T", handle)
		}
		if tx, ok := CurrentTx(work); !ok || tx == nil {
			t.Fatalf("expected active sql tx")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("InTx: %v", err)
	}
}
