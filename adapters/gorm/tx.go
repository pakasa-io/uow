package gormadapter

import "gorm.io/gorm"

// Tx wraps *gorm.DB for the uow.Tx contract.
type Tx struct {
	db    *gorm.DB
	name  string
	depth int
	kind  txKind
}

// Name implements uow.Tx.
func (t *Tx) Name() string {
	if t == nil {
		return ""
	}
	return t.name
}

// Depth implements uow.Tx.
func (t *Tx) Depth() int {
	if t == nil {
		return 0
	}
	return t.depth
}

// Raw implements uow.Tx.
func (t *Tx) Raw() any {
	if t == nil {
		return nil
	}
	return t.db
}
