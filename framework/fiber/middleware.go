package fiberuow

import (
	"context"
	"errors"

	"github.com/gofiber/fiber/v2"
	"github.com/pakasa-io/uow"
)

// Middleware injects a managed UnitOfWork for downstream Fiber handlers.
func Middleware(manager *uow.Manager, cfg Config) fiber.Handler {
	if manager == nil {
		panic("fiberuow: nil manager")
	}
	return func(ctx *fiber.Ctx) error {
		return execute(manager, cfg, ctx, ctx.Next)
	}
}

// Wrap applies Config to one Fiber handler.
func Wrap(manager *uow.Manager, cfg Config, next fiber.Handler) fiber.Handler {
	if next == nil {
		panic("fiberuow: nil handler")
	}
	return func(ctx *fiber.Ctx) error {
		return execute(manager, cfg, ctx, func() error {
			return next(ctx)
		})
	}
}

func execute(manager *uow.Manager, cfg Config, ctx *fiber.Ctx, next func() error) error {
	baseCtx := ctx.UserContext()
	if baseCtx == nil {
		baseCtx = context.Background()
	}

	execCfg, err := cfg.execution(ctx)
	if err != nil {
		return cfg.handleError(ctx, err)
	}
	baseCtx, err = cfg.decorateContext(baseCtx, ctx)
	if err != nil {
		return cfg.handleError(ctx, err)
	}

	var passthroughErr error
	runErr := manager.Run(baseCtx, execCfg, func(execCtx context.Context) error {
		ctx.SetUserContext(execCtx)

		handlerErr := next()
		if handlerErr != nil {
			if cfg.shouldRollbackError(handlerErr, statusCodeFromHandlerError(ctx, handlerErr)) {
				return handlerErr
			}
			passthroughErr = handlerErr
			return nil
		}
		if cfg.RollbackOnStatus != nil && cfg.RollbackOnStatus(ctx.Response().StatusCode()) {
			return markRollbackOnly(execCtx, ctx.Response().StatusCode())
		}
		return nil
	})
	if runErr != nil {
		return cfg.handleError(ctx, runErr)
	}
	if passthroughErr != nil {
		return cfg.handleError(ctx, passthroughErr)
	}
	return nil
}

func markRollbackOnly(ctx context.Context, statusCode int) error {
	work, ok := uow.From(ctx)
	if !ok || !work.InTransaction() {
		return nil
	}
	if err := work.SetRollbackOnly(&StatusError{StatusCode: statusCode}); err != nil && !errors.Is(err, uow.ErrNoActiveTransaction) {
		return err
	}
	return nil
}

func statusCodeFromHandlerError(ctx *fiber.Ctx, err error) int {
	var fiberErr *fiber.Error
	if errors.As(err, &fiberErr) {
		return fiberErr.Code
	}
	return ctx.Response().StatusCode()
}
