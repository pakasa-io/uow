package httpuow

import (
	"net/http"
	"net/http/httptest"
	"testing"

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
			ResolveExecution: func(*http.Request) (uow.ExecutionConfig, error) { return uow.ExecutionConfig{}, nil },
		}).Validate()
		if err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})
}

func TestResolveExecutionValidationRunsPerRequest(t *testing.T) {
	adapter := &testAdapter{}
	manager := newManager(t, adapter)
	called := false

	handler := Wrap(manager, Config{
		ResolveExecution: func(*http.Request) (uow.ExecutionConfig, error) {
			return uow.ExecutionConfig{Transactional: uow.TransactionalMode(99)}, nil
		},
		ErrorHandler: func(ctx ErrorContext) {
			called = true
			ctx.ResponseWriter.WriteHeader(http.StatusBadRequest)
		},
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("handler should not run")
	}))

	req := httptest.NewRequest(http.MethodGet, "/invalid", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !called {
		t.Fatalf("expected error handler to run")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if adapter.beginCount != 0 || adapter.commitCount != 0 || adapter.rollbackCount != 0 {
		t.Fatalf("unexpected tx counts: begin=%d commit=%d rollback=%d", adapter.beginCount, adapter.commitCount, adapter.rollbackCount)
	}
}
