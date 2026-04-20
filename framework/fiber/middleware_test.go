package fiberuow

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/gofiber/fiber/v2"
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
}

func (a *testAdapter) Name() string { return "test" }

func (a *testAdapter) Capabilities() uow.Capabilities {
	return uow.Capabilities{RootTransaction: true}
}

func (a *testAdapter) Begin(ctx context.Context, client any, opts uow.BeginOptions) (uow.Tx, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
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
	return nil
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

func TestMiddlewareInjectsUnitOfWorkForGroup(t *testing.T) {
	adapter := &testAdapter{}
	manager := newManager(t, adapter)

	app := fiber.New()
	api := app.Group("/api", Middleware(manager, Config{
		Execution: uow.ExecutionConfig{Transactional: uow.TransactionalOn},
		ResolveTenant: func(c *fiber.Ctx) (string, error) {
			return "tenant-acme", nil
		},
	}))
	api.Get("/items", func(c *fiber.Ctx) error {
		work := uow.MustFrom(c.UserContext())
		if !work.InTransaction() {
			t.Fatalf("expected active transaction")
		}
		if work.Binding().TenantID != "tenant-acme" {
			t.Fatalf("unexpected binding: %+v", work.Binding())
		}
		return c.SendStatus(fiber.StatusNoContent)
	})

	req, err := http.NewRequest(http.MethodGet, "/api/items", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			t.Errorf("Close body: %v", closeErr)
		}
	}()

	if resp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("unexpected status: %d", resp.StatusCode)
	}
	if adapter.beginCount != 1 || adapter.commitCount != 1 || adapter.rollbackCount != 0 {
		t.Fatalf("unexpected tx counts: begin=%d commit=%d rollback=%d", adapter.beginCount, adapter.commitCount, adapter.rollbackCount)
	}
}

func TestWrapRollsBackOnStatus(t *testing.T) {
	adapter := &testAdapter{}
	manager := newManager(t, adapter)

	app := fiber.New()
	app.Get("/fail", Wrap(manager, Config{
		Execution:        uow.ExecutionConfig{Transactional: uow.TransactionalOn},
		RollbackOnStatus: RollbackOn5xx,
	}, func(c *fiber.Ctx) error {
		c.Status(fiber.StatusInternalServerError)
		return c.SendString("failed")
	}))

	req, err := http.NewRequest(http.MethodGet, "/fail", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			t.Errorf("Close body: %v", closeErr)
		}
	}()

	if resp.StatusCode != fiber.StatusInternalServerError {
		t.Fatalf("unexpected status: %d", resp.StatusCode)
	}
	if adapter.beginCount != 1 || adapter.commitCount != 0 || adapter.rollbackCount != 1 {
		t.Fatalf("unexpected tx counts: begin=%d commit=%d rollback=%d", adapter.beginCount, adapter.commitCount, adapter.rollbackCount)
	}
}

func TestReturnedFiberErrorCanUseStatusPolicy(t *testing.T) {
	adapter := &testAdapter{}
	manager := newManager(t, adapter)

	app := fiber.New()
	app.Get("/missing", Wrap(manager, Config{
		Execution:        uow.ExecutionConfig{Transactional: uow.TransactionalOn},
		RollbackOnStatus: RollbackOn4xx5xx,
		RollbackOnError: func(err error) bool {
			return false
		},
	}, func(c *fiber.Ctx) error {
		return fiber.ErrNotFound
	}))

	req, err := http.NewRequest(http.MethodGet, "/missing", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			t.Errorf("Close body: %v", closeErr)
		}
	}()

	if resp.StatusCode != fiber.StatusNotFound {
		t.Fatalf("unexpected status: %d", resp.StatusCode)
	}
	if adapter.beginCount != 1 || adapter.commitCount != 0 || adapter.rollbackCount != 1 {
		t.Fatalf("unexpected tx counts: begin=%d commit=%d rollback=%d", adapter.beginCount, adapter.commitCount, adapter.rollbackCount)
	}
}

func TestResolveExecutionCanDisableTransactions(t *testing.T) {
	adapter := &testAdapter{}
	manager := newManager(t, adapter)

	app := fiber.New()
	app.Use(Middleware(manager, Config{
		ResolveExecution: func(c *fiber.Ctx) (uow.ExecutionConfig, error) {
			if c.Get("X-Tx") == "on" {
				return uow.ExecutionConfig{Transactional: uow.TransactionalOn}, nil
			}
			return uow.ExecutionConfig{Transactional: uow.TransactionalOff}, nil
		},
	}))
	app.Get("/mode", func(c *fiber.Ctx) error {
		txEnabled := c.Get("X-Tx") == "on"
		if uow.MustFrom(c.UserContext()).InTransaction() != txEnabled {
			t.Fatalf("unexpected transaction state for X-Tx=%q", c.Get("X-Tx"))
		}
		return c.SendStatus(fiber.StatusNoContent)
	})

	req, err := http.NewRequest(http.MethodGet, "/mode", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("X-Tx", "off")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if closeErr := resp.Body.Close(); closeErr != nil {
		t.Fatalf("Close body: %v", closeErr)
	}

	req, err = http.NewRequest(http.MethodGet, "/mode", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("X-Tx", "on")
	resp, err = app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if closeErr := resp.Body.Close(); closeErr != nil {
		t.Fatalf("Close body: %v", closeErr)
	}

	if adapter.beginCount != 1 || adapter.commitCount != 1 || adapter.rollbackCount != 0 {
		t.Fatalf("unexpected tx counts: begin=%d commit=%d rollback=%d", adapter.beginCount, adapter.commitCount, adapter.rollbackCount)
	}
}
