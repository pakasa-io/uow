package uow

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// Registry stores adapter/client registrations used by the resolver.
//
// Reads are safe for concurrent use. Writes are serialized.
type Registry struct {
	mu    sync.RWMutex
	items map[string]Registration
	list  []Registration
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		items: make(map[string]Registration),
	}
}

// Register registers one adapter/client binding.
func (r *Registry) Register(reg Registration) error {
	if r == nil {
		return classifyError(ErrKindConfig, errNilRegistry)
	}
	normalized, err := normalizeRegistration(reg)
	if err != nil {
		return err
	}
	key := registrationKey(normalized.AdapterName, normalized.ClientName, normalized.TenantID)

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.items[key]; exists {
		return classifyError(ErrKindConfig, fmt.Errorf("uow: duplicate registration for adapter=%q client=%q tenant=%q", normalized.AdapterName, normalized.ClientName, normalized.TenantID))
	}
	r.items[key] = normalized
	r.list = append(r.list, normalized)
	return nil
}

// MustRegister registers a binding and panics on error.
func (r *Registry) MustRegister(reg Registration) {
	if err := r.Register(reg); err != nil {
		panic(err)
	}
}

// Registrations returns a snapshot of the current registrations.
func (r *Registry) Registrations() []Registration {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Registration, len(r.list))
	copy(out, r.list)
	return out
}

func normalizeRegistration(reg Registration) (Registration, error) {
	reg.AdapterName = strings.TrimSpace(reg.AdapterName)
	reg.ClientName = strings.TrimSpace(reg.ClientName)
	reg.TenantID = strings.TrimSpace(reg.TenantID)

	if reg.Adapter == nil {
		return Registration{}, classifyError(ErrKindConfig, fmt.Errorf("uow: registration adapter is nil"))
	}
	if isNilValue(reg.Client) {
		return Registration{}, classifyError(ErrKindConfig, fmt.Errorf("uow: registration client is nil"))
	}
	adapterName := strings.TrimSpace(reg.Adapter.Name())
	if adapterName == "" {
		return Registration{}, classifyError(ErrKindConfig, fmt.Errorf("uow: adapter returned empty name"))
	}
	if reg.AdapterName == "" {
		reg.AdapterName = adapterName
	} else if reg.AdapterName != adapterName {
		return Registration{}, classifyError(ErrKindConfig, fmt.Errorf("uow: registration adapter name %q does not match adapter.Name() %q", reg.AdapterName, adapterName))
	}
	if reg.ClientName == "" {
		return Registration{}, classifyError(ErrKindConfig, fmt.Errorf("uow: registration client name is required"))
	}
	if reg.Tags != nil {
		copied := make(map[string]string, len(reg.Tags))
		for key, value := range reg.Tags {
			copied[key] = value
		}
		reg.Tags = copied
	}
	return reg, nil
}

func registrationKey(adapterName, clientName, tenantID string) string {
	return adapterName + "\x00" + clientName + "\x00" + tenantID
}

func isNilValue(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}
