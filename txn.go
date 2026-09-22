// Copyright IBM Corp. 2015, 2026
// SPDX-License-Identifier: MPL-2.0
//
// Modifications Copyright (c) 2026 Ville Vesilehto
// Derived from github.com/hashicorp/go-memdb txn.go @ 7d3fdd5: the exported
// API, its documentation, the error messages and the observable semantics are
// upstream's; the implementation on top of go-juuri is new.

package memdb

import (
	"bytes"
	"fmt"
	"hash/maphash"
	"strings"
	"sync"

	"github.com/thevilledev/go-juuri"
	"github.com/thevilledev/go-maemmidb/internal/bitmap"
	"github.com/thevilledev/go-maemmidb/internal/pvec"
)

const (
	id = "id"
)

var (
	// ErrNotFound is returned when the requested item is not found
	ErrNotFound = fmt.Errorf("not found")
)

// Txn is a transaction against a MemDB.
// This can be a read or write transaction.
type Txn struct {
	db *MemDB

	// root is the database version this transaction reads. A write
	// transaction sets it to nil when it is committed or aborted.
	//
	// A read transaction holds nothing else, and nothing it holds is ever
	// mutated: like upstream's, it may be used from several goroutines.
	root *dbRoot

	// txnExtra is what only a write transaction uses. It is allocated together
	// with the transaction (see MemDB.Txn) and nil in a read transaction, which
	// is what most transactions are and which is less than half the size for it.
	*txnExtra

	write bool
}

// txnExtra is the write half of a transaction.
type txnExtra struct {
	// w is the state of a write transaction, allocated on the first write.
	w *writeState

	// The deferred functions. Nearly every transaction that defers anything
	// defers one function, which needs no slice; the rest are behind a
	// pointer so that a write transaction stays within its size class.
	after     func()
	afterMore *[]func()

	// changes is used to track the changes performed during the transaction. If
	// it is nil at transaction start then changes are not tracked.
	changes Changes
}

// writeTxn is the single allocation behind a write transaction.
type writeTxn struct {
	Txn
	extra txnExtra
}

// writeState holds the uncommitted index trees of a write transaction plus
// the scratch buffers its writes build keys in. It never escapes the
// transaction, so finished transactions return it to a pool: a steady stream
// of small write transactions allocates no bookkeeping at all.
type writeState struct {
	// nf collects what the transaction replaces, for notification at commit.
	nf juuri.Notifier
	// tables lists the tables written so far; nearly always one or two.
	tables []tableTxn

	// Scratch for the row being written: its primary key, the keys it has
	// (newKeys) and the keys its previous version had (oldKeys) in every
	// secondary index, and where each index's keys are within those lists.
	id      []byte
	newKeys keyList
	oldKeys keyList
	tmp     keyList // intermediate values of compound multi-indexes
	ranges  []keyRange

	// The same for the bitmap indexes of the row's table, plus the writers
	// of their persistent structures (see bitmap_index.go).
	bmNew    keyList
	bmOld    keyList
	bmRanges []keyRange
	bmW      bitmap.Writer
	vecW     pvec.Writer
}

// keyRange locates one index's keys inside writeState.newKeys and oldKeys.
type keyRange struct {
	newFrom, newTo int
	oldFrom, oldTo int
	ok, okOld      bool
}

// tableTxn holds the tree transactions of one table, by compiledIndex.ord.
// Entries are zero (not Started) until the index is first written.
type tableTxn struct {
	table *compiledTable
	idx   []juuri.Txn
	// rows is the table's row bookkeeping, if it has bitmap indexes and has
	// been written.
	rows *rowsTxn
}

var writeStatePool = sync.Pool{New: func() interface{} { return new(writeState) }}

// maxPooledScratch keeps a transaction that built enormous keys from pinning
// that memory in the pool forever.
const maxPooledScratch = 64 << 10

// release returns the write state to the pool. Everything that could keep
// database memory alive is cleared first.
func (w *writeState) release() {
	w.nf.Reset()
	for i := range w.tables {
		tt := &w.tables[i]
		clear(tt.idx)
		tt.table, tt.rows = nil, nil
	}
	w.tables = w.tables[:0]
	w.bmW.Freeze()
	w.vecW.Freeze()
	if cap(w.newKeys.buf) > maxPooledScratch || cap(w.oldKeys.buf) > maxPooledScratch || cap(w.id) > maxPooledScratch {
		w.newKeys, w.oldKeys, w.id = keyList{}, keyList{}, nil
	}
	writeStatePool.Put(w)
}

// TrackChanges enables change tracking for the transaction. If called at any
// point before commit, subsequent mutations will be recorded and can be
// retrieved using ChangeSet. Once this has been called on a transaction it
// can't be unset. As with other Txn methods it's not safe to call this from a
// different goroutine than the one making mutations or committing the
// transaction.
func (txn *Txn) TrackChanges() {
	if txn.txnExtra == nil {
		txn.txnExtra = &txnExtra{} // a read transaction; harmless, as upstream
	}
	if txn.changes == nil {
		txn.changes = make(Changes, 0, 1)
	}
}

