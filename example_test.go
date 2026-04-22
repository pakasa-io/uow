package uow

import (
	"context"
	"fmt"
)

func ExampleManager_InTx() {
	adapter := newMockAdapter(Capabilities{RootTransaction: true})
	registry := NewRegistry()
	if err := registry.Register(defaultRegistration(adapter)); err != nil {
		panic(err)
	}
	manager, err := NewManager(registry, DefaultConfig(), ManagerOptions{})
	if err != nil {
		panic(err)
	}

	err = manager.InTx(context.Background(), RootTx(
		WithLabel("example"),
	), func(ctx context.Context) error {
		u := MustFrom(ctx)
		fmt.Printf("%s/%s tx=%v\n", u.Binding().AdapterName, u.Binding().ClientName, u.InTransaction())
		return nil
	})
	if err != nil {
		panic(err)
	}

	// Output:
	// mock/primary tx=true
}

func ExampleTxConfigFromExecution() {
	execCfg := Exec(
		WithClient("primary"),
		WithTransactional(TransactionalOn),
		WithReadOnly(),
		WithLabel("reporting"),
	)

	txCfg, err := TxConfigFromExecution(execCfg)
	if err != nil {
		panic(err)
	}

	fmt.Printf("client=%s readOnly=%v label=%s\n", txCfg.ClientName.Value, txCfg.ReadOnly, txCfg.Label)

	// Output:
	// client=primary readOnly=true label=reporting
}
