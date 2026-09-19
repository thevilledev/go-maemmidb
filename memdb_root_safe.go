// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

//go:build memdb_safe || purego

package memdb

import "sync/atomic"

// rootPtr is the atomically published root of a database.
type rootPtr struct {
	p atomic.Pointer[dbRoot]
}

func (r *rootPtr) load() *dbRoot { return r.p.Load() }

func (r *rootPtr) store(root *dbRoot) { r.p.Store(root) }

func newMemDB(c *compiled, root *dbRoot, primary bool) *MemDB {
	db := &MemDB{compiled: c, primary: primary}
	db.root.store(root)
	return db
}