// tableTxn returns the write state of a table, creating it if needed. The
// result is only valid until the next call.
func (txn *Txn) tableTxn(ct *compiledTable) *tableTxn {
	if txn.w == nil {
		txn.w = writeStatePool.Get().(*writeState)
	}
	w := txn.w
	for i := range w.tables {
		if w.tables[i].table == ct {
			return &w.tables[i]
		}
	}

	// Reuse the slot (and its tree-transaction array) a pooled state kept.
	n := len(w.tables)
	if n < cap(w.tables) {
		w.tables = w.tables[:n+1]
	} else {
		w.tables = append(w.tables, tableTxn{})
	}
	tt := &w.tables[n]
	tt.table, tt.rows = ct, nil
	if cap(tt.idx) >= len(ct.indexes) {
		tt.idx = tt.idx[:len(ct.indexes)]
	} else {
		tt.idx = make([]juuri.Txn, len(ct.indexes))
	}
	return tt
}

// writableIndex returns the tree transaction used for modifying the given
// index, starting it if needed.
func (txn *Txn) writableIndex(tt *tableTxn, ci *compiledIndex) *juuri.Txn {
	it := &tt.idx[ci.ord]
	if !it.Started() {
		// If we are the primary DB, enable mutation tracking. Snapshots should
		// not notify, otherwise we will trigger watches on the primary DB when
		// the writes will not be visible.
		var nf *juuri.Notifier
		if txn.db.primary {
			nf = &txn.w.nf
		}
		*it = txn.root.trees[ci.slot].Txn(nf)
	}
	return it
}

// readableIndex returns the tree of an index as this transaction sees it,
// including its own uncommitted writes.
//
// escapes must be true when something derived from the tree outlives the call:
// an iterator or a watch channel. Uncommitted state is then frozen first, so
// that later writes of this transaction copy instead of mutating what the
// caller still observes.
func (txn *Txn) readableIndex(ci *compiledIndex, escapes bool) juuri.Tree {
	if x := txn.txnExtra; x != nil && x.w != nil {
		for i := range x.w.tables {
			if tt := &x.w.tables[i]; tt.table == ci.table {
				if it := &tt.idx[ci.ord]; it.Started() {
					if escapes {
						it.Freeze()
					}
					return it.Tree()
				}
				break
			}
		}
	}
	return txn.root.trees[ci.slot]
}

// Abort is used to cancel this transaction.
// This is a noop for read transactions,
// already aborted or commited transactions.
func (txn *Txn) Abort() {
	// Noop for a read transaction
	if !txn.write {
		return
	}

	// Check if already aborted or committed
	if txn.root == nil {
		return
	}

	// Clear the txn
	txn.root = nil
	x := txn.txnExtra
	if x.w != nil {
		x.w.release()
		x.w = nil
	}
	x.changes = nil

	// Release the writer lock since this is invalid
	txn.db.writer.Unlock()
}

// Commit is used to finalize this transaction.
// This is a noop for read transactions,
// already aborted or committed transactions.
func (txn *Txn) Commit() {
	// Noop for a read transaction
	if !txn.write {
		return
	}

	// Check if already aborted or committed
	if txn.root == nil {
		return
	}

	if w := txn.w; w != nil {
		// Build the next database version from the modified index trees.
		next := newDBRoot(len(txn.root.trees))
		copy(next.trees, txn.root.trees)
		for i := range w.tables {
			tt := &w.tables[i]
			for ord := range tt.idx {
				if it := &tt.idx[ord]; it.Started() {
					next.trees[tt.table.indexes[ord].slot] = it.Commit()
				}
			}
		}

		next.ext = txn.withRows(w)

		// Update the root of the DB
		txn.db.root.store(next)

		// Now issue all of the mutation updates; we do this after
		// the root pointer is swapped so that waking responders will
		// see the new state.
		w.nf.Notify()
		w.release()
		txn.w = nil
	}

	// Clear the txn
	txn.root = nil

	// Release the writer lock since this is invalid
	txn.db.writer.Unlock()

	// Run the deferred functions, if any
	x := txn.txnExtra
	if x.afterMore != nil {
		more := *x.afterMore
		for i := len(more); i > 0; i-- {
			fn := more[i-1]
			fn()
		}
	}
	if x.after != nil {
		x.after()
	}
}

