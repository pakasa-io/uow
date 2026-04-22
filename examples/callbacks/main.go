package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pakasa-io/uow"
	sqladapter "github.com/pakasa-io/uow/adapters/sql"
)

type UserCreated struct {
	UserID string
	Email  string
}

type UserCreatedCallback func(context.Context, UserCreated) error

type UserCreatedCallbacks []UserCreatedCallback

func (c UserCreatedCallbacks) Run(ctx context.Context, evt UserCreated) error {
	for _, callback := range c {
		if err := callback(ctx, evt); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	db, manager, err := openManager()
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	event := UserCreated{
		UserID: "user_123",
		Email:  "user@example.com",
	}

	callbacks := UserCreatedCallbacks{
		writeAuditEntry,
	}

	err = manager.InTx(context.Background(), uow.TxConfig{Label: "register-user"}, func(ctx context.Context) error {
		handle := sqladapter.MustCurrent(uow.MustFrom(ctx))
		if _, err := handle.ExecContext(ctx, `insert into users(id, email) values (?, ?)`, event.UserID, event.Email); err != nil {
			return err
		}
		return callbacks.Run(ctx, event)
	})
	if err != nil {
		log.Fatal(err)
	}

	backgroundErr := dispatchBackground(context.Background(), manager, event, recordBackgroundProjection)
	if err := <-backgroundErr; err != nil {
		log.Fatal(err)
	}

	fmt.Printf("users=%d audit_entries=%d background_jobs=%d\n",
		queryCount(db, "users"),
		queryCount(db, "audit_log"),
		queryCount(db, "background_jobs"),
	)
}

func writeAuditEntry(ctx context.Context, evt UserCreated) error {
	work := uow.MustFrom(ctx)
	handle := sqladapter.MustCurrent(work)
	message := fmt.Sprintf("created %s in_tx=%v", evt.Email, work.InTransaction())
	_, err := handle.ExecContext(ctx, `insert into audit_log(user_id, message) values (?, ?)`, evt.UserID, message)
	return err
}

func recordBackgroundProjection(ctx context.Context, evt UserCreated) error {
	work := uow.MustFrom(ctx)
	handle := sqladapter.MustCurrent(work)
	_, err := handle.ExecContext(ctx,
		`insert into background_jobs(user_id, tenant_id, note) values (?, ?, ?)`,
		evt.UserID,
		work.Binding().TenantID,
		fmt.Sprintf("background callback in_tx=%v", work.InTransaction()),
	)
	return err
}

func dispatchBackground(parent context.Context, manager *uow.Manager, evt UserCreated, callback UserCreatedCallback) <-chan error {
	result := make(chan error, 1)
	go func() {
		defer close(result)

		background := context.Background()
		if parent != nil {
			background = context.WithoutCancel(parent)
		}
		background = uow.WithTenantID(background, "tenant_callbacks")

		result <- manager.Run(background, uow.ExecutionConfig{
			Transactional: uow.TransactionalOn,
			Label:         "user-created-callback",
		}, func(ctx context.Context) error {
			return callback(ctx, evt)
		})
	}()
	return result
}

func openManager() (*sql.DB, *uow.Manager, error) {
	db, err := sql.Open("sqlite3", "file:callbacks_example?mode=memory&cache=shared")
	if err != nil {
		return nil, nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := createSchema(db); err != nil {
		db.Close()
		return nil, nil, err
	}

	registry := uow.NewRegistry()
	registry.MustRegister(uow.Registration{
		Adapter:    sqladapter.New("sql"),
		Client:     db,
		ClientName: "primary",
		Default:    true,
	})

	manager, err := uow.NewManager(registry, uow.DefaultConfig(), uow.ManagerOptions{
		TenantPolicy: uow.ContextTenantPolicy{},
	})
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return db, manager, nil
}

func createSchema(db *sql.DB) error {
	statements := []string{
		`create table users (id text primary key, email text not null)`,
		`create table audit_log (id integer primary key autoincrement, user_id text not null, message text not null)`,
		`create table background_jobs (id integer primary key autoincrement, user_id text not null, tenant_id text not null, note text not null)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

func queryCount(db *sql.DB, table string) int {
	var count int
	row := db.QueryRow(fmt.Sprintf("select count(*) from %s", table))
	if err := row.Scan(&count); err != nil {
		log.Fatal(err)
	}
	return count
}
