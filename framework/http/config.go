package httpuow

import (
	"context"
	"errors"
	"net/http"

	"github.com/pakasa-io/uow"
)

// ErrorContext describes an integration error after managed execution.
type ErrorContext struct {
	ResponseWriter http.ResponseWriter
	Request        *http.Request
	Err            error
	StatusCode     int
	Started        bool
}

// ErrorHandler handles integration and finalization failures.
type ErrorHandler func(ErrorContext)

// Config controls per-handler and per-request HTTP integration behavior.
type Config struct {
	Execution              uow.ExecutionConfig
	ResolveExecution       func(*http.Request) (uow.ExecutionConfig, error)
	ResolveTenant          func(*http.Request) (string, error)
	ResolveBindingOverride func(*http.Request) (uow.BindingOverride, bool, error)
	RollbackOnStatus       func(int) bool
	ErrorHandler           ErrorHandler
}

// Validate validates the static portion of the HTTP integration config.
//
// When ResolveExecution is set, execution settings are validated per request.
func (c Config) Validate() error {
	if c.ResolveExecution != nil {
		return nil
	}
	return c.Execution.Validate()
}

func (c Config) execution(r *http.Request) (uow.ExecutionConfig, error) {
	var execCfg uow.ExecutionConfig
	if c.ResolveExecution != nil {
		var err error
		execCfg, err = c.ResolveExecution(r)
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

func (c Config) decorateContext(ctx context.Context, r *http.Request) (context.Context, error) {
	if c.ResolveTenant != nil {
		tenantID, err := c.ResolveTenant(r)
		if err != nil {
			return nil, err
		}
		if tenantID != "" {
			ctx = uow.WithTenantID(ctx, tenantID)
		}
	}
	if c.ResolveBindingOverride != nil {
		override, ok, err := c.ResolveBindingOverride(r)
		if err != nil {
			return nil, err
		}
		if ok {
			ctx = uow.WithBindingOverride(ctx, override)
		}
	}
	return ctx, nil
}

func (c Config) handleError(w http.ResponseWriter, r *http.Request, err error, statusCode int, started bool) {
	handler := c.ErrorHandler
	if handler == nil {
		handler = DefaultErrorHandler
	}
	handler(ErrorContext{
		ResponseWriter: w,
		Request:        r,
		Err:            err,
		StatusCode:     statusCode,
		Started:        started,
	})
}

// DefaultErrorHandler preserves status-based rollback responses when possible
// and otherwise returns HTTP 500 when the response has not started yet.
func DefaultErrorHandler(ctx ErrorContext) {
	if ctx.Started {
		return
	}
	var statusErr *StatusError
	if errors.As(ctx.Err, &statusErr) {
		ctx.ResponseWriter.WriteHeader(statusErr.StatusCode)
		return
	}
	http.Error(ctx.ResponseWriter, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}
