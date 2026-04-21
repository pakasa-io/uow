package fiberuow

import (
	"context"
	"errors"

	"github.com/gofiber/fiber/v2"
	"github.com/pakasa-io/uow"
)

// ErrorHandler handles execution and finalization failures.
type ErrorHandler func(*fiber.Ctx, error) error

// Config controls per-route and middleware behavior for Fiber integration.
type Config struct {
	Execution              uow.ExecutionConfig
	ResolveExecution       func(*fiber.Ctx) (uow.ExecutionConfig, error)
	ResolveTenant          func(*fiber.Ctx) (string, error)
	ResolveBindingOverride func(*fiber.Ctx) (uow.BindingOverride, bool, error)
	RollbackOnStatus       func(int) bool
	RollbackOnError        func(error) bool
	ErrorHandler           ErrorHandler
}

// Validate validates the static portion of the Fiber integration config.
//
// When ResolveExecution is set, execution settings are validated per request.
func (c Config) Validate() error {
	if c.ResolveExecution != nil {
		return nil
	}
	return c.Execution.Validate()
}

func (c Config) execution(ctx *fiber.Ctx) (uow.ExecutionConfig, error) {
	var execCfg uow.ExecutionConfig
	if c.ResolveExecution != nil {
		var err error
		execCfg, err = c.ResolveExecution(ctx)
		if err != nil {
			return uow.ExecutionConfig{}, err
		}
	} else {
		execCfg = c.Execution
	}
	if err := execCfg.Validate(); err != nil {
		return uow.ExecutionConfig{}, err
	}
	return execCfg, nil
}

func (c Config) decorateContext(ctx context.Context, cctx *fiber.Ctx) (context.Context, error) {
	if c.ResolveTenant != nil {
		tenantID, err := c.ResolveTenant(cctx)
		if err != nil {
			return nil, err
		}
		if tenantID != "" {
			ctx = uow.WithTenantID(ctx, tenantID)
		}
	}
	if c.ResolveBindingOverride != nil {
		override, ok, err := c.ResolveBindingOverride(cctx)
		if err != nil {
			return nil, err
		}
		if ok {
			ctx = uow.WithBindingOverride(ctx, override)
		}
	}
	return ctx, nil
}

func (c Config) shouldRollbackError(err error, statusCode int) bool {
	if err == nil {
		return false
	}
	if c.RollbackOnError != nil && c.RollbackOnError(err) {
		return true
	}
	if c.RollbackOnStatus != nil && statusCode > 0 && c.RollbackOnStatus(statusCode) {
		return true
	}
	return c.RollbackOnError == nil
}

func (c Config) handleError(ctx *fiber.Ctx, err error) error {
	handler := c.ErrorHandler
	if handler == nil {
		handler = DefaultErrorHandler
	}
	return handler(ctx, err)
}

// DefaultErrorHandler preserves status-based rollback responses when possible
// and otherwise delegates to Fiber's application error handler.
func DefaultErrorHandler(ctx *fiber.Ctx, err error) error {
	var statusErr *StatusError
	if errors.As(err, &statusErr) {
		ctx.Status(statusErr.StatusCode)
		return nil
	}
	return err
}