// Insert is used to add or update an object into the given table.
//
// When updating an object, the obj provided should be a copy rather
// than a value updated in-place. Modifying values in-place that are already
// inserted into MemDB is not supported behavior.
func (txn *Txn) Insert(table string, obj interface{}) error {
	if !txn.write {
		return fmt.Errorf("cannot insert in read-only transaction")
	}

	// Get the table schema
	ct, ok := txn.db.tables.get(table)
	if !ok {
		return fmt.Errorf("invalid table '%s'", table)
	}

	// Get the primary ID of the object
	tt := txn.tableTxn(ct)
	w := txn.w
	idVal, err := ct.primaryKey(w.id[:0], obj)
	w.id = idVal[:0]
	if err != nil {
		return err
	}

	// Write the primary index first: a single descent both stores the object
	// and tells us whether this is an update of an existing one.
	idTxn := txn.writableIndex(tt, ct.id())
	existing, update := idTxn.Insert(idVal, obj)

	// Build the keys for every secondary index before touching any of them,
	// so that a failing indexer leaves the transaction exactly as it was.
	w.newKeys.reset()
	w.oldKeys.reset()
	w.ranges = w.ranges[:0]
	for i := 1; i < ct.classic; i++ {
		ci := &ct.indexes[i]
		r := keyRange{newFrom: w.newKeys.len(), oldFrom: w.oldKeys.len()}

		// Determine the new index value
		if r.ok, err = ci.keys(&w.newKeys, &w.tmp, obj, idVal); err == nil && update {
			// On an update, there is an existing object with the given
			// primary ID. We do the update by deleting the current object
			// and inserting the new object.
			r.okOld, err = ci.keys(&w.oldKeys, &w.tmp, existing, idVal)
		}
		if err != nil {
			err = fmt.Errorf("failed to build index '%s': %v", ci.name, err)
		} else if !r.ok && !ci.allowMissing {
			// If there is no index value, either this is an error or an
			// expected case and we can skip updating
			err = fmt.Errorf("missing value for index '%s'", ci.name)
		}
		if err != nil {
			// Undo the primary index write.
			if update {
				idTxn.Insert(idVal, existing)
			} else {
				idTxn.Delete(idVal)
			}
			return err
		}
		r.newTo, r.oldTo = w.newKeys.len(), w.oldKeys.len()
		w.ranges = append(w.ranges, r)
	}
	if ct.classic < len(ct.indexes) {
		if err := txn.bitmapKeys(ct, obj, existing); err != nil {
			// Undo the primary index write.
			if update {
				idTxn.Insert(idVal, existing)
			} else {
				idTxn.Delete(idVal)
			}
			return err
		}
	}

	for i, r := range w.ranges {
		indexTxn := txn.writableIndex(tt, &ct.indexes[i+1])

		// Handle the update by deleting from the index first. Old and new
		// keys are compared position by position, exactly like upstream.
		if r.okOld {
			for k := r.oldFrom; k < r.oldTo; k++ {
				valExist := w.oldKeys.key(k)
				// If we are writing to the same index with the same value,
				// we can avoid the delete as the insert will overwrite the
				// value anyways.
				if n := r.newFrom + k - r.oldFrom; n >= r.newTo || !bytes.Equal(valExist, w.newKeys.key(n)) {
					indexTxn.Delete(valExist)
				}
			}
		}

		// Update the value of the index
		for k := r.newFrom; k < r.newTo; k++ {
			indexTxn.Insert(w.newKeys.key(k), obj)
		}
	}
	if ct.classic < len(ct.indexes) {
		txn.applyBitmaps(tt, obj, existing, idVal)
	}
	if txn.changes != nil {
		txn.changes = append(txn.changes, Change{
			Table:      table,
			Before:     existing, // might be nil on a create
			After:      obj,
			primaryKey: append([]byte(nil), idVal...),
		})
	}
	return nil
}

// deleteFromIndexes removes obj from the secondary indexes of its table,
// except skip. All keys are built before any index is touched.
func (txn *Txn) deleteFromIndexes(tt *tableTxn, obj interface{}, idVal []byte, skip *compiledIndex) error {
	ct, w := tt.table, txn.w
	w.oldKeys.reset()
	w.ranges = w.ranges[:0]
	for i := 1; i < ct.classic; i++ {
		ci := &ct.indexes[i]
		r := keyRange{oldFrom: w.oldKeys.len()}
		if ci != skip {
			if _, err := ci.keys(&w.oldKeys, &w.tmp, obj, idVal); err != nil {
				return fmt.Errorf("failed to build index '%s': %v", ci.name, err)
			}
		}
		r.oldTo = w.oldKeys.len()
		w.ranges = append(w.ranges, r)
	}
	if ct.classic < len(ct.indexes) {
		if err := txn.bitmapKeys(ct, nil, obj); err != nil {
			return err
		}
	}
	for i, r := range w.ranges {
		if r.oldFrom == r.oldTo {
			continue
		}
		indexTxn := txn.writableIndex(tt, &ct.indexes[i+1])
		for k := r.oldFrom; k < r.oldTo; k++ {
			indexTxn.Delete(w.oldKeys.key(k))
		}
	}
	if ct.classic < len(ct.indexes) {
		txn.applyBitmaps(tt, nil, obj, idVal)
	}
	return nil
}

