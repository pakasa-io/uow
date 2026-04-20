package sqladapter

import "database/sql"

// Tx wraps *sql.Tx for the uow.Adapter contract.
type Tx struct {
	tx    *sql.Tx
	name  string
	depth int
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
	return t.tx
}
