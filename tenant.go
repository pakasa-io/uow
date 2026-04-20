package uow

import "context"

// ContextTenantPolicy resolves the tenant identity from WithTenantID.
type ContextTenantPolicy struct{}

// ResolveTenant implements TenantResolutionPolicy.
func (ContextTenantPolicy) ResolveTenant(ctx context.Context) (string, error) {
	tenantID, _ := TenantIDFromContext(ctx)
	return tenantID, nil
}
