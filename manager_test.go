package uow

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
)

type mockTx struct {
	name  string
	depth int
	raw   string
}

func (t *mockTx) Name() string { return t.name }
func (t *mockTx) Depth() int   { return t.depth }
func (t *mockTx) Raw() any     { return t.raw }

type mockAdapter struct {
	name string
	caps Capabilities

	mu            sync.Mutex
	next          int
	beginCalls    []BeginOptions
	nestedCalls   []NestedOptions
	commits       []string
	rollbacks     []string
	beginFn       func(context.Context, any, BeginOptions) (Tx, error)
	beginNestedFn func(context.Context, Tx, NestedOptions) (Tx, error)
	commitFn      func(context.Context, Tx) error
	rollbackFn    func(context.Context, Tx) error
}

func newMockAdapter(caps Capabilities) *mockAdapter {
	return &mockAdapter{name: "mock", caps: caps}
}

func (a *mockAdapter) Name() string               { return a.name }
func (a *mockAdapter) Capabilities() Capabilities { return a.caps }

func (a *mockAdapter) Begin(ctx context.Context, client any, opts BeginOptions) (Tx, error) {
	a.mu.Lock()
	a.beginCalls = append(a.beginCalls, opts)
	fn := a.beginFn
	a.next++
	index := a.next
	a.mu.Unlock()
	if fn != nil {
		return fn(ctx, client, opts)
	}
	return &mockTx{name: fmt.Sprintf("root-%d", index), depth: 0, raw: fmt.Sprintf("handle-root-%d", index)}, nil
}

func (a *mockAdapter) BeginNested(ctx context.Context, parent Tx, opts NestedOptions) (Tx, error) {
	a.mu.Lock()
	a.nestedCalls = append(a.nestedCalls, opts)
	fn := a.beginNestedFn
	a.next++
	index := a.next
	a.mu.Unlock()
	if fn != nil {
		return fn(ctx, parent, opts)
	}
	return &mockTx{name: fmt.Sprintf("nested-%d", index), depth: parent.Depth() + 1, raw: fmt.Sprintf("handle-nested-%d", index)}, nil
}

func (a *mockAdapter) Commit(ctx context.Context, tx Tx) error {
	a.mu.Lock()
	a.commits = append(a.commits, tx.Name())
	fn := a.commitFn
	a.mu.Unlock()
	if fn != nil {
		return fn(ctx, tx)
	}
	return nil
}

func (a *mockAdapter) Rollback(ctx context.Context, tx Tx) error {
	a.mu.Lock()
	a.rollbacks = append(a.rollbacks, tx.Name())
	fn := a.rollbackFn
	a.mu.Unlock()
	if fn != nil {
		return fn(ctx, tx)
	}
	return nil
}

func (a *mockAdapter) Unwrap(tx Tx) any { return tx.Raw() }

type recordingInterceptor struct {
	events            *[]string
	beforeBeginErr    error
	beforeRollbackErr error
}

func (i recordingInterceptor) BeforeBegin(_ context.Context, _ TxMeta) error {
	*i.events = append(*i.events, "before_begin")
	return i.beforeBeginErr
}
func (i recordingInterceptor) AfterBegin(_ context.Context, _ TxMeta, err error) {
	if err != nil {
		*i.events = append(*i.events, "after_begin:error")
		return
	}
	*i.events = append(*i.events, "after_begin")
}
func (i recordingInterceptor) BeforeCommit(_ context.Context, _ TxMeta) error {
	*i.events = append(*i.events, "before_commit")
	return nil
}
func (i recordingInterceptor) AfterCommit(_ context.Context, _ TxMeta, err error) {
	if err != nil {
		*i.events = append(*i.events, "after_commit:error")
		return
	}
	*i.events = append(*i.events, "after_commit")
}
func (i recordingInterceptor) BeforeRollback(_ context.Context, _ TxMeta) error {
	*i.events = append(*i.events, "before_rollback")
	return i.beforeRollbackErr
}
func (i recordingInterceptor) AfterRollback(_ context.Context, _ TxMeta, err error) {
	if err != nil {
		*i.events = append(*i.events, "after_rollback:error")
		return
	}
	*i.events = append(*i.events, "after_rollback")
}

