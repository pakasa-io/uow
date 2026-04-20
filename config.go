package uow

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config controls resolver and transaction behavior.
type Config struct {
	NestedMode              NestedMode
	TransactionMode         TransactionMode
	DefaultAdapterName      string
	DefaultClientName       string
	DefaultFinalizePolicy   FinalizePolicy
	StrictOptionEnforcement bool
	AllowOptionDowngrade    bool
	RequireTenantResolution bool
}

// DefaultConfig returns the package defaults defined by the specification.
func DefaultConfig() Config {
	return Config{
		NestedMode:              NestedStrict,
		TransactionMode:         ExplicitOnly,
		StrictOptionEnforcement: true,
		AllowOptionDowngrade:    false,
		RequireTenantResolution: false,
	}
}

// Validate validates the configuration after default normalization.
func (c Config) Validate() error {
	cfg := c.normalized()
	switch cfg.NestedMode {
	case NestedStrict, NestedEmulated:
	default:
		return classifyError(ErrKindConfig, fmt.Errorf("uow: invalid nested mode %d", cfg.NestedMode))
	}
	switch cfg.TransactionMode {
	case ExplicitOnly, GlobalAuto:
	default:
		return classifyError(ErrKindConfig, fmt.Errorf("uow: invalid transaction mode %d", cfg.TransactionMode))
	}
	if cfg.AllowOptionDowngrade && cfg.StrictOptionEnforcement {
		return classifyError(ErrKindConfig, fmt.Errorf("uow: allow option downgrade requires non-strict option enforcement"))
	}
	return nil
}

func (c Config) normalized() Config {
	cfg := DefaultConfig()
	cfg.NestedMode = c.NestedMode
	cfg.TransactionMode = c.TransactionMode
	cfg.DefaultAdapterName = strings.TrimSpace(c.DefaultAdapterName)
	cfg.DefaultClientName = strings.TrimSpace(c.DefaultClientName)
	cfg.DefaultFinalizePolicy = c.DefaultFinalizePolicy
	if c.StrictOptionEnforcement {
		cfg.StrictOptionEnforcement = true
	}
	if c.AllowOptionDowngrade {
		cfg.AllowOptionDowngrade = true
		cfg.StrictOptionEnforcement = false
	}
	if c.RequireTenantResolution {
		cfg.RequireTenantResolution = true
	}
	return cfg
}

// ConfigFromEnv loads Config from environment variables using the provided
// prefix. The prefix defaults to UOW when empty.
//
// Supported keys are:
//
//   - <PREFIX>_NESTED_MODE
//   - <PREFIX>_TRANSACTION_MODE
//   - <PREFIX>_DEFAULT_ADAPTER_NAME
//   - <PREFIX>_DEFAULT_CLIENT_NAME
//   - <PREFIX>_STRICT_OPTION_ENFORCEMENT
//   - <PREFIX>_ALLOW_OPTION_DOWNGRADE
//   - <PREFIX>_REQUIRE_TENANT_RESOLUTION
//   - <PREFIX>_DEFAULT_FINALIZE_POLICY (supports only "default")
func ConfigFromEnv(prefix string) (Config, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "UOW"
	}
	prefix = strings.TrimSuffix(strings.ToUpper(prefix), "_")
	key := func(name string) string {
		return prefix + "_" + name
	}

	cfg := DefaultConfig()
	if value, ok := os.LookupEnv(key("NESTED_MODE")); ok {
		mode, err := ParseNestedMode(value)
		if err != nil {
			return Config{}, err
		}
		cfg.NestedMode = mode
	}
	if value, ok := os.LookupEnv(key("TRANSACTION_MODE")); ok {
		mode, err := ParseTransactionMode(value)
		if err != nil {
			return Config{}, err
		}
		cfg.TransactionMode = mode
	}
	if value, ok := os.LookupEnv(key("DEFAULT_ADAPTER_NAME")); ok {
		cfg.DefaultAdapterName = strings.TrimSpace(value)
	}
	if value, ok := os.LookupEnv(key("DEFAULT_CLIENT_NAME")); ok {
		cfg.DefaultClientName = strings.TrimSpace(value)
	}
	if value, ok := os.LookupEnv(key("STRICT_OPTION_ENFORCEMENT")); ok {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, classifyError(ErrKindConfig, fmt.Errorf("uow: parse %s: %w", key("STRICT_OPTION_ENFORCEMENT"), err))
		}
		cfg.StrictOptionEnforcement = parsed
	}
	if value, ok := os.LookupEnv(key("ALLOW_OPTION_DOWNGRADE")); ok {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, classifyError(ErrKindConfig, fmt.Errorf("uow: parse %s: %w", key("ALLOW_OPTION_DOWNGRADE"), err))
		}
		cfg.AllowOptionDowngrade = parsed
		if parsed {
			cfg.StrictOptionEnforcement = false
		}
	}
	if value, ok := os.LookupEnv(key("REQUIRE_TENANT_RESOLUTION")); ok {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, classifyError(ErrKindConfig, fmt.Errorf("uow: parse %s: %w", key("REQUIRE_TENANT_RESOLUTION"), err))
		}
		cfg.RequireTenantResolution = parsed
	}
	if value, ok := os.LookupEnv(key("DEFAULT_FINALIZE_POLICY")); ok {
		value = strings.TrimSpace(strings.ToLower(value))
		if value != "" && value != "default" {
			return Config{}, classifyError(ErrKindConfig, fmt.Errorf("uow: unsupported %s value %q", key("DEFAULT_FINALIZE_POLICY"), value))
		}
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// ParseNestedMode parses a string representation of NestedMode.
func ParseNestedMode(value string) (NestedMode, error) {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "strict", "nested_strict":
		return NestedStrict, nil
	case "emulated", "nested_emulated":
		return NestedEmulated, nil
	default:
		return NestedStrict, classifyError(ErrKindConfig, fmt.Errorf("uow: invalid nested mode %q", value))
	}
}

// String implements fmt.Stringer.
func (m NestedMode) String() string {
	switch m {
	case NestedStrict:
		return "strict"
	case NestedEmulated:
		return "emulated"
	default:
		return fmt.Sprintf("unknown(%d)", int(m))
	}
}

// ParseTransactionMode parses a string representation of TransactionMode.
func ParseTransactionMode(value string) (TransactionMode, error) {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "explicit_only", "explicit":
		return ExplicitOnly, nil
	case "global_auto", "auto", "global":
		return GlobalAuto, nil
	default:
		return ExplicitOnly, classifyError(ErrKindConfig, fmt.Errorf("uow: invalid transaction mode %q", value))
	}
}

// String implements fmt.Stringer.
func (m TransactionMode) String() string {
	switch m {
	case ExplicitOnly:
		return "explicit_only"
	case GlobalAuto:
		return "global_auto"
	default:
		return fmt.Sprintf("unknown(%d)", int(m))
	}
}

// String implements fmt.Stringer.
func (m TransactionalMode) String() string {
	switch m {
	case TransactionalInherit:
		return "inherit"
	case TransactionalOff:
		return "off"
	case TransactionalOn:
		return "on"
	default:
		return fmt.Sprintf("unknown(%d)", int(m))
	}
}