// Delete is used to delete a single object from the given table.
// This object must already exist in the table.
func (txn *Txn) Delete(table string, obj interface{}) error {
	if !txn.write {
		return fmt.Errorf("cannot delete in read-only transaction")
	}

	// Get the table schema
	ct, ok := txn.db.tables.get(table)
	if !ok {
		return fmt.Errorf("invalid table '%s'", table)
	}

	// Get the primary ID of the object
	tt := txn.tableTxn(ct)
	w := txn.w
	idVal, err := ct.primaryKey(w.id[:0], obj)
	w.id = idVal[:0]
	if err != nil {
		return err
	}

	// Remove the object from the primary index; this also tells us whether
	// it exists (a miss leaves the tree untouched) and what is stored.
	idTxn := txn.writableIndex(tt, ct.id())
	existing, ok := idTxn.Delete(idVal)
	if !ok {
		return ErrNotFound
	}

	// Remove the stored object from all the other indexes
	if err := txn.deleteFromIndexes(tt, existing, idVal, nil); err != nil {
		idTxn.Insert(idVal, existing)
		return err
	}
	if txn.changes != nil {
		txn.changes = append(txn.changes, Change{
			Table:      table,
			Before:     existing,
			After:      nil, // Now nil indicates deletion
			primaryKey: append([]byte(nil), idVal...),
		})
	}
	return nil
}

// DeletePrefix is used to delete an entire subtree based on a prefix.
// The given index must be a prefix index, and will be used to perform a scan and enumerate the set of objects to delete.
// These will be removed from all other indexes, and then a special prefix operation will delete the objects from the given index in an efficient subtree delete operation.
// This is useful when you have a very large number of objects indexed by the given index, along with a much smaller number of entries in the other indexes for those objects.
func (txn *Txn) DeletePrefix(table string, prefix_index string, prefix string) (bool, error) {
	if !txn.write {
		return false, fmt.Errorf("cannot delete in read-only transaction")
	}

	if !strings.HasSuffix(prefix_index, "_prefix") {
		return false, fmt.Errorf("Index name for DeletePrefix must be a prefix index, Got %v ", prefix_index)
	}

	// Get an iterator over all of the keys with the given prefix.
	entries, err := txn.Get(table, prefix_index, prefix)
	if err != nil {
		return false, fmt.Errorf("failed kvs lookup: %s", err)
	}
	// Get succeeded, so the table and the index (prefix_index minus its
	// "_prefix" suffix) both resolve.
	ct, _ := txn.db.tables.get(table)
	ref, _ := ct.byName.get(prefix_index)
	target := ref.index

	foundAny := false
	for entry := entries.Next(); entry != nil; entry = entries.Next() {
		foundAny = true
		// Get the primary ID of the object
		tt := txn.tableTxn(ct)
		w := txn.w
		idVal, err := ct.primaryKey(w.id[:0], entry)
		w.id = idVal[:0]
		if err != nil {
			return false, err
		}
		idTxn := txn.writableIndex(tt, ct.id())
		if txn.changes != nil {
			// Record the deletion
			existing, ok := idTxn.Tree().Get(idVal)
			if ok {
				txn.changes = append(txn.changes, Change{
					Table:      table,
					Before:     existing,
					After:      nil, // Now nil indicates deletion
					primaryKey: append([]byte(nil), idVal...),
				})
			}
		}
		// Remove the object from all the indexes except the given prefix index
		if target != ct.id() {
			idTxn.Delete(idVal)
		}
		if err := txn.deleteFromIndexes(tt, entry, idVal, target); err != nil {
			return false, err
		}
	}
	if foundAny {
		// Upstream quirk, kept on purpose: the subtree is cut at the RAW
		// prefix string, not at the key the indexer derives from it.
		indexTxn := txn.writableIndex(txn.tableTxn(ct), target)
		ok := indexTxn.DeletePrefix([]byte(prefix))
		if !ok {
			panic(fmt.Errorf("prefix %v matched some entries but DeletePrefix did not delete any ", prefix))
		}
		return true, nil
	}
	return false, nil
}

// DeleteAll is used to delete all the objects in a given table
// matching the constraints on the index
func (txn *Txn) DeleteAll(table, index string, args ...interface{}) (int, error) {
	if !txn.write {
		return 0, fmt.Errorf("cannot delete in read-only transaction")
	}

	// Get all the objects
	iter, err := txn.Get(table, index, args...)
	if err != nil {
		return 0, err
	}

	// Put them into a slice so there are no safety concerns while actually
	// performing the deletes
	var objs []interface{}
	for {
		obj := iter.Next()
		if obj == nil {
			break
		}

		objs = append(objs, obj)
	}

	// Do the deletes
	num := 0
	for _, obj := range objs {
		if err := txn.Delete(table, obj); err != nil {
			return num, err
		}
		num++
	}
	return num, nil
}

