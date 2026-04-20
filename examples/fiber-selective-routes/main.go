package main

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http/httptest"
	"strings"

	_ "github.com/mattn/go-sqlite3"

	"github.com/gofiber/fiber/v2"
	"github.com/pakasa-io/uow"
	sqladapter "github.com/pakasa-io/uow/adapters/sql"
	fiberuow "github.com/pakasa-io/uow/framework/fiber"
)

func main() {
	db, manager, err := openManager()
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	app := fiber.New()
	api := app.Group("/api", fiberuow.Middleware(manager, fiberuow.Config{
		Execution: uow.ExecutionConfig{
			Transactional: uow.TransactionalOff,
			Label:         "api-default",
		},
		ResolveTenant: func(c *fiber.Ctx) (string, error) {
			return c.Get("X-Tenant-ID"), nil
		},
	}))

	api.Get("/healthz", func(c *fiber.Ctx) error {
		work := uow.MustFrom(c.UserContext())
		return c.SendString(fmt.Sprintf("route=healthz in_tx=%v tenant=%s", work.InTransaction(), work.Binding().TenantID))
	})

	api.Post("/users", fiberuow.Wrap(manager, fiberuow.Config{
		Execution: uow.ExecutionConfig{
			Transactional: uow.TransactionalOn,
			Label:         "fiber-create-user",
		},
	}, func(c *fiber.Ctx) error {
		work := uow.MustFrom(c.UserContext())
		handle := sqladapter.MustCurrent(work)
		if _, err := handle.ExecContext(c.UserContext(), `insert into users(id, tenant_id) values (?, ?)`, "fiber_user", work.Binding().TenantID); err != nil {
			return err
		}
		return c.SendString(fmt.Sprintf("route=users in_tx=%v tenant=%s", work.InTransaction(), work.Binding().TenantID))
	}))

	healthBody := call(app, "/api/healthz")
	userBody := call(app, "/api/users")

	fmt.Println(healthBody)
	fmt.Println(userBody)
	fmt.Printf("persisted_users=%d\n", queryCount(db))
}

func call(app *fiber.App, path string) string {
	req := httptest.NewRequest("GET", path, nil)
	if strings.HasSuffix(path, "/users") {
		req.Method = "POST"
	}
	req.Header.Set("X-Tenant-ID", "tenant_fiber")

	resp, err := app.Test(req)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Fatal(closeErr)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}
	return strings.TrimSpace(string(body))
}

func openManager() (*sql.DB, *uow.Manager, error) {
	db, err := sql.Open("sqlite3", "file:fiber_example?mode=memory&cache=shared")
	if err != nil {
		return nil, nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if _, err := db.Exec(`create table users (id text primary key, tenant_id text not null)`); err != nil {
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

func queryCount(db *sql.DB) int {
	var count int
	if err := db.QueryRow(`select count(*) from users`).Scan(&count); err != nil {
		log.Fatal(err)
	}
	return count
}
