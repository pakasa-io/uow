package gormadapter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/pakasa-io/uow"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type testRecord struct {
	ID   uint `gorm:"primaryKey"`
	Name string
}

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", sanitizeTestName(t.Name()))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db.DB: %v", err)
	}
	t.Cleanup(func() {
		_ = sqlDB.Close()
	})
	if err := db.AutoMigrate(&testRecord{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	return db
}

func registerManager(t *testing.T, cfg uow.Config, adapter *Adapter, db *gorm.DB) *uow.Manager {
	t.Helper()
	registry := uow.NewRegistry()
	registry.MustRegister(uow.Registration{
		Adapter:    adapter,
		Client:     db,
		ClientName: "primary",
		Default:    true,
	})
	manager, err := uow.NewManager(registry, cfg, uow.ManagerOptions{})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return manager
}

func TestCurrentHandleAndCommit(t *testing.T) {
	db := openTestDB(t)
	manager := registerManager(t, uow.DefaultConfig(), New(""), db)

	err := manager.InTx(context.Background(), uow.TxConfig{}, func(ctx context.Context) error {
		current := MustCurrent(uow.MustFrom(ctx))
		if err := current.Create(&testRecord{Name: "committed"}).Error; err != nil {
			return err
		}
		if _, ok := CurrentTx(uow.MustFrom(ctx)); !ok {
			t.Fatalf("expected active gorm tx")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("InTx: %v", err)
	}

	var count int64
	if err := db.Model(&testRecord{}).Where("name = ?", "committed").Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 committed row, got %d", count)
	}
}

func TestRollbackOnError(t *testing.T) {
	db := openTestDB(t)
	manager := registerManager(t, uow.DefaultConfig(), New(""), db)
	workErr := errors.New("boom")

	err := manager.InTx(context.Background(), uow.TxConfig{}, func(ctx context.Context) error {
		if err := MustCurrent(uow.MustFrom(ctx)).Create(&testRecord{Name: "rolled-back"}).Error; err != nil {
			return err
		}
		return workErr
	})
	if !errors.Is(err, workErr) {
		t.Fatalf("expected callback error, got %v", err)
	}

	var count int64
	if err := db.Model(&testRecord{}).Where("name = ?", "rolled-back").Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected rollback, found %d rows", count)
	}
}

func TestCurrentOutsideTransaction(t *testing.T) {
	db := openTestDB(t)
	manager := registerManager(t, uow.DefaultConfig(), New(""), db)

	_, work, err := manager.Bind(context.Background(), uow.ExecutionConfig{})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	current, err := Current(work)
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if current == nil {
		t.Fatalf("expected gorm db")
	}
	if _, ok := CurrentTx(work); ok {
		t.Fatalf("expected no active tx")
	}
}

func TestNestedSavepoints(t *testing.T) {
	db := openTestDB(t)
	cfg := uow.DefaultConfig()
	cfg.NestedMode = uow.NestedStrict
	manager := registerManager(t, cfg, New("", WithNestedSavepoints(true)), db)
	nestedErr := errors.New("nested failed")

	err := manager.InTx(context.Background(), uow.TxConfig{}, func(ctx context.Context) error {
		if err := MustCurrent(uow.MustFrom(ctx)).Create(&testRecord{Name: "outer"}).Error; err != nil {
			return err
		}

		err := manager.InNestedTx(ctx, uow.NestedOptions{Label: "child"}, func(ctx context.Context) error {
			if err := MustCurrent(uow.MustFrom(ctx)).Create(&testRecord{Name: "inner"}).Error; err != nil {
				return err
			}
			return nestedErr
		})
		if !errors.Is(err, nestedErr) {
			t.Fatalf("expected nested error, got %v", err)
		}

		if uow.MustFrom(ctx).IsRollbackOnly() {
			t.Fatalf("strict nested rollback should not mark root rollback-only")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("outer InTx: %v", err)
	}

	var outerCount int64
	if err := db.Model(&testRecord{}).Where("name = ?", "outer").Count(&outerCount).Error; err != nil {
		t.Fatalf("count outer: %v", err)
	}
	if outerCount != 1 {
		t.Fatalf("expected outer row committed, got %d", outerCount)
	}

	var innerCount int64
	if err := db.Model(&testRecord{}).Where("name = ?", "inner").Count(&innerCount).Error; err != nil {
		t.Fatalf("count inner: %v", err)
	}
	if innerCount != 0 {
		t.Fatalf("expected inner row rolled back, got %d", innerCount)
	}
}

func TestNestedUnsupportedByDefault(t *testing.T) {
	db := openTestDB(t)
	cfg := uow.DefaultConfig()
	cfg.NestedMode = uow.NestedStrict
	manager := registerManager(t, cfg, New(""), db)

	err := manager.InTx(context.Background(), uow.TxConfig{}, func(ctx context.Context) error {
		_, err := uow.MustFrom(ctx).BeginNested(ctx, uow.NestedOptions{})
		if !errors.Is(err, uow.ErrNestedTxUnsupported) {
			t.Fatalf("expected nested unsupported, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("InTx: %v", err)
	}
}

func TestIsolationValidation(t *testing.T) {
	db := openTestDB(t)
	manager := registerManager(t, uow.DefaultConfig(), New(""), db)

	err := manager.InTx(context.Background(), uow.TxConfig{
		IsolationLevel: uow.IsolationLevel("custom"),
	}, func(ctx context.Context) error { return nil })
	if err == nil {
		t.Fatalf("expected isolation validation error")
	}
}

func sanitizeTestName(name string) string {
	replacer := strings.NewReplacer("/", "_", " ", "_")
	return replacer.Replace(name)
}