// FirstWatch is used to return the first matching object for
// the given constraints on the index along with the watch channel.
//
// Note that all values read in the transaction form a consistent snapshot
// from the time when the transaction was created.
//
// The watch channel is closed when a subsequent write transaction
// has updated the result of the query. Since each read transaction
// operates on an isolated snapshot, a new read transaction must be
// started to observe the changes that have been made.
//
// If the value of index ends with "_prefix", FirstWatch will perform a prefix
// match instead of full match on the index. The registered indexer must implement
// PrefixIndexer, otherwise an error is returned.
func (txn *Txn) FirstWatch(table, index string, args ...interface{}) (<-chan struct{}, interface{}, error) {
	watch, obj, err := txn.first(true, table, index, args)
	if err != nil {
		return nil, nil, err
	}
	return watch.Chan(), obj, nil
}

// first implements First and FirstWatch. Only the latter hands out a watch
// channel, which requires freezing uncommitted state (see readableIndex).
func (txn *Txn) first(watched bool, table, index string, args []interface{}) (juuri.Watch, interface{}, error) {
	// Get the index value
	var scratch [keyScratch]byte
	ref, val, err := txn.getIndexValue(scratch[:0], table, index, args)
	if err != nil {
		return juuri.Watch{}, nil, err
	}

	// Get the index itself
	tree := txn.readableIndex(ref.index, watched)

	// Do an exact lookup
	if ref.index.unique && val != nil && !ref.prefixScan {
		watch, obj, _ := tree.GetWatch(val)
		return watch, obj, nil
	}

	// Handle non-unique index by using an iterator and getting the first value
	watch, obj, _ := tree.FirstPrefix(val)
	return watch, obj, nil
}

// LastWatch is used to return the last matching object for
// the given constraints on the index along with the watch channel.
//
// Note that all values read in the transaction form a consistent snapshot
// from the time when the transaction was created.
//
// The watch channel is closed when a subsequent write transaction
// has updated the result of the query. Since each read transaction
// operates on an isolated snapshot, a new read transaction must be
// started to observe the changes that have been made.
//
// If the value of index ends with "_prefix", LastWatch will perform a prefix
// match instead of full match on the index. The registered indexer must implement
// PrefixIndexer, otherwise an error is returned.
func (txn *Txn) LastWatch(table, index string, args ...interface{}) (<-chan struct{}, interface{}, error) {
	watch, obj, err := txn.last(true, table, index, args)
	if err != nil {
		return nil, nil, err
	}
	return watch.Chan(), obj, nil
}

func (txn *Txn) last(watched bool, table, index string, args []interface{}) (juuri.Watch, interface{}, error) {
	// Get the index value
	var scratch [keyScratch]byte
	ref, val, err := txn.getIndexValue(scratch[:0], table, index, args)
	if err != nil {
		return juuri.Watch{}, nil, err
	}

	// Get the index itself
	tree := txn.readableIndex(ref.index, watched)

	// Do an exact lookup
	if ref.index.unique && val != nil && !ref.prefixScan {
		watch, obj, _ := tree.GetWatch(val)
		return watch, obj, nil
	}

	// Handle non-unique index by using an iterator and getting the last value
	watch, obj, _ := tree.LastPrefix(val)
	return watch, obj, nil
}

// First is used to return the first matching object for
// the given constraints on the index.
//
// Note that all values read in the transaction form a consistent snapshot
// from the time when the transaction was created.
func (txn *Txn) First(table, index string, args ...interface{}) (interface{}, error) {
	_, val, err := txn.first(false, table, index, args)
	return val, err
}

// Last is used to return the last matching object for
// the given constraints on the index.
//
// Note that all values read in the transaction form a consistent snapshot
// from the time when the transaction was created.
func (txn *Txn) Last(table, index string, args ...interface{}) (interface{}, error) {
	_, val, err := txn.last(false, table, index, args)
	return val, err
}

// LongestPrefix is used to fetch the longest prefix match for the given
// constraints on the index. Note that this will not work with the memdb
// StringFieldIndex because it adds null terminators which prevent the
// algorithm from correctly finding a match (it will get to right before the
// null and fail to find a leaf node). This should only be used where the prefix
// given is capable of matching indexed entries directly, which typically only
// applies to a custom indexer. See the unit test for an example.
//
// Note that all values read in the transaction form a consistent snapshot
// from the time when the transaction was created.
func (txn *Txn) LongestPrefix(table, index string, args ...interface{}) (interface{}, error) {
	// Enforce that this only works on prefix indexes.
	if !strings.HasSuffix(index, "_prefix") {
		return nil, fmt.Errorf("must use '%s_prefix' on index", index)
	}

	// Get the index value.
	var scratch [keyScratch]byte
	ref, val, err := txn.getIndexValue(scratch[:0], table, index, args)
	if err != nil {
		return nil, err
	}

	// This algorithm only makes sense against a unique index, otherwise the
	// index keys will have the IDs appended to them.
	if !ref.index.unique {
		return nil, fmt.Errorf("index '%s' is not unique", index)
	}

	// Find the longest prefix match with the given index.
	if value, ok := txn.readableIndex(ref.index, false).LongestPrefix(val); ok {
		return value, nil
	}
	return nil, nil
}

