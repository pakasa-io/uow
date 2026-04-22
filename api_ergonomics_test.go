package uow

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSelectorHelpers(t *testing.T) {
	t.Run("select trims and locks a value", func(t *testing.T) {
		got := Select(" primary ")
		want := Selector{Set: true, Value: "primary"}
		if got != want {
			t.Fatalf("Select() = %+v, want %+v", got, want)
		}
	})

	t.Run("default selection locks empty", func(t *testing.T) {
		got := DefaultSelection()
		want := Selector{Set: true, Value: ""}
		if got != want {
			t.Fatalf("DefaultSelection() = %+v, want %+v", got, want)
		}
	})

	t.Run("no tenant is an explicit empty tenant selection", func(t *testing.T) {
		got := NoTenant()
		want := Selector{Set: true, Value: ""}
		if got != want {
			t.Fatalf("NoTenant() = %+v, want %+v", got, want)
		}
	})

	t.Run("SelectAdapter trims and locks an adapter name", func(t *testing.T) {
		got := SelectAdapter(" gorm ")
		want := Selector{Set: true, Value: "gorm"}
		if got != want {
			t.Fatalf("SelectAdapter() = %+v, want %+v", got, want)
		}
	})

	t.Run("SelectClient trims and locks a client name", func(t *testing.T) {
		got := SelectClient(" primary ")
		want := Selector{Set: true, Value: "primary"}
		if got != want {
			t.Fatalf("SelectClient() = %+v, want %+v", got, want)
		}
	})

	t.Run("SelectTenant trims and locks a tenant id", func(t *testing.T) {
		got := SelectTenant(" acme ")
		want := Selector{Set: true, Value: "acme"}
		if got != want {
			t.Fatalf("SelectTenant() = %+v, want %+v", got, want)
		}
	})
}

func TestExecutionConfigValidate(t *testing.T) {
	t.Run("zero value is valid", func(t *testing.T) {
		if err := (ExecutionConfig{}).Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("invalid transactional mode is rejected", func(t *testing.T) {
		err := (ExecutionConfig{Transactional: TransactionalMode(99)}).Validate()
		assertConfigError(t, err)
	})

	t.Run("invalid isolation level is rejected", func(t *testing.T) {
		err := (ExecutionConfig{IsolationLevel: IsolationLevel("custom")}).Validate()
		assertConfigError(t, err)
	})

	t.Run("negative timeout is rejected", func(t *testing.T) {
		err := (ExecutionConfig{Timeout: -time.Second}).Validate()
		assertConfigError(t, err)
	})
}

func TestTxConfigValidate(t *testing.T) {
	t.Run("zero value is valid", func(t *testing.T) {
		if err := (TxConfig{}).Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("invalid isolation level is rejected", func(t *testing.T) {
		err := (TxConfig{IsolationLevel: IsolationLevel("custom")}).Validate()
		assertConfigError(t, err)
	})

	t.Run("negative timeout is rejected", func(t *testing.T) {
		err := (TxConfig{Timeout: -time.Second}).Validate()
		assertConfigError(t, err)
	})
}

func TestExecBuilder(t *testing.T) {
	got := Exec(
		WithAdapter(" sql "),
		WithClient(" primary "),
		WithTenant(" acme "),
		WithTransactional(TransactionalOn),
		WithReadOnly(),
		WithIsolation(IsolationSerializable),
		WithTimeout(5*time.Second),
		WithLabel(" reports "),
	)

	want := ExecutionConfig{
		AdapterName:    SelectAdapter("sql"),
		ClientName:     SelectClient("primary"),
		TenantID:       SelectTenant("acme"),
		Transactional:  TransactionalOn,
		ReadOnly:       true,
		IsolationLevel: IsolationSerializable,
		Timeout:        5 * time.Second,
		Label:          "reports",
	}
	if got != want {
		t.Fatalf("Exec() = %+v, want %+v", got, want)
	}
}

func TestTxBuilder(t *testing.T) {
	got := RootTx(
		WithAdapter(" sql "),
		WithClientSelector(DefaultSelection()),
		WithTenantSelector(NoTenant()),
		WithReadOnly(),
		WithIsolation(IsolationSnapshot),
		WithTimeout(3*time.Second),
		WithLabel(" reports "),
	)

	want := TxConfig{
		AdapterName:    SelectAdapter("sql"),
		ClientName:     DefaultSelection(),
		TenantID:       NoTenant(),
		ReadOnly:       true,
		IsolationLevel: IsolationSnapshot,
		Timeout:        3 * time.Second,
		Label:          "reports",
	}
	if got != want {
		t.Fatalf("RootTx() = %+v, want %+v", got, want)
	}
}

func TestTxConfigFromExecution(t *testing.T) {
	t.Run("copies shared fields from ambient config", func(t *testing.T) {
		got, err := TxConfigFromExecution(ExecutionConfig{
			AdapterName:    SelectAdapter("gorm"),
			ClientName:     SelectClient("primary"),
			TenantID:       SelectTenant("acme"),
			Transactional:  TransactionalOn,
			ReadOnly:       true,
			IsolationLevel: IsolationRepeatableRead,
			Timeout:        2 * time.Second,
			Label:          "reporting",
		})
		if err != nil {
			t.Fatalf("TxConfigFromExecution() error = %v", err)
		}

		want := TxConfig{
			AdapterName:    SelectAdapter("gorm"),
			ClientName:     SelectClient("primary"),
			TenantID:       SelectTenant("acme"),
			ReadOnly:       true,
			IsolationLevel: IsolationRepeatableRead,
			Timeout:        2 * time.Second,
			Label:          "reporting",
		}
		if got != want {
			t.Fatalf("TxConfigFromExecution() = %+v, want %+v", got, want)
		}
	})

	t.Run("rejects invalid transactional mode before converting", func(t *testing.T) {
		_, err := TxConfigFromExecution(ExecutionConfig{
			Transactional: TransactionalMode(99),
		})
		assertConfigError(t, err)
	})
}

func TestManagerRejectsInvalidExecutionAndTxConfig(t *testing.T) {
	adapter := newMockAdapter(Capabilities{RootTransaction: true})
	manager := mustManager(t, DefaultConfig(), ManagerOptions{}, defaultRegistration(adapter))

	t.Run("Bind rejects invalid execution config", func(t *testing.T) {
		_, _, err := manager.Bind(context.Background(), ExecutionConfig{Transactional: TransactionalMode(99)})
		assertConfigError(t, err)
	})

	t.Run("Do rejects invalid execution config before running callback", func(t *testing.T) {
		called := false
		err := manager.Do(context.Background(), ExecutionConfig{Transactional: TransactionalMode(99)}, func(ctx context.Context) error {
			called = true
			return nil
		})
		assertConfigError(t, err)
		if called {
			t.Fatalf("callback should not run")
		}
	})

	t.Run("InTx rejects invalid tx config", func(t *testing.T) {
		called := false
		err := manager.InTx(context.Background(), TxConfig{Timeout: -time.Second}, func(ctx context.Context) error {
			called = true
			return nil
		})
		assertConfigError(t, err)
		if called {
			t.Fatalf("callback should not run")
		}
	})
}

func assertConfigError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error")
	}
	var uerr *UOWError
	if !errors.As(err, &uerr) {
		t.Fatalf("expected UOWError, got %T (%v)", err, err)
	}
	if uerr.Kind != ErrKindConfig {
		t.Fatalf("expected ErrKindConfig, got %s", uerr.Kind)
	}
}
