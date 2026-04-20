package uow

import (
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrNoAdapterRegistered indicates that the registry is empty.
	ErrNoAdapterRegistered = errors.New("uow: no adapter registered")
	// ErrAdapterNotFound indicates that a requested adapter could not be resolved.
	ErrAdapterNotFound = errors.New("uow: adapter not found")
	// ErrClientNotFound indicates that a requested client could not be resolved.
	ErrClientNotFound = errors.New("uow: client not found")
	// ErrTenantNotResolved indicates that a required tenant was not available.
	ErrTenantNotResolved = errors.New("uow: tenant not resolved")
	// ErrTenantBindingNotFound indicates that a tenant-specific binding could not
	// be resolved.
	ErrTenantBindingNotFound = errors.New("uow: tenant binding not found")
	// ErrUOWNotFound indicates that no UnitOfWork is present in context.
	ErrUOWNotFound = errors.New("uow: unit of work not found")
	// ErrTxAlreadyClosed indicates that a scope was already finalized.
	ErrTxAlreadyClosed = errors.New("uow: transaction already closed")
	// ErrNestedTxUnsupported indicates that strict nested transactions are
	// unsupported by the selected adapter.
	ErrNestedTxUnsupported = errors.New("uow: nested transaction unsupported")
	// ErrRootTxUnsupported indicates that root transactions are unsupported by the
	// selected adapter.
	ErrRootTxUnsupported = errors.New("uow: root transaction unsupported")
	// ErrBeginAborted indicates that begin was vetoed before the adapter call.
	ErrBeginAborted = errors.New("uow: begin aborted")
	// ErrBeginFailed indicates that begin failed after the adapter call path was
	// entered.
	ErrBeginFailed = errors.New("uow: begin failed")
	// ErrCommitAborted indicates that commit failed and rollback succeeded.
	ErrCommitAborted = errors.New("uow: commit aborted")
	// ErrRollbackFailed indicates that rollback itself failed.
	ErrRollbackFailed = errors.New("uow: rollback failed")
	// ErrFinalizationFailed indicates that multiple finalization failures need to
	// be preserved together.
	ErrFinalizationFailed = errors.New("uow: finalization failed")
	// ErrBindingOverrideConflict indicates conflicting explicit and context
	// selectors.
	ErrBindingOverrideConflict = errors.New("uow: binding override conflict")
	// ErrRootOwnershipViolation indicates an attempt to control a root
	// transaction outside the owning lifecycle.
	ErrRootOwnershipViolation = errors.New("uow: root ownership violation")
	// ErrNoActiveTransaction indicates that no root transaction is active.
	ErrNoActiveTransaction = errors.New("uow: no active transaction")
	// ErrRollbackOnly indicates that commit is blocked by rollback-only state.
	ErrRollbackOnly = errors.New("uow: rollback only")
	// ErrBindingImmutable indicates that a UnitOfWork binding cannot change.
	ErrBindingImmutable = errors.New("uow: binding immutable")
	// ErrMultipleRootBindingsForbidden indicates that a single execution cannot
	// open multiple root bindings.
	ErrMultipleRootBindingsForbidden = errors.New("uow: multiple root bindings forbidden")
	// ErrScopeOrderViolation indicates nested scope finalization out of order.
	ErrScopeOrderViolation = errors.New("uow: scope order violation")
	// ErrContextCancelled indicates that execution was cancelled before commit.
	ErrContextCancelled = errors.New("uow: context cancelled")
)

var (
	errNilRegistry             = errors.New("uow: nil registry")
	errNilCallback             = errors.New("uow: nil callback")
	errManualRollback          = errors.New("uow: manual rollback requested")
	errPolicyRequestedRollback = errors.New("uow: finalize policy requested rollback")
)

// ErrorKind categorizes package errors for programmatic inspection.
type ErrorKind int

const (
	// ErrKindConfig indicates invalid configuration or input.
	ErrKindConfig ErrorKind = iota
	// ErrKindAdapter indicates backend adapter capability or lifecycle failures.
	ErrKindAdapter
	// ErrKindResolver indicates binding resolution failures.
	ErrKindResolver
	// ErrKindTransaction indicates transaction lifecycle failures.
	ErrKindTransaction
	// ErrKindState indicates invalid UnitOfWork state transitions.
	ErrKindState
	// ErrKindTenant indicates tenant resolution failures.
	ErrKindTenant
)

func (k ErrorKind) String() string {
	switch k {
	case ErrKindConfig:
		return "config"
	case ErrKindAdapter:
		return "adapter"
	case ErrKindResolver:
		return "resolver"
	case ErrKindTransaction:
		return "transaction"
	case ErrKindState:
		return "state"
	case ErrKindTenant:
		return "tenant"
	default:
		return fmt.Sprintf("unknown(%d)", int(k))
	}
}

// UOWError wraps a categorized package error.
type UOWError struct {
	Kind ErrorKind
	Err  error
}

// Error implements error.
func (e *UOWError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err == nil {
		return e.Kind.String()
	}
	return e.Err.Error()
}

// Unwrap exposes the underlying error chain.
func (e *UOWError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type compositeError struct {
	primary error
	others  []error
}

func (e *compositeError) Error() string {
	parts := make([]string, 0, 1+len(e.others))
	if e.primary != nil {
		parts = append(parts, e.primary.Error())
	}
	for _, err := range e.others {
		if err != nil {
			parts = append(parts, err.Error())
		}
	}
	return strings.Join(parts, "; ")
}

func (e *compositeError) Unwrap() []error {
	errs := make([]error, 0, 1+len(e.others))
	if e.primary != nil {
		errs = append(errs, e.primary)
	}
	for _, err := range e.others {
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

func classifyError(kind ErrorKind, err error) error {
	if err == nil {
		return nil
	}
	var uerr *UOWError
	if errors.As(err, &uerr) {
		return err
	}
	return &UOWError{Kind: kind, Err: err}
}

func withSentinel(kind ErrorKind, sentinel error, causes ...error) error {
	return classifyError(kind, composeErrors(sentinel, causes...))
}

func composeErrors(primary error, others ...error) error {
	filtered := make([]error, 0, len(others))
	for _, err := range others {
		if err != nil {
			filtered = append(filtered, err)
		}
	}
	if primary == nil {
		switch len(filtered) {
		case 0:
			return nil
		case 1:
			return filtered[0]
		default:
			return &compositeError{primary: filtered[0], others: filtered[1:]}
		}
	}
	if len(filtered) == 0 {
		return primary
	}
	return &compositeError{primary: primary, others: filtered}
}