// getIndexValue is used to get the index and the value
// used to scan the index given the parameters. This handles prefix based
// scans when the index has the "_prefix" suffix. The index must support
// prefix iteration.
//
// The value is built in scratch when the index's fast path applies; scratch is
// the caller's stack buffer, which keeps read transactions free of shared
// mutable state (they may be used from several goroutines, like upstream's).
func (txn *Txn) getIndexValue(scratch []byte, table, index string, args []interface{}) (indexRef, []byte, error) {
	// Get the table schema
	ct, ok := txn.db.tables.get(table)
	if !ok {
		return indexRef{}, nil, fmt.Errorf("invalid table '%s'", table)
	}

	// Get the index schema; a "_prefix" suffix selects a prefix scan
	ref, ok := ct.byName.get(index)
	if !ok {
		if _, isBitmap := ct.bitmapIndex(index); isBitmap {
			return indexRef{}, nil, fmt.Errorf("index '%s' is a bitmap index: query it with Where", strings.TrimSuffix(index, prefixSuffix))
		}
		return indexRef{}, nil, fmt.Errorf("invalid index '%s'", strings.TrimSuffix(index, prefixSuffix))
	}

	// Hot-path for when there are no arguments
	if len(args) == 0 {
		return ref, nil, nil
	}

	if val, ok := ref.index.ext.appendArgs(scratch, args, ref.prefixScan); ok {
		return ref, val, nil
	}

	// Anything else -- custom indexers, and every error -- is the exported
	// indexer's business. It gets its own copy of the arguments, so that the
	// argument slice of every query need not be allocated on the heap just
	// because this slow path exists.
	heapArgs := append([]interface{}(nil), args...)

	// Special case the prefix scanning
	if ref.prefixScan {
		if ref.index.prefix == nil {
			return ref, nil,
				fmt.Errorf("index '%s' does not support prefix scanning", ref.index.name)
		}

		val, err := ref.index.prefix.PrefixFromArgs(heapArgs...)
		if err != nil {
			return ref, nil, fmt.Errorf("index error: %v", err)
		}
		return ref, val, err
	}

	// Get the exact match index
	val, err := ref.index.schema.Indexer.FromArgs(heapArgs...)
	if err != nil {
		return ref, nil, fmt.Errorf("index error: %v", err)
	}
	return ref, val, err
}

// ResultIterator is used to iterate over a list of results from a query on a table.
//
// When a ResultIterator is created from a write transaction, the results from
// Next will reflect a snapshot of the table at the time the ResultIterator is
// created.
// This means that calling Insert or Delete on a transaction while iterating is
// allowed, but the changes made by Insert or Delete will not be observed in the
// results returned from subsequent calls to Next. For example if an item is deleted
// from the index used by the iterator it will still be returned by Next. If an
// item is inserted into the index used by the iterator, it will not be returned
// by Next. However, an iterator created after a call to Insert or Delete will
// reflect the modifications.
//
// When a ResultIterator is created from a write transaction, and there are already
// modifications to the index used by the iterator, the modification cache of the
// index will be invalidated. This may result in some additional allocations if
// the same node in the index is modified again.
type ResultIterator interface {
	WatchCh() <-chan struct{}
	// Next returns the next result from the iterator. If there are no more results
	// nil is returned.
	Next() interface{}
}

// Get is used to construct a ResultIterator over all the rows that match the
// given constraints of an index. The index values must match exactly (this
// is not a range-based or prefix-based lookup) by default.
//
// Prefix lookups: if the named index implements PrefixIndexer, you may perform
// prefix-based lookups by appending "_prefix" to the index name. In this
// scenario, the index values given in args are treated as prefix lookups. For
// example, a StringFieldIndex will match any string with the given value
// as a prefix: "mem" matches "memdb".
//
// See the documentation for ResultIterator to understand the behaviour of the
// returned ResultIterator.
func (txn *Txn) Get(table, index string, args ...interface{}) (ResultIterator, error) {
	var scratch [keyScratch]byte
	ref, val, err := txn.getIndexValue(scratch[:0], table, index, args)
	if err != nil {
		return nil, err
	}

	// Seek the iterator to the appropriate sub-set
	iter := &radixIterator{}
	iter.watch = iter.iter.SeekPrefixWatch(txn.readableIndex(ref.index, true), val)
	return iter, nil
}

