package uow

import (
	"context"
	"fmt"
	"strings"
)

type resolvedField struct {
	locked bool
	value  string
}

type tenantChoice struct {
	value        string
	explicitNone bool
	explicit     bool
	derived      bool
}

type resolvedSelectors struct {
	adapter resolvedField
	client  resolvedField
	tenant  resolvedField
}

func mergeSelectors(req ResolutionRequest, override BindingOverride, cfg Config) (resolvedSelectors, error) {
	var selectors resolvedSelectors
	var err error

	switch req.Mode {
	case ResolutionAmbient:
		selectors.adapter = mergeAmbientField(override.AdapterName, req.AdapterName, cfg.DefaultAdapterName)
		selectors.client = mergeAmbientField(override.ClientName, req.ClientName, cfg.DefaultClientName)
		selectors.tenant = mergeAmbientField(override.TenantID, req.TenantID, "")
	case ResolutionExplicit:
		if selectors.adapter, err = mergeExplicitField(req.AdapterName, override.AdapterName, cfg.DefaultAdapterName, "adapter"); err != nil {
			return resolvedSelectors{}, err
		}
		if selectors.client, err = mergeExplicitField(req.ClientName, override.ClientName, cfg.DefaultClientName, "client"); err != nil {
			return resolvedSelectors{}, err
		}
		if selectors.tenant, err = mergeExplicitField(req.TenantID, override.TenantID, "", "tenant"); err != nil {
			return resolvedSelectors{}, err
		}
	default:
		return resolvedSelectors{}, classifyError(ErrKindResolver, fmt.Errorf("uow: invalid resolution mode %d", req.Mode))
	}
	return selectors, nil
}

func mergeAmbientField(override Selector, request Selector, configuredDefault string) resolvedField {
	field := resolvedField{}
	switch {
	case override.Set:
		field.locked = true
		field.value = strings.TrimSpace(override.Value)
	case request.Set:
		field.locked = true
		field.value = strings.TrimSpace(request.Value)
	}
	if field.value == "" {
		field.value = strings.TrimSpace(configuredDefault)
	}
	return field
}

func mergeExplicitField(request Selector, override Selector, configuredDefault, fieldName string) (resolvedField, error) {
	field := resolvedField{}
	if request.Set {
		requestValue := strings.TrimSpace(request.Value)
		if override.Set {
			overrideValue := strings.TrimSpace(override.Value)
			if requestValue != overrideValue {
				return resolvedField{}, withSentinel(ErrKindResolver, ErrBindingOverrideConflict, fmt.Errorf("uow: explicit %s selector %q conflicts with binding override %q", fieldName, requestValue, overrideValue))
			}
		}
		field.locked = true
		field.value = requestValue
	} else if override.Set {
		field.locked = true
		field.value = strings.TrimSpace(override.Value)
	}
	if field.value == "" {
		field.value = strings.TrimSpace(configuredDefault)
	}
	return field, nil
}

func (m *Manager) resolveTenant(ctx context.Context, field resolvedField) (tenantChoice, error) {
	if field.locked {
		if field.value == "" {
			if m.cfg.RequireTenantResolution {
				return tenantChoice{}, withSentinel(ErrKindTenant, ErrTenantNotResolved)
			}
			return tenantChoice{explicitNone: true, explicit: true}, nil
		}
		return tenantChoice{value: field.value, explicit: true}, nil
	}
	if m.opts.TenantPolicy != nil {
		tenantID, err := m.opts.TenantPolicy.ResolveTenant(ctx)
		if err != nil {
			return tenantChoice{}, classifyError(ErrKindTenant, fmt.Errorf("uow: resolve tenant: %w", err))
		}
		tenantID = strings.TrimSpace(tenantID)
		if tenantID != "" {
			return tenantChoice{value: tenantID, derived: true}, nil
		}
	}
	if m.cfg.RequireTenantResolution {
		return tenantChoice{}, withSentinel(ErrKindTenant, ErrTenantNotResolved)
	}
	return tenantChoice{}, nil
}

func filterByAdapter(regs []Registration, adapterName string) []Registration {
	if adapterName == "" {
		out := make([]Registration, len(regs))
		copy(out, regs)
		return out
	}
	filtered := make([]Registration, 0, len(regs))
	for _, reg := range regs {
		if reg.AdapterName == adapterName {
			filtered = append(filtered, reg)
		}
	}
	return filtered
}

func filterByClient(regs []Registration, clientName string) []Registration {
	if clientName == "" {
		out := make([]Registration, len(regs))
		copy(out, regs)
		return out
	}
	filtered := make([]Registration, 0, len(regs))
	for _, reg := range regs {
		if reg.ClientName == clientName {
			filtered = append(filtered, reg)
		}
	}
	return filtered
}

func splitTenantCandidates(regs []Registration, tenant tenantChoice) (exact []Registration, global []Registration) {
	exact = make([]Registration, 0, len(regs))
	global = make([]Registration, 0, len(regs))
	for _, reg := range regs {
		switch {
		case tenant.value != "" && reg.TenantID == tenant.value:
			exact = append(exact, reg)
		case reg.TenantID == "":
			global = append(global, reg)
		}
	}
	return exact, global
}

func chooseRegistration(candidates []Registration, selector resolvedSelectors) (Registration, error) {
	if len(candidates) == 0 {
		return Registration{}, nil
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	defaults := make([]Registration, 0, len(candidates))
	for _, reg := range candidates {
		if reg.Default {
			defaults = append(defaults, reg)
		}
	}
	if len(defaults) == 1 {
		return defaults[0], nil
	}

	adapters := make(map[string]struct{}, len(candidates))
	clients := make(map[string]struct{}, len(candidates))
	for _, reg := range candidates {
		adapters[reg.AdapterName] = struct{}{}
		clients[reg.ClientName] = struct{}{}
	}
	if selector.adapter.value == "" && len(adapters) > 1 {
		return Registration{}, withSentinel(ErrKindResolver, ErrAdapterNotFound, fmt.Errorf("uow: binding resolution is ambiguous across %d adapters; configure DefaultAdapterName or mark one registration default", len(adapters)))
	}
	if selector.client.value == "" && len(clients) > 1 {
		return Registration{}, withSentinel(ErrKindResolver, ErrClientNotFound, fmt.Errorf("uow: binding resolution is ambiguous across %d clients; configure DefaultClientName or mark one registration default", len(clients)))
	}
	return candidates[0], nil
}
