package main

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/pakasa-io/uow"
	gormadapter "github.com/pakasa-io/uow/adapters/gorm"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type LedgerEntry struct {
	ID      uint `gorm:"primaryKey"`
	Message string
}

type AuditEntry struct {
	ID   uint `gorm:"primaryKey"`
	Note string
}

func main() {
	db, manager, err := openManager()
	if err != nil {
		log.Fatal(err)
	}

	err = manager.InTx(context.Background(), uow.TxConfig{Label: "root-ledger"}, func(ctx context.Context) error {
		rootDB := gormadapter.MustCurrent(uow.MustFrom(ctx))
		if err := rootDB.Create(&LedgerEntry{Message: "root transaction started"}).Error; err != nil {
			return err
		}

		nestedErr := manager.InNestedTx(ctx, uow.NestedOptions{Label: "optional-audit"}, func(ctx context.Context) error {
			work := uow.MustFrom(ctx)
			current, ok := work.Current()
			if !ok {
				return errors.New("nested transaction not available")
			}
			fmt.Printf("nested_depth=%d\n", current.Depth())

			nestedDB := gormadapter.MustCurrent(work)
			if err := nestedDB.Create(&AuditEntry{Note: "savepoint-only write"}).Error; err != nil {
				return err
			}
			return errors.New("discard audit without aborting root")
		})
		fmt.Printf("nested_error=%v\n", nestedErr)

		return rootDB.Create(&LedgerEntry{Message: "root transaction committed"}).Error
	})
	if err != nil {
		log.Fatal(err)
	}

	var ledgerCount int64
	if err := db.Model(&LedgerEntry{}).Count(&ledgerCount).Error; err != nil {
		log.Fatal(err)
	}
	var auditCount int64
	if err := db.Model(&AuditEntry{}).Count(&auditCount).Error; err != nil {
		log.Fatal(err)
	}

	fmt.Printf("ledger_entries=%d audit_entries=%d\n", ledgerCount, auditCount)
}

func openManager() (*gorm.DB, *uow.Manager, error) {
	db, err := gorm.Open(sqlite.Open("file:nested_example?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		return nil, nil, err
	}
	if err := db.AutoMigrate(&LedgerEntry{}, &AuditEntry{}); err != nil {
		return nil, nil, err
	}

	registry := uow.NewRegistry()
	registry.MustRegister(uow.Registration{
		Adapter:    gormadapter.New("gorm", gormadapter.WithNestedSavepoints(true)),
		Client:     db,
		ClientName: "primary",
		Default:    true,
	})

	cfg := uow.DefaultConfig()
	cfg.NestedMode = uow.NestedStrict

	manager, err := uow.NewManager(registry, cfg, uow.ManagerOptions{})
	if err != nil {
		return nil, nil, err
	}
	return db, manager, nil
}