// GetReverse is used to construct a Reverse ResultIterator over all the
// rows that match the given constraints of an index.
// The returned ResultIterator's Next() will return the next Previous value.
//
// See the documentation on Get for details on arguments.
//
// See the documentation for ResultIterator to understand the behaviour of the
// returned ResultIterator.
func (txn *Txn) GetReverse(table, index string, args ...interface{}) (ResultIterator, error) {
	var scratch [keyScratch]byte
	ref, val, err := txn.getIndexValue(scratch[:0], table, index, args)
	if err != nil {
		return nil, err
	}

	// Seek the iterator to the appropriate sub-set
	iter := &radixReverseIterator{}
	iter.watch = iter.iter.SeekPrefixWatch(txn.readableIndex(ref.index, true), val)
	return iter, nil
}

// LowerBound is used to construct a ResultIterator over all the the range of
// rows that have an index value greater than or equal to the provide args.
// Calling this then iterating until the rows are larger than required allows
// range scans within an index. It is not possible to watch the resulting
// iterator since the radix tree doesn't efficiently allow watching on lower
// bound changes. The WatchCh returned will be nill and so will block forever.
//
// If the value of index ends with "_prefix", LowerBound will perform a prefix match instead of
// a full match on the index. The registered index must implement PrefixIndexer,
// otherwise an error is returned.
//
// See the documentation for ResultIterator to understand the behaviour of the
// returned ResultIterator.
func (txn *Txn) LowerBound(table, index string, args ...interface{}) (ResultIterator, error) {
	var scratch [keyScratch]byte
	ref, val, err := txn.getIndexValue(scratch[:0], table, index, args)
	if err != nil {
		return nil, err
	}

	// Seek the iterator to the appropriate sub-set
	iter := &radixIterator{}
	iter.iter.SeekLowerBound(txn.readableIndex(ref.index, true), val)
	return iter, nil
}

// ReverseLowerBound is used to construct a Reverse ResultIterator over all the
// the range of rows that have an index value less than or equal to the
// provide args.  Calling this then iterating until the rows are lower than
// required allows range scans within an index. It is not possible to watch the
// resulting iterator since the radix tree doesn't efficiently allow watching
// on lower bound changes. The WatchCh returned will be nill and so will block
// forever.
//
// See the documentation for ResultIterator to understand the behaviour of the
// returned ResultIterator.
func (txn *Txn) ReverseLowerBound(table, index string, args ...interface{}) (ResultIterator, error) {
	var scratch [keyScratch]byte
	ref, val, err := txn.getIndexValue(scratch[:0], table, index, args)
	if err != nil {
		return nil, err
	}

	// Seek the iterator to the appropriate sub-set
	iter := &radixReverseIterator{}
	iter.iter.SeekReverseLowerBound(txn.readableIndex(ref.index, true), val)
	return iter, nil
}

// objectID is a tuple of table name and the raw internal id byte slice
// converted to a string. It's only converted to a string to make it comparable
// so this struct can be used as a map index.
type objectID struct {
	Table    string
	IndexVal string
}

// mutInfo stores metadata about mutations to allow collapsing multiple
// mutations to the same object into one.
type mutInfo struct {
	firstBefore interface{}
	lastIdx     int
}

// Changes returns the set of object changes that have been made in the
// transaction so far. If change tracking is not enabled it wil always return
// nil. It can be called before or after Commit. If it is before Commit it will
// return all changes made so far which may not be the same as the final
// Changes. After abort it will always return nil. As with other Txn methods
// it's not safe to call this from a different goroutine than the one making
// mutations or committing the transaction. Mutations will appear in the order
// they were performed in the transaction but multiple operations to the same
// object will be collapsed so only the effective overall change to that object
// is present. If transaction operations are dependent (e.g. copy object X to Y
// then delete X) this might mean the set of mutations is incomplete to verify
// history, but it is complete in that the net effect is preserved (Y got a new
// value, X got removed).
func (txn *Txn) Changes() Changes {
	if txn.txnExtra == nil || txn.changes == nil {
		return nil
	}

	// Most transactions touch each object once. Establish that without
	// building upstream's string-keyed map, and return the list as is.
	if !hasDuplicateChanges(txn.changes) {
		return txn.changes
	}

	// De-duplicate mutations by key so all take effect at the point of the last
	// write but we keep the mutations in order.
	dups := make(map[objectID]mutInfo)
	for i, m := range txn.changes {
		oid := objectID{
			Table:    m.Table,
			IndexVal: string(m.primaryKey),
		}
		// Store the latest mutation index for each key value
		mi, ok := dups[oid]
		if !ok {
			// First entry for key, store the before value
			mi.firstBefore = m.Before
		}
		mi.lastIdx = i
		dups[oid] = mi
	}
	if len(dups) == len(txn.changes) {
		// No duplicates found, fast path return it as is
		return txn.changes
	}

	// Need to remove the duplicates
	cs := make(Changes, 0, len(dups))
	for i, m := range txn.changes {
		oid := objectID{
			Table:    m.Table,
			IndexVal: string(m.primaryKey),
		}
		mi := dups[oid]
		if mi.lastIdx == i {
			// This was the latest value for this key copy it with the before value in
			// case it's different. Note that m is not a pointer so we are not
			// modifying the txn.changeSet here - it's already a copy.
			m.Before = mi.firstBefore

			// Edge case - if the object was inserted and then eventually deleted in
			// the same transaction, then the net affect on that key is a no-op. Don't
			// emit a mutation with nil for before and after as it's meaningless and
			// might violate expectations and cause a panic in code that assumes at
			// least one must be set.
			if m.Before == nil && m.After == nil {
				continue
			}
			cs = append(cs, m)
		}
	}
	// Store the de-duped version in case this is called again
	txn.changes = cs
	return cs
}

