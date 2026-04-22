package uow

import (
	"strings"
	"time"
)

type sharedConfig struct {
	AdapterName    Selector
	ClientName     Selector
	TenantID       Selector
	ReadOnly       bool
	IsolationLevel IsolationLevel
	Timeout        time.Duration
	Label          string
}

func (c ExecutionConfig) shared() sharedConfig {
	return sharedConfig{
		AdapterName:    c.AdapterName,
		ClientName:     c.ClientName,
		TenantID:       c.TenantID,
		ReadOnly:       c.ReadOnly,
		IsolationLevel: c.IsolationLevel,
		Timeout:        c.Timeout,
		Label:          c.Label,
	}
}

func (c TxConfig) shared() sharedConfig {
	return sharedConfig{
		AdapterName:    c.AdapterName,
		ClientName:     c.ClientName,
		TenantID:       c.TenantID,
		ReadOnly:       c.ReadOnly,
		IsolationLevel: c.IsolationLevel,
		Timeout:        c.Timeout,
		Label:          c.Label,
	}
}

func (c sharedConfig) executionConfig(transactional TransactionalMode) ExecutionConfig {
	return ExecutionConfig{
		AdapterName:    c.AdapterName,
		ClientName:     c.ClientName,
		TenantID:       c.TenantID,
		Transactional:  transactional,
		ReadOnly:       c.ReadOnly,
		IsolationLevel: c.IsolationLevel,
		Timeout:        c.Timeout,
		Label:          c.Label,
	}
}

func (c sharedConfig) txConfig() TxConfig {
	return TxConfig{
		AdapterName:    c.AdapterName,
		ClientName:     c.ClientName,
		TenantID:       c.TenantID,
		ReadOnly:       c.ReadOnly,
		IsolationLevel: c.IsolationLevel,
		Timeout:        c.Timeout,
		Label:          c.Label,
	}
}

func (c sharedConfig) resolutionRequest(mode ResolutionMode) ResolutionRequest {
	return ResolutionRequest{
		Mode:        mode,
		AdapterName: c.AdapterName,
		ClientName:  c.ClientName,
		TenantID:    c.TenantID,
	}
}

func (c sharedConfig) beginOptions() BeginOptions {
	return BeginOptions{
		ReadOnly:       c.ReadOnly,
		IsolationLevel: c.IsolationLevel,
		Timeout:        c.Timeout,
		Label:          c.Label,
	}
}

func (c sharedConfig) validate() error {
	return validateSharedTxOptions(c.IsolationLevel, c.Timeout)
}

// TxOption configures shared transactional settings for RootTx(...).
type TxOption interface {
	applyTx(*TxConfig)
}

// ExecOption configures ambient execution settings for Exec(...).
type ExecOption interface {
	applyExec(*ExecutionConfig)
}

// Option configures shared fields supported by both Exec(...) and RootTx(...).
type Option interface {
	TxOption
	ExecOption
}

type sharedOption struct {
	apply func(*sharedConfig)
}

func (o sharedOption) applyTx(cfg *TxConfig) {
	if o.apply == nil {
		return
	}
	shared := cfg.shared()
	o.apply(&shared)
	*cfg = shared.txConfig()
}

func (o sharedOption) applyExec(cfg *ExecutionConfig) {
	if o.apply == nil {
		return
	}
	shared := cfg.shared()
	transactional := cfg.Transactional
	o.apply(&shared)
	*cfg = shared.executionConfig(transactional)
}

type executionOption struct {
	apply func(*ExecutionConfig)
}

func (o executionOption) applyExec(cfg *ExecutionConfig) {
	if o.apply != nil {
		o.apply(cfg)
	}
}

// Exec builds an ExecutionConfig from additive option helpers.
func Exec(opts ...ExecOption) ExecutionConfig {
	var cfg ExecutionConfig
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		opt.applyExec(&cfg)
	}
	return cfg
}

// RootTx builds a TxConfig from additive option helpers.
func RootTx(opts ...TxOption) TxConfig {
	var cfg TxConfig
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		opt.applyTx(&cfg)
	}
	return cfg
}

// TxConfigFromExecution copies the shared transaction fields from an
// ExecutionConfig into a TxConfig after validating the ambient config.
func TxConfigFromExecution(cfg ExecutionConfig) (TxConfig, error) {
	if err := cfg.Validate(); err != nil {
		return TxConfig{}, err
	}
	return cfg.shared().txConfig(), nil
}

// WithAdapter selects a concrete adapter name for Exec(...) or RootTx(...).
func WithAdapter(name string) Option {
	return WithAdapterSelector(SelectAdapter(name))
}

// WithAdapterSelector applies an adapter selector to Exec(...) or RootTx(...).
func WithAdapterSelector(selector Selector) Option {
	return sharedOption{
		apply: func(cfg *sharedConfig) {
			cfg.AdapterName = normalizeSelector(selector)
		},
	}
}

// WithClient selects a concrete client name for Exec(...) or RootTx(...).
func WithClient(name string) Option {
	return WithClientSelector(SelectClient(name))
}

// WithClientSelector applies a client selector to Exec(...) or RootTx(...).
func WithClientSelector(selector Selector) Option {
	return sharedOption{
		apply: func(cfg *sharedConfig) {
			cfg.ClientName = normalizeSelector(selector)
		},
	}
}

// WithTenant selects a concrete tenant id for Exec(...) or RootTx(...).
func WithTenant(id string) Option {
	return WithTenantSelector(SelectTenant(id))
}

// WithTenantSelector applies a tenant selector to Exec(...) or RootTx(...).
func WithTenantSelector(selector Selector) Option {
	return sharedOption{
		apply: func(cfg *sharedConfig) {
			cfg.TenantID = normalizeSelector(selector)
		},
	}
}

// WithReadOnly requests a read-only root transaction.
func WithReadOnly() Option {
	return sharedOption{
		apply: func(cfg *sharedConfig) {
			cfg.ReadOnly = true
		},
	}
}

// WithIsolation requests a specific transaction isolation level.
func WithIsolation(level IsolationLevel) Option {
	return sharedOption{
		apply: func(cfg *sharedConfig) {
			cfg.IsolationLevel = level
		},
	}
}

// WithTimeout requests a transaction timeout.
func WithTimeout(timeout time.Duration) Option {
	return sharedOption{
		apply: func(cfg *sharedConfig) {
			cfg.Timeout = timeout
		},
	}
}

// WithLabel applies an execution label for observability.
func WithLabel(label string) Option {
	return sharedOption{
		apply: func(cfg *sharedConfig) {
			cfg.Label = strings.TrimSpace(label)
		},
	}
}

// WithTransactional configures ambient transaction behavior for Exec(...).
func WithTransactional(mode TransactionalMode) ExecOption {
	return executionOption{
		apply: func(cfg *ExecutionConfig) {
			cfg.Transactional = mode
		},
	}
}

func normalizeSelector(selector Selector) Selector {
	if !selector.Set {
		return Selector{}
	}
	return Selector{
		Set:   true,
		Value: strings.TrimSpace(selector.Value),
	}
}
