package main

import (
	"context"
	"fmt"
	"log"

	"github.com/pakasa-io/uow"
	gormadapter "github.com/pakasa-io/uow/adapters/gorm"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type Account struct {
	ID      uint `gorm:"primaryKey"`
	Email   string
	Balance int
}

func main() {
	db, manager, err := openManager()
	if err != nil {
		log.Fatal(err)
	}

	err = manager.InTx(context.Background(), uow.TxConfig{Label: "gorm-create-account"}, func(ctx context.Context) error {
		work := uow.MustFrom(ctx)
		tx := gormadapter.MustCurrent(work)
		return tx.Create(&Account{
			Email:   "gorm@example.com",
			Balance: 1250,
		}).Error
	})
	if err != nil {
		log.Fatal(err)
	}

	var count int64
	if err := db.Model(&Account{}).Count(&count).Error; err != nil {
		log.Fatal(err)
	}

	fmt.Printf("accounts=%d\n", count)
}

func openManager() (*gorm.DB, *uow.Manager, error) {
	db, err := gorm.Open(sqlite.Open("file:gorm_example?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		return nil, nil, err
	}
	if err := db.AutoMigrate(&Account{}); err != nil {
		return nil, nil, err
	}

	registry := uow.NewRegistry()
	registry.MustRegister(uow.Registration{
		Adapter:    gormadapter.New("gorm"),
		Client:     db,
		ClientName: "primary",
		Default:    true,
	})

	manager, err := uow.NewManager(registry, uow.DefaultConfig(), uow.ManagerOptions{})
	if err != nil {
		return nil, nil, err
	}
	return db, manager, nil
}
