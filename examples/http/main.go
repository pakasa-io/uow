package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pakasa-io/uow"
	sqladapter "github.com/pakasa-io/uow/adapters/sql"
	httpuow "github.com/pakasa-io/uow/framework/http"
)

func main() {
	db, manager, err := openManager()
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	resolveTenant := func(r *http.Request) (string, error) {
		return r.Header.Get("X-Tenant-ID"), nil
	}

	mux := http.NewServeMux()
	mux.Handle("/healthz", httpuow.Wrap(manager, httpuow.Config{
		Execution: uow.ExecutionConfig{
			Transactional: uow.TransactionalOff,
			Label:         "healthz",
		},
		ResolveTenant: resolveTenant,
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		work := uow.MustFrom(r.Context())
		fmt.Fprintf(w, "route=healthz in_tx=%v tenant=%s", work.InTransaction(), work.Binding().TenantID)
	})))

	mux.Handle("/users", httpuow.Wrap(manager, httpuow.Config{
		Execution: uow.ExecutionConfig{
			Transactional: uow.TransactionalOn,
			Label:         "create-user",
		},
		ResolveTenant: resolveTenant,
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		work := uow.MustFrom(r.Context())
		handle := sqladapter.MustCurrent(work)
		if _, err := handle.ExecContext(r.Context(), `insert into users(id, tenant_id) values (?, ?)`, "http_user", work.Binding().TenantID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, "route=users in_tx=%v tenant=%s", work.InTransaction(), work.Binding().TenantID)
	})))

	healthReq := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	healthReq.Header.Set("X-Tenant-ID", "tenant_http")
	healthRes := httptest.NewRecorder()
	mux.ServeHTTP(healthRes, healthReq)

	userReq := httptest.NewRequest(http.MethodPost, "/users", nil)
	userReq.Header.Set("X-Tenant-ID", "tenant_http")
	userRes := httptest.NewRecorder()
	mux.ServeHTTP(userRes, userReq)

	fmt.Println(strings.TrimSpace(healthRes.Body.String()))
	fmt.Println(strings.TrimSpace(userRes.Body.String()))
	fmt.Printf("persisted_users=%d\n", queryCount(db))
}

func openManager() (*sql.DB, *uow.Manager, error) {
	db, err := sql.Open("sqlite3", "file:http_example?mode=memory&cache=shared")
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