type recordingHooks struct {
	events *[]string
	metas  *[]TxMeta
}

func (h recordingHooks) OnBegin(_ context.Context, meta TxMeta) {
	*h.events = append(*h.events, "hook_begin")
	*h.metas = append(*h.metas, meta)
}
func (h recordingHooks) OnCommit(_ context.Context, meta TxMeta, err error) {
	if err != nil {
		*h.events = append(*h.events, "hook_commit:error")
	} else {
		*h.events = append(*h.events, "hook_commit")
	}
	*h.metas = append(*h.metas, meta)
}
func (h recordingHooks) OnRollback(_ context.Context, meta TxMeta, err error) {
	if err != nil {
		*h.events = append(*h.events, "hook_rollback:error")
	} else {
		*h.events = append(*h.events, "hook_rollback")
	}
	*h.metas = append(*h.metas, meta)
}
func (h recordingHooks) OnNestedBegin(_ context.Context, meta TxMeta) {
	*h.events = append(*h.events, "hook_nested_begin")
	*h.metas = append(*h.metas, meta)
}

func mustManager(t *testing.T, cfg Config, opts ManagerOptions, regs ...Registration) *Manager {
	t.Helper()
	registry := NewRegistry()
	for _, reg := range regs {
		if err := registry.Register(reg); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	manager, err := NewManager(registry, cfg, opts)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	return manager
}

func defaultRegistration(adapter Adapter) Registration {
	return Registration{
		Adapter:     adapter,
		Client:      "primary-client",
		ClientName:  "primary",
		Default:     true,
		AdapterName: adapter.Name(),
	}
}

func TestInTxCommitsAndPropagatesUnitOfWork(t *testing.T) {
	adapter := newMockAdapter(Capabilities{
		RootTransaction:   true,
		NestedTransaction: true,
		Savepoints:        true,
		ReadOnlyTx:        true,
		IsolationLevels:   true,
		Timeouts:          true,
	})
	manager := mustManager(t, DefaultConfig(), ManagerOptions{}, defaultRegistration(adapter))

	err := manager.InTx(context.Background(), TxConfig{}, func(ctx context.Context) error {
		u := MustFrom(ctx)
		if !u.InTransaction() {
			t.Fatalf("expected transaction")
		}
		if got := u.Binding(); got.ClientName != "primary" || got.AdapterName != "mock" {
			t.Fatalf("unexpected binding: %+v", got)
		}
		root, ok := u.Root()
		if !ok || root.Depth() != 0 {
			t.Fatalf("unexpected root: %#v %v", root, ok)
		}
		current, ok := u.Current()
		if !ok || current.Depth() != 0 {
			t.Fatalf("unexpected current: %#v %v", current, ok)
		}
		if handle := u.CurrentHandle(); handle == nil {
			t.Fatalf("expected current handle")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("InTx: %v", err)
	}
	if len(adapter.beginCalls) != 1 {
		t.Fatalf("expected 1 begin, got %d", len(adapter.beginCalls))
	}
	if len(adapter.commits) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(adapter.commits))
	}
	if len(adapter.rollbacks) != 0 {
		t.Fatalf("expected no rollback, got %d", len(adapter.rollbacks))
	}
}

func TestBeginVetoSkipsCallback(t *testing.T) {
	adapter := newMockAdapter(Capabilities{RootTransaction: true})
	events := []string{}
	veto := errors.New("no begin")
	manager := mustManager(t, DefaultConfig(), ManagerOptions{
		Interceptors: []Interceptor{recordingInterceptor{events: &events, beforeBeginErr: veto}},
	}, defaultRegistration(adapter))

	called := false
	err := manager.InTx(context.Background(), TxConfig{}, func(ctx context.Context) error {
		called = true
		return nil
	})
	if !errors.Is(err, ErrBeginAborted) || !errors.Is(err, veto) {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Fatalf("callback should not run")
	}
	if got := events; !slices.Equal(got, []string{"before_begin", "after_begin:error"}) {
		t.Fatalf("unexpected events: %v", got)
	}
}

func TestCallbackErrorRollsBack(t *testing.T) {
	adapter := newMockAdapter(Capabilities{RootTransaction: true})
	manager := mustManager(t, DefaultConfig(), ManagerOptions{}, defaultRegistration(adapter))
	workErr := errors.New("work failed")

	err := manager.InTx(context.Background(), TxConfig{}, func(ctx context.Context) error {
		return workErr
	})
	if !errors.Is(err, workErr) {
		t.Fatalf("expected callback error, got %v", err)
	}
	if len(adapter.rollbacks) != 1 {
		t.Fatalf("expected rollback, got %v", adapter.rollbacks)
	}
	if len(adapter.commits) != 0 {
		t.Fatalf("expected no commit, got %v", adapter.commits)
	}
}

func TestPanicRollsBackAndRepanics(t *testing.T) {
	adapter := newMockAdapter(Capabilities{RootTransaction: true})
	manager := mustManager(t, DefaultConfig(), ManagerOptions{}, defaultRegistration(adapter))
	panicValue := "boom"

	defer func() {
		recovered := recover()
		if recovered != panicValue {
			t.Fatalf("unexpected panic value: %v", recovered)
		}
		if len(adapter.rollbacks) != 1 {
			t.Fatalf("expected rollback, got %v", adapter.rollbacks)
		}
	}()

	_ = manager.InTx(context.Background(), TxConfig{}, func(ctx context.Context) error {
		panic(panicValue)
	})
}

func TestCancellationSetsRollbackOnlyAndRollsBack(t *testing.T) {
	adapter := newMockAdapter(Capabilities{RootTransaction: true})
	manager := mustManager(t, DefaultConfig(), ManagerOptions{}, defaultRegistration(adapter))

	ctx, cancel := context.WithCancel(context.Background())
	err := manager.InTx(ctx, TxConfig{}, func(ctx context.Context) error {
		cancel()
		if !MustFrom(ctx).IsRollbackOnly() {
			t.Fatalf("expected rollback-only after cancellation")
		}
		return nil
	})
	if !errors.Is(err, ErrContextCancelled) {
		t.Fatalf("expected context cancellation error, got %v", err)
	}
	if len(adapter.rollbacks) != 1 {
		t.Fatalf("expected rollback, got %v", adapter.rollbacks)
	}
}

func TestCommitFailureTriggersRollback(t *testing.T) {
	commitErr := errors.New("commit failed")
	adapter := newMockAdapter(Capabilities{RootTransaction: true})
	adapter.commitFn = func(context.Context, Tx) error { return commitErr }
	manager := mustManager(t, DefaultConfig(), ManagerOptions{}, defaultRegistration(adapter))

	err := manager.InTx(context.Background(), TxConfig{}, func(ctx context.Context) error { return nil })
	if !errors.Is(err, ErrCommitAborted) || !errors.Is(err, commitErr) {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(adapter.rollbacks) != 1 {
		t.Fatalf("expected compensating rollback, got %v", adapter.rollbacks)
	}
}

func TestCommitFailurePreservesBeforeRollbackError(t *testing.T) {
	commitErr := errors.New("commit failed")
	beforeRollbackErr := errors.New("before rollback failed")
	adapter := newMockAdapter(Capabilities{RootTransaction: true})
	adapter.commitFn = func(context.Context, Tx) error { return commitErr }
	events := []string{}
	manager := mustManager(t, DefaultConfig(), ManagerOptions{
		Interceptors: []Interceptor{recordingInterceptor{
			events:            &events,
			beforeRollbackErr: beforeRollbackErr,
		}},
	}, defaultRegistration(adapter))

	err := manager.InTx(context.Background(), TxConfig{}, func(context.Context) error { return nil })
	if !errors.Is(err, ErrFinalizationFailed) {
		t.Fatalf("expected finalization failure, got %v", err)
	}
	if !errors.Is(err, commitErr) {
		t.Fatalf("expected commit error, got %v", err)
	}
	if !errors.Is(err, beforeRollbackErr) {
		t.Fatalf("expected preserved before rollback error, got %v", err)
	}
	if len(adapter.rollbacks) != 1 {
		t.Fatalf("expected compensating rollback, got %v", adapter.rollbacks)
	}
}

func TestRollbackFailurePreservesCallbackError(t *testing.T) {
	rollbackErr := errors.New("rollback failed")
	adapter := newMockAdapter(Capabilities{RootTransaction: true})
	adapter.rollbackFn = func(context.Context, Tx) error { return rollbackErr }
	manager := mustManager(t, DefaultConfig(), ManagerOptions{}, defaultRegistration(adapter))
	workErr := errors.New("work failed")

	err := manager.InTx(context.Background(), TxConfig{}, func(ctx context.Context) error { return workErr })
	if !errors.Is(err, workErr) || !errors.Is(err, ErrRollbackFailed) || !errors.Is(err, rollbackErr) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStrictNestedUnsupported(t *testing.T) {
	adapter := newMockAdapter(Capabilities{RootTransaction: true})
	manager := mustManager(t, DefaultConfig(), ManagerOptions{}, defaultRegistration(adapter))

	err := manager.InTx(context.Background(), TxConfig{}, func(ctx context.Context) error {
		_, err := MustFrom(ctx).BeginNested(ctx, NestedOptions{})
		if !errors.Is(err, ErrNestedTxUnsupported) {
			t.Fatalf("expected nested unsupported, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("outer transaction failed: %v", err)
	}
}

func TestReentrantInTxUsesNestedScope(t *testing.T) {
	adapter := newMockAdapter(Capabilities{
		RootTransaction:   true,
		NestedTransaction: true,
	})
	manager := mustManager(t, DefaultConfig(), ManagerOptions{}, defaultRegistration(adapter))

	err := manager.InTx(context.Background(), TxConfig{}, func(ctx context.Context) error {
		return manager.InTx(ctx, TxConfig{}, func(ctx context.Context) error {
			current, ok := MustFrom(ctx).Current()
			if !ok || current.Depth() != 1 {
				t.Fatalf("expected nested current depth 1, got %#v %v", current, ok)
			}
			return nil
		})
	})
	if err != nil {
		t.Fatalf("re-entrant InTx: %v", err)
	}
	if len(adapter.nestedCalls) != 1 {
		t.Fatalf("expected nested begin, got %d", len(adapter.nestedCalls))
	}
	if len(adapter.commits) != 2 {
		t.Fatalf("expected nested and root commit, got %v", adapter.commits)
	}
}

func TestNestedEmulatedRollbackMarksRootRollbackOnly(t *testing.T) {
	cfg := DefaultConfig()
	cfg.NestedMode = NestedEmulated
	adapter := newMockAdapter(Capabilities{RootTransaction: true})
	manager := mustManager(t, cfg, ManagerOptions{}, defaultRegistration(adapter))
	innerErr := errors.New("inner failed")

	err := manager.InTx(context.Background(), TxConfig{}, func(ctx context.Context) error {
		err := manager.InNestedTx(ctx, NestedOptions{Label: "inner"}, func(ctx context.Context) error {
			return innerErr
		})
		if !errors.Is(err, innerErr) {
			t.Fatalf("expected inner error, got %v", err)
		}
		if !MustFrom(ctx).IsRollbackOnly() {
			t.Fatalf("expected rollback-only after emulated nested rollback")
		}
		return nil
	})
	if !errors.Is(err, innerErr) {
		t.Fatalf("expected rollback reason, got %v", err)
	}
	if len(adapter.nestedCalls) != 0 {
		t.Fatalf("expected no adapter-level nested begin, got %d", len(adapter.nestedCalls))
	}
	if len(adapter.rollbacks) != 1 {
		t.Fatalf("expected root rollback, got %v", adapter.rollbacks)
	}
}

func TestNestedEmulatedInheritsRollbackOnly(t *testing.T) {
	cfg := DefaultConfig()
	cfg.NestedMode = NestedEmulated
	adapter := newMockAdapter(Capabilities{RootTransaction: true})
	manager := mustManager(t, cfg, ManagerOptions{}, defaultRegistration(adapter))
	marker := errors.New("marked rollback-only")

	err := manager.InTx(context.Background(), TxConfig{}, func(ctx context.Context) error {
		u := MustFrom(ctx)
		if err := u.SetRollbackOnly(marker); err != nil {
			t.Fatalf("SetRollbackOnly: %v", err)
		}
		scope, err := u.BeginNested(ctx, NestedOptions{})
		if err != nil {
			t.Fatalf("BeginNested: %v", err)
		}
		if err := scope.Commit(ctx); err != nil {
			t.Fatalf("commit inherited rollback-only scope: %v", err)
		}
		return nil
	})
	if !errors.Is(err, marker) {
		t.Fatalf("expected rollback marker, got %v", err)
	}
	if len(adapter.nestedCalls) != 0 {
		t.Fatalf("expected no adapter nested begin, got %d", len(adapter.nestedCalls))
	}
}

func TestInNestedTxWithoutRootFails(t *testing.T) {
	manager := mustManager(t, DefaultConfig(), ManagerOptions{}, defaultRegistration(newMockAdapter(Capabilities{RootTransaction: true})))
	err := manager.InNestedTx(context.Background(), NestedOptions{}, func(ctx context.Context) error { return nil })
	if !errors.Is(err, ErrNoActiveTransaction) {
		t.Fatalf("expected no active transaction, got %v", err)
	}
}

func TestExplicitBindingOverrideConflict(t *testing.T) {
	manager := mustManager(t, DefaultConfig(), ManagerOptions{}, defaultRegistration(newMockAdapter(Capabilities{RootTransaction: true})))
	ctx := WithBindingOverride(context.Background(), BindingOverride{
		ClientName: SelectClient("override"),
	})
	err := manager.InTx(ctx, TxConfig{
		ClientName: SelectClient("explicit"),
	}, func(ctx context.Context) error { return nil })
	if !errors.Is(err, ErrBindingOverrideConflict) {
		t.Fatalf("expected binding override conflict, got %v", err)
	}
}

func TestTenantResolutionPrecedenceAndFallback(t *testing.T) {
	caps := Capabilities{RootTransaction: true}
	tenantAdapter := newMockAdapter(caps)
	sharedAdapter := newMockAdapter(caps)

	t.Run("tenant specific wins", func(t *testing.T) {
		manager := mustManager(t, DefaultConfig(), ManagerOptions{TenantPolicy: ContextTenantPolicy{}},
			Registration{Adapter: sharedAdapter, AdapterName: "mock", Client: "shared", ClientName: "shared", Default: true},
			Registration{Adapter: tenantAdapter, AdapterName: "mock", Client: "tenant", ClientName: "tenant", TenantID: "acme"},
		)
		binding, err := manager.ResolveBinding(WithTenantID(context.Background(), "acme"), ResolutionRequest{Mode: ResolutionAmbient})
		if err != nil {
			t.Fatalf("ResolveBinding: %v", err)
		}
		if binding.ClientName != "tenant" || binding.TenantID != "acme" {
			t.Fatalf("unexpected binding: %+v", binding.BindingInfo)
		}
	})

	t.Run("fallback keeps tenant context", func(t *testing.T) {
		manager := mustManager(t, DefaultConfig(), ManagerOptions{TenantPolicy: ContextTenantPolicy{}},
			Registration{Adapter: sharedAdapter, AdapterName: "mock", Client: "shared", ClientName: "shared", Default: true},
		)
		binding, err := manager.ResolveBinding(WithTenantID(context.Background(), "acme"), ResolutionRequest{Mode: ResolutionAmbient})
		if err != nil {
			t.Fatalf("ResolveBinding: %v", err)
		}
		if binding.ClientName != "shared" || binding.TenantID != "acme" {
			t.Fatalf("unexpected binding: %+v", binding.BindingInfo)
		}
	})

	t.Run("tenant required", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.RequireTenantResolution = true
		manager := mustManager(t, cfg, ManagerOptions{TenantPolicy: ContextTenantPolicy{}},
			Registration{Adapter: sharedAdapter, AdapterName: "mock", Client: "shared", ClientName: "shared", Default: true},
		)
		_, err := manager.ResolveBinding(context.Background(), ResolutionRequest{Mode: ResolutionAmbient})
		if !errors.Is(err, ErrTenantNotResolved) {
			t.Fatalf("expected tenant not resolved, got %v", err)
		}
	})

	t.Run("tenant required rejects explicit non-tenant selection", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.RequireTenantResolution = true
		manager := mustManager(t, cfg, ManagerOptions{TenantPolicy: ContextTenantPolicy{}},
			Registration{Adapter: sharedAdapter, AdapterName: "mock", Client: "shared", ClientName: "shared", Default: true},
		)
		_, err := manager.ResolveBinding(context.Background(), ResolutionRequest{
			Mode:     ResolutionAmbient,
			TenantID: NoTenant(),
		})
		if !errors.Is(err, ErrTenantNotResolved) {
			t.Fatalf("expected tenant not resolved, got %v", err)
		}
	})
}

func TestMultipleBindingsRejected(t *testing.T) {
	adapter := newMockAdapter(Capabilities{RootTransaction: true})
	manager := mustManager(t, DefaultConfig(), ManagerOptions{},
		Registration{Adapter: adapter, AdapterName: "mock", Client: "primary-client", ClientName: "primary", Default: true},
		Registration{Adapter: adapter, AdapterName: "mock", Client: "analytics-client", ClientName: "analytics"},
	)
	ctx, _, err := manager.Bind(context.Background(), ExecutionConfig{
		ClientName: SelectClient("primary"),
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	err = manager.InTx(ctx, TxConfig{
		ClientName: SelectClient("analytics"),
	}, func(ctx context.Context) error { return nil })
	if !errors.Is(err, ErrMultipleRootBindingsForbidden) {
		t.Fatalf("expected multiple root bindings forbidden, got %v", err)
	}
}

func TestHookAndInterceptorOrdering(t *testing.T) {
	adapter := newMockAdapter(Capabilities{RootTransaction: true})
	events := []string{}
	metas := []TxMeta{}
	manager := mustManager(t, DefaultConfig(), ManagerOptions{
		Interceptors: []Interceptor{recordingInterceptor{events: &events}},
		Hooks:        recordingHooks{events: &events, metas: &metas},
	}, defaultRegistration(adapter))

	err := manager.InTx(context.Background(), TxConfig{}, func(ctx context.Context) error { return nil })
	if err != nil {
		t.Fatalf("InTx: %v", err)
	}
	want := []string{
		"before_begin",
		"after_begin",
		"hook_begin",
		"before_commit",
		"after_commit",
		"hook_commit",
	}
	if !slices.Equal(events, want) {
		t.Fatalf("unexpected event order:\nwant %v\ngot  %v", want, events)
	}
	if len(metas) == 0 || metas[0].Depth != 0 || metas[0].AdapterName != "mock" || metas[0].ClientName != "primary" {
		t.Fatalf("unexpected metadata: %+v", metas)
	}
}

func TestDuplicateFinalizationRejected(t *testing.T) {
	adapter := newMockAdapter(Capabilities{RootTransaction: true, NestedTransaction: true})
	manager := mustManager(t, DefaultConfig(), ManagerOptions{}, defaultRegistration(adapter))

	err := manager.InTx(context.Background(), TxConfig{}, func(ctx context.Context) error {
		scope, err := MustFrom(ctx).BeginNested(ctx, NestedOptions{})
		if err != nil {
			t.Fatalf("BeginNested: %v", err)
		}
		if err := scope.Commit(ctx); err != nil {
			t.Fatalf("first commit: %v", err)
		}
		if err := scope.Commit(ctx); !errors.Is(err, ErrTxAlreadyClosed) {
			t.Fatalf("expected closed scope error, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("InTx: %v", err)
	}
}

func TestAmbientRunRespectsTransactionMode(t *testing.T) {
	adapter := newMockAdapter(Capabilities{RootTransaction: true})
	cfg := DefaultConfig()
	cfg.TransactionMode = GlobalAuto
	manager := mustManager(t, cfg, ManagerOptions{}, defaultRegistration(adapter))

	if err := manager.Run(context.Background(), ExecutionConfig{}, func(ctx context.Context) error {
		if !MustFrom(ctx).InTransaction() {
			t.Fatalf("expected auto transaction")
		}
		return nil
	}); err != nil {
		t.Fatalf("Run inherit: %v", err)
	}

	if err := manager.Run(context.Background(), ExecutionConfig{Transactional: TransactionalOff}, func(ctx context.Context) error {
		if MustFrom(ctx).InTransaction() {
			t.Fatalf("expected non-transactional execution")
		}
		return nil
	}); err != nil {
		t.Fatalf("Run off: %v", err)
	}
}

func TestAttachBindsDefaultUnitOfWork(t *testing.T) {
	adapter := newMockAdapter(Capabilities{})
	manager := mustManager(t, DefaultConfig(), ManagerOptions{}, defaultRegistration(adapter))

	ctx, work, err := manager.Attach(nil)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if work == nil {
		t.Fatalf("expected unit of work")
	}
	if work.InTransaction() {
		t.Fatalf("expected non-transactional unit of work")
	}
	if work.Binding().AdapterName != "mock" || work.Binding().ClientName != "primary" {
		t.Fatalf("unexpected binding: %+v", work.Binding())
	}
	got, ok := From(ctx)
	if !ok {
		t.Fatalf("expected unit of work in context")
	}
	if got != work {
		t.Fatalf("context unit of work mismatch: got %v want %v", got, work)
	}
}

func TestOptionDowngradePolicy(t *testing.T) {
	t.Run("strict rejects unsupported option", func(t *testing.T) {
		adapter := newMockAdapter(Capabilities{RootTransaction: true})
		manager := mustManager(t, DefaultConfig(), ManagerOptions{}, defaultRegistration(adapter))
		called := false
		err := manager.InTx(context.Background(), TxConfig{ReadOnly: true}, func(ctx context.Context) error {
			called = true
			return nil
		})
		if err == nil {
			t.Fatalf("expected error")
		}
		if called {
			t.Fatalf("callback should not run")
		}
	})

	t.Run("allow downgrade strips unsupported option", func(t *testing.T) {
		adapter := newMockAdapter(Capabilities{RootTransaction: true})
		cfg := DefaultConfig()
		cfg.AllowOptionDowngrade = true
		manager := mustManager(t, cfg, ManagerOptions{}, defaultRegistration(adapter))
		err := manager.InTx(context.Background(), TxConfig{ReadOnly: true}, func(ctx context.Context) error { return nil })
		if err != nil {
			t.Fatalf("InTx: %v", err)
		}
		if len(adapter.beginCalls) != 1 || adapter.beginCalls[0].ReadOnly {
			t.Fatalf("expected downgraded read-only option, got %+v", adapter.beginCalls)
		}
	})
}

func TestRootTransactionUnsupported(t *testing.T) {
	adapter := newMockAdapter(Capabilities{})
	manager := mustManager(t, DefaultConfig(), ManagerOptions{}, defaultRegistration(adapter))
	called := false
	err := manager.InTx(context.Background(), TxConfig{}, func(ctx context.Context) error {
		called = true
		return nil
	})
	if !errors.Is(err, ErrRootTxUnsupported) {
		t.Fatalf("expected unsupported root tx, got %v", err)
	}
	if called {
		t.Fatalf("callback should not run")
	}
}

func TestScopeOrderViolation(t *testing.T) {
	adapter := newMockAdapter(Capabilities{RootTransaction: true, NestedTransaction: true})
	manager := mustManager(t, DefaultConfig(), ManagerOptions{}, defaultRegistration(adapter))

	err := manager.InTx(context.Background(), TxConfig{}, func(ctx context.Context) error {
		u := MustFrom(ctx)
		outer, err := u.BeginNested(ctx, NestedOptions{})
		if err != nil {
			t.Fatalf("outer BeginNested: %v", err)
		}
		inner, err := u.BeginNested(ctx, NestedOptions{})
		if err != nil {
			t.Fatalf("inner BeginNested: %v", err)
		}
		if err := outer.Commit(ctx); !errors.Is(err, ErrScopeOrderViolation) {
			t.Fatalf("expected scope order violation, got %v", err)
		}
		if err := inner.Rollback(ctx); err != nil {
			t.Fatalf("inner rollback: %v", err)
		}
		if err := outer.Rollback(ctx); err != nil {
			t.Fatalf("outer rollback: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("InTx: %v", err)
	}
}
