package fiberuow

import (
	"net/http"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/pakasa-io/uow"
)

func TestConfigValidate(t *testing.T) {
	t.Run("zero value is valid", func(t *testing.T) {
		if err := (Config{}).Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("invalid static execution config is rejected", func(t *testing.T) {
		err := (Config{
			Execution: uow.ExecutionConfig{Transactional: uow.TransactionalMode(99)},
		}).Validate()
		if err == nil {
			t.Fatalf("expected error")
		}
	})

	t.Run("dynamic execution config defers validation to request time", func(t *testing.T) {
		err := (Config{
			Execution:        uow.ExecutionConfig{Transactional: uow.TransactionalMode(99)},
			ResolveExecution: func(*fiber.Ctx) (uow.ExecutionConfig, error) { return uow.ExecutionConfig{}, nil },
		}).Validate()
		if err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})
}

func TestResolveExecutionValidationRunsPerRequest(t *testing.T) {
	adapter := &testAdapter{}
	manager := newManager(t, adapter)

	app := fiber.New()
	app.Get("/invalid", Wrap(manager, Config{
		ResolveExecution: func(*fiber.Ctx) (uow.ExecutionConfig, error) {
			return uow.ExecutionConfig{Transactional: uow.TransactionalMode(99)}, nil
		},
		ErrorHandler: func(c *fiber.Ctx, err error) error {
			return c.SendStatus(fiber.StatusBadRequest)
		},
	}, func(c *fiber.Ctx) error {
		t.Fatalf("handler should not run")
		return nil
	}))

	req, err := http.NewRequest(http.MethodGet, "/invalid", nil)
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

	if resp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("unexpected status: %d", resp.StatusCode)
	}
	if adapter.beginCount != 0 || adapter.commitCount != 0 || adapter.rollbackCount != 0 {
		t.Fatalf("unexpected tx counts: begin=%d commit=%d rollback=%d", adapter.beginCount, adapter.commitCount, adapter.rollbackCount)
	}
}