// changeSeed keys the hash used to look for repeated objects in a change list.
var changeSeed = maphash.MakeSeed()

// hasDuplicateChanges reports whether two changes concern the same object,
// i.e. the same (table, primary key). It may report true when in doubt; the
// caller then runs the exact de-duplication.
func hasDuplicateChanges(changes Changes) bool {
	n := len(changes)
	if n <= 8 {
		// A handful of changes: compare them pairwise, allocation free.
		for i := 1; i < n; i++ {
			for j := 0; j < i; j++ {
				if changes[i].Table == changes[j].Table && bytes.Equal(changes[i].primaryKey, changes[j].primaryKey) {
					return true
				}
			}
		}
		return false
	}

	// Many changes: a set of 64-bit hashes instead of a map keyed by
	// (table, string(primaryKey)), which allocates a string per change. A
	// hash collision between different objects only costs the slow path.
	seen := make(map[uint64]struct{}, n)
	for i := range changes {
		var h maphash.Hash
		h.SetSeed(changeSeed)
		h.WriteString(changes[i].Table)
		h.WriteByte(0)
		h.Write(changes[i].primaryKey)
		sum := h.Sum64()
		if _, dup := seen[sum]; dup {
			return true
		}
		seen[sum] = struct{}{}
	}
	return false
}

// Defer is used to push a new arbitrary function onto a stack which
// gets called when a transaction is committed and finished. Deferred
// functions are called in LIFO order, and only invoked at the end of
// write transactions.
func (txn *Txn) Defer(fn func()) {
	switch {
	case txn.txnExtra == nil:
		// A read transaction never runs deferred functions.
	case txn.after == nil && txn.afterMore == nil && fn != nil:
		txn.after = fn
	default:
		if txn.afterMore == nil {
			txn.afterMore = new([]func())
		}
		*txn.afterMore = append(*txn.afterMore, fn)
	}
}

// radixIterator adapts a forward tree iterator to ResultIterator. The tree
// iterator is embedded by value, so a query allocates exactly one object.
type radixIterator struct {
	iter  juuri.Iterator
	watch juuri.Watch
}

func (r *radixIterator) WatchCh() <-chan struct{} {
	return r.watch.Chan()
}

func (r *radixIterator) Next() interface{} {
	value, _ := r.iter.Next()
	return value
}

type radixReverseIterator struct {
	iter  juuri.ReverseIterator
	watch juuri.Watch
}

func (r *radixReverseIterator) Next() interface{} {
	value, _ := r.iter.Previous()
	return value
}

func (r *radixReverseIterator) WatchCh() <-chan struct{} {
	return r.watch.Chan()
}

// Snapshot creates a snapshot of the current state of the transaction.
// Returns a new read-only transaction or nil if the transaction is already
// aborted or committed.
func (txn *Txn) Snapshot() *Txn {
	if txn.root == nil {
		return nil
	}

	snapshot := &Txn{
		db:   txn.db,
		root: txn.root,
	}
	if txn.txnExtra == nil || txn.w == nil {
		return snapshot
	}

	// Freeze the uncommitted index trees into the snapshot: from here on the
	// write transaction copies instead of mutating what the snapshot sees.
	snapshot.root = newDBRoot(len(txn.root.trees))
	copy(snapshot.root.trees, txn.root.trees)
	txn.w.freezeRows()
	snapshot.root.ext = txn.withRows(txn.w)
	for i := range txn.w.tables {
		tt := &txn.w.tables[i]
		for ord := range tt.idx {
			if it := &tt.idx[ord]; it.Started() {
				it.Freeze()
				snapshot.root.trees[tt.table.indexes[ord].slot] = it.Tree()
			}
		}
	}
	return snapshot
}
