package httpuow

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/pakasa-io/uow"
)

type testTx struct {
	name  string
	depth int
	raw   string
}

func (t *testTx) Name() string { return t.name }
func (t *testTx) Depth() int   { return t.depth }
func (t *testTx) Raw() any     { return t.raw }

type testAdapter struct {
	mu            sync.Mutex
	beginCount    int
	commitCount   int
	rollbackCount int
	beginErr      error
	commitErr     error
}

func (a *testAdapter) Name() string { return "test" }

func (a *testAdapter) Capabilities() uow.Capabilities {
	return uow.Capabilities{RootTransaction: true}
}

func (a *testAdapter) Begin(ctx context.Context, client any, opts uow.BeginOptions) (uow.Tx, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.beginErr != nil {
		return nil, a.beginErr
	}
	a.beginCount++
	return &testTx{name: "root", raw: "tx"}, nil
}

func (a *testAdapter) BeginNested(ctx context.Context, parent uow.Tx, opts uow.NestedOptions) (uow.Tx, error) {
	return nil, uow.ErrNestedTxUnsupported
}

func (a *testAdapter) Commit(ctx context.Context, tx uow.Tx) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.commitCount++
	return a.commitErr
}

func (a *testAdapter) Rollback(ctx context.Context, tx uow.Tx) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rollbackCount++
	return nil
}

func (a *testAdapter) Unwrap(tx uow.Tx) any { return tx.Raw() }

func newManager(t *testing.T, adapter *testAdapter) *uow.Manager {
	t.Helper()
	registry := uow.NewRegistry()
	registry.MustRegister(uow.Registration{
		Adapter:    adapter,
		Client:     "client",
		ClientName: "primary",
		Default:    true,
	})
	manager, err := uow.NewManager(registry, uow.DefaultConfig(), uow.ManagerOptions{TenantPolicy: uow.ContextTenantPolicy{}})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return manager
}

func TestWrapInjectsUnitOfWorkAndCommits(t *testing.T) {
	adapter := &testAdapter{}
	manager := newManager(t, adapter)

	handler := Wrap(manager, Config{
		Execution: uow.ExecutionConfig{Transactional: uow.TransactionalOn},
		ResolveTenant: func(r *http.Request) (string, error) {
			return "tenant-acme", nil
		},
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		work := uow.MustFrom(r.Context())
		if !work.InTransaction() {
			t.Fatalf("expected active transaction")
		}
		if work.Binding().TenantID != "tenant-acme" {
			t.Fatalf("unexpected tenant: %+v", work.Binding())
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/items", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if adapter.beginCount != 1 || adapter.commitCount != 1 || adapter.rollbackCount != 0 {
		t.Fatalf("unexpected tx counts: begin=%d commit=%d rollback=%d", adapter.beginCount, adapter.commitCount, adapter.rollbackCount)
	}
}

func TestMiddlewareRollsBackOnConfiguredStatus(t *testing.T) {
	adapter := &testAdapter{}
	manager := newManager(t, adapter)

	middleware := Middleware(manager, Config{
		Execution:        uow.ExecutionConfig{Transactional: uow.TransactionalOn},
		RollbackOnStatus: RollbackOn5xx,
	})

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uow.MustFrom(r.Context())
		w.WriteHeader(http.StatusInternalServerError)
	}))

	req := httptest.NewRequest(http.MethodGet, "/fail", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if adapter.beginCount != 1 || adapter.commitCount != 0 || adapter.rollbackCount != 1 {
		t.Fatalf("unexpected tx counts: begin=%d commit=%d rollback=%d", adapter.beginCount, adapter.commitCount, adapter.rollbackCount)
	}
}

func TestResolveExecutionChoosesTransactionalModePerRequest(t *testing.T) {
	adapter := &testAdapter{}
	manager := newManager(t, adapter)

	handler := Wrap(manager, Config{
		ResolveExecution: func(r *http.Request) (uow.ExecutionConfig, error) {
			if r.Header.Get("X-Tx") == "on" {
				return uow.ExecutionConfig{Transactional: uow.TransactionalOn}, nil
			}
			return uow.ExecutionConfig{Transactional: uow.TransactionalOff}, nil
		},
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if txEnabled := r.Header.Get("X-Tx") == "on"; uow.MustFrom(r.Context()).InTransaction() != txEnabled {
			t.Fatalf("unexpected transaction state for X-Tx=%q", r.Header.Get("X-Tx"))
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/dynamic", nil)
	req.Header.Set("X-Tx", "off")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	req = httptest.NewRequest(http.MethodGet, "/dynamic", nil)
	req.Header.Set("X-Tx", "on")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if adapter.beginCount != 1 || adapter.commitCount != 1 || adapter.rollbackCount != 0 {
		t.Fatalf("unexpected tx counts: begin=%d commit=%d rollback=%d", adapter.beginCount, adapter.commitCount, adapter.rollbackCount)
	}
}

func TestErrorHandlerRunsOnBeginFailure(t *testing.T) {
	adapter := &testAdapter{beginErr: errors.New("begin failed")}
	manager := newManager(t, adapter)
	called := false

	handler := Wrap(manager, Config{
		Execution: uow.ExecutionConfig{Transactional: uow.TransactionalOn},
		ErrorHandler: func(ctx ErrorContext) {
			called = true
			if ctx.Started {
				t.Fatalf("expected unstarted response")
			}
			ctx.ResponseWriter.WriteHeader(http.StatusServiceUnavailable)
		},
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("handler should not run")
	}))

	req := httptest.NewRequest(http.MethodGet, "/begin-fail", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !called {
		t.Fatalf("expected custom error handler")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
}

func TestCommitFailureCanReplaceBufferedSuccessResponse(t *testing.T) {
	adapter := &testAdapter{commitErr: errors.New("commit failed")}
	manager := newManager(t, adapter)

	handler := Wrap(manager, Config{
		Execution: uow.ExecutionConfig{Transactional: uow.TransactionalOn},
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/commit-fail", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if adapter.beginCount != 1 || adapter.commitCount != 1 || adapter.rollbackCount != 1 {
		t.Fatalf("unexpected tx counts: begin=%d commit=%d rollback=%d", adapter.beginCount, adapter.commitCount, adapter.rollbackCount)
	}
}
