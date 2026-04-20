package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"

	_ "github.com/mattn/go-sqlite3"

	"github.com/pakasa-io/uow"
	sqladapter "github.com/pakasa-io/uow/adapters/sql"
)

type CreateUserRequest struct {
	ID    string
	Email string
}

type UserController struct {
	manager *uow.Manager
	service *UserService
}

type UserService struct {
	repo *UserRepository
}

type UserRepository struct{}

func main() {
	db, manager, err := openManager()
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	controller := &UserController{
		manager: manager,
		service: &UserService{
			repo: &UserRepository{},
		},
	}

	err = controller.CreateUser(context.Background(), CreateUserRequest{
		ID:    "layered_user",
		Email: "layers@example.com",
	})
	if err != nil {
		log.Fatal(err)
	}

	var tenantID string
	if err := db.QueryRow(`select tenant_id from users where id = ?`, "layered_user").Scan(&tenantID); err != nil {
		log.Fatal(err)
	}

	fmt.Printf("persisted_users=%d tenant=%s\n", queryCount(db), tenantID)
}

func (c *UserController) CreateUser(ctx context.Context, req CreateUserRequest) error {
	ctx = uow.WithTenantID(ctx, "tenant_layers")
	return c.manager.Do(ctx, uow.ExecutionConfig{
		Transactional: uow.TransactionalOn,
		Label:         "controller-create-user",
	}, func(ctx context.Context) error {
		return c.service.CreateUser(ctx, req)
	})
}

func (s *UserService) CreateUser(ctx context.Context, req CreateUserRequest) error {
	if strings.TrimSpace(req.Email) == "" {
		return fmt.Errorf("email is required")
	}
	exists, err := s.repo.ExistsByEmail(ctx, req.Email)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("email %q already exists", req.Email)
	}
	return s.repo.Insert(ctx, req)
}

func (r *UserRepository) ExistsByEmail(ctx context.Context, email string) (bool, error) {
	handle := sqladapter.MustCurrent(uow.MustFrom(ctx))
	row := handle.QueryRowContext(ctx, `select count(*) from users where email = ?`, email)

	var count int
	if err := row.Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *UserRepository) Insert(ctx context.Context, req CreateUserRequest) error {
	work := uow.MustFrom(ctx)
	handle := sqladapter.MustCurrent(work)
	_, err := handle.ExecContext(ctx,
		`insert into users(id, email, tenant_id) values (?, ?, ?)`,
		req.ID,
		req.Email,
		work.Binding().TenantID,
	)
	return err
}

func openManager() (*sql.DB, *uow.Manager, error) {
	db, err := sql.Open("sqlite3", "file:layers_example?mode=memory&cache=shared")
	if err != nil {
		return nil, nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if _, err := db.Exec(`create table users (id text primary key, email text not null unique, tenant_id text not null)`); err != nil {
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
