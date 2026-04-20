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

	err = manager.InTx(context.Background(), TxConfig{}, func(ctx context.Context) error {
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
