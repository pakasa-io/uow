package uow

import "context"

type contextKey string

const (
	uowContextKey             contextKey = "uow.unit_of_work"
	bindingOverrideContextKey contextKey = "uow.binding_override"
	tenantIDContextKey        contextKey = "uow.tenant_id"
)

// With stores a UnitOfWork in context.
func With(ctx context.Context, uow UnitOfWork) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if uow == nil {
		return ctx
	}
	return context.WithValue(ctx, uowContextKey, uow)
}

// From returns the UnitOfWork stored in context.
func From(ctx context.Context) (UnitOfWork, bool) {
	if ctx == nil {
		return nil, false
	}
	uow, ok := ctx.Value(uowContextKey).(UnitOfWork)
	return uow, ok
}

// MustFrom returns the UnitOfWork stored in context or panics with
// ErrUOWNotFound.
func MustFrom(ctx context.Context) UnitOfWork {
	uow, ok := From(ctx)
	if !ok {
		panic(ErrUOWNotFound)
	}
	return uow
}

// WithBindingOverride stores a binding override in context.
func WithBindingOverride(ctx context.Context, override BindingOverride) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, bindingOverrideContextKey, override)
}

// BindingOverrideFrom returns the binding override stored in context.
func BindingOverrideFrom(ctx context.Context) (BindingOverride, bool) {
	if ctx == nil {
		return BindingOverride{}, false
	}
	override, ok := ctx.Value(bindingOverrideContextKey).(BindingOverride)
	return override, ok
}

// WithTenantID stores a tenant identity in context for ContextTenantPolicy.
func WithTenantID(ctx context.Context, tenantID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, tenantIDContextKey, tenantID)
}

// TenantIDFromContext returns the tenant identity stored in context.
func TenantIDFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	tenantID, ok := ctx.Value(tenantIDContextKey).(string)
	if !ok || tenantID == "" {
		return "", false
	}
	return tenantID, true
}
