// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

//go:build !memdb_safe && !purego

package memdb

import (
	"sync/atomic"
	"unsafe"
)

// rootPtr is the atomically published root of a database. This variant is a
// bare pointer used with the sync/atomic functions, like upstream's: unlike
// atomic.Pointer it can be initialised in a composite literal, so Snapshot
// does not pay for an atomic store into an object nobody else can see yet.
type rootPtr struct {
	p unsafe.Pointer // *dbRoot
}

func (r *rootPtr) load() *dbRoot { return (*dbRoot)(atomic.LoadPointer(&r.p)) }

func (r *rootPtr) store(root *dbRoot) { atomic.StorePointer(&r.p, unsafe.Pointer(root)) }

func newMemDB(c *compiled, root *dbRoot, primary bool) *MemDB {
	return &MemDB{compiled: c, root: rootPtr{p: unsafe.Pointer(root)}, primary: primary}
}
