// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package memdb

import (
	"bytes"
	"fmt"
	"iter"
	"strings"

	"github.com/thevilledev/go-juuri"
	"github.com/thevilledev/go-maemmidb/internal/bitmap"
	"github.com/thevilledev/go-maemmidb/internal/pvec"
)

// This file is an extension: go-memdb has no counterpart. A database whose
// schema declares no BitmapIndex never runs any of it.
//
// # Bitmap indexes
//
// An ordinary index is an ordered map from (index value, primary key) to the
// row. It answers "the rows with this value, in order", and it costs a tree
// entry per row and a tree write per row write. Two questions it answers
// badly are counting and combining: "how many allocations are running on
// this node?", "which of them belong to neither of these two jobs?" -- each
// means walking every matching row of one index and testing the others by
// hand (a FilterIterator).
//
// A bitmap index maps an index value to the SET of rows that have it, as a
// compressed bitmap over small integer row ids (internal/bitmap). Sets are
// combined with And, Or and AndNot, 64 rows per machine instruction, and know
// their size. An update that does not change the row's value in the index does
// not touch the index at all, where an ordinary index has to be rewritten
// because it points at the row object. The price: results come in row id
// order (roughly insertion order), not in index order; there are no range
// scans; and the table keeps a map from primary key to row id and a vector
// from row id to row object, which an insert or delete has to maintain.
//
// It suits what the name suggests: columns with few distinct values relative
// to the number of rows -- states, types, flags, owners, tags.
//
// Everything is as transactional as the rest of the database. The bitmaps, the
// id map and the row vector are persistent structures published with the same
// atomic root swap as the index trees: snapshots, aborts, read-your-writes and
// Txn.Snapshot work the same way.

// BitmapIndex wraps the Indexer of an IndexSchema to make the index a bitmap
// index:
//
//	"status": {
//		Name:    "status",
//		Indexer: &memdb.BitmapIndex{Indexer: &memdb.StringFieldIndex{Field: "Status"}},
//	},
//
// Any SingleIndexer or MultiIndexer can be wrapped; with a MultiIndexer a row
// is in the set of each of its values. AllowMissing works as usual. A bitmap
// index cannot be unique and cannot be the "id" index. It is queried with
// Txn.Where and Txn.WhereWatch; First, Get and the other ordered queries
// refuse it.
type BitmapIndex struct {
	Indexer Indexer
}

// FromObject makes BitmapIndex a MultiIndexer, which is what a schema requires
// of it. Transactions use the wrapped indexer directly.
func (b *BitmapIndex) FromObject(obj interface{}) (bool, [][]byte, error) {
	switch ix := b.Indexer.(type) {
	case SingleIndexer:
		ok, val, err := ix.FromObject(obj)
		if !ok || err != nil {
			return false, nil, err
		}
		return true, [][]byte{val}, nil
	case MultiIndexer:
		return ix.FromObject(obj)
	}
	return false, nil, fmt.Errorf("BitmapIndex wraps no SingleIndexer or MultiIndexer")
}

// FromArgs is the wrapped indexer's.
func (b *BitmapIndex) FromArgs(args ...interface{}) ([]byte, error) {
	if b.Indexer == nil {
		return nil, fmt.Errorf("BitmapIndex wraps no indexer")
	}
	return b.Indexer.FromArgs(args...)
}

// PrefixFromArgs is the wrapped indexer's, if it has one.
func (b *BitmapIndex) PrefixFromArgs(args ...interface{}) ([]byte, error) {
	if p, ok := b.Indexer.(PrefixIndexer); ok {
		return p.PrefixFromArgs(args...)
	}
	return nil, fmt.Errorf("the wrapped indexer does not support prefix scanning")
}

func validateBitmapIndex(table string, is *IndexSchema, inner Indexer) error {
	switch inner.(type) {
	case *BitmapIndex:
		return fmt.Errorf("bitmap index '%s' of table '%s' wraps another BitmapIndex", is.Name, table)
	case SingleIndexer, MultiIndexer:
	default:
		return fmt.Errorf("bitmap index '%s' of table '%s' must wrap a SingleIndexer or a MultiIndexer", is.Name, table)
	}
	if is.Unique {
		return fmt.Errorf("bitmap index '%s' of table '%s' cannot be unique", is.Name, table)
	}
	return nil
}

// values appends the values of obj in a bitmap index to kl. It is
// compiledIndex.keys without the primary key suffix: a bitmap index is keyed
// by the value alone.
func (ci *compiledIndex) values(kl, tmp *keyList, obj interface{}) (bool, error) {
	ok, handled, err := ci.ext.appendKeys(kl, tmp, obj, nil)
	if handled {
		return ok, err
	}
	if ci.single != nil {
		ok, val, err := ci.single.FromObject(obj)
		if err != nil {
			return false, err
		}
		if ok {
			kl.add(val, nil)
		}
		return ok, nil
	}
	ok, vals, err := ci.multi.FromObject(obj)
	if err != nil {
		return false, err
	}
	if ok {
		for _, val := range vals {
			kl.add(val, nil)
		}
	}
	return ok, nil
}

// rootExt is the part of a database version that only bitmap indexes need:
// the row bookkeeping of every table that has one, by compiledTable.ord.
type rootExt struct {
	tables []*tableRows
}

// tableRows is one immutable version of a table's row bookkeeping.
type tableRows struct {
	ids  juuri.Tree    // primary key -> row id
	rows pvec.Vector   // row id -> row object
	all  bitmap.Bitmap // the row ids in use
	free bitmap.Bitmap // the row ids below next that are not in use
	next uint32
}

func newRootExt(tables nameIndex[*compiledTable]) *rootExt {
	var ext *rootExt
	for _, e := range tables.entries {
		ct := e.val
		if ct.classic == len(ct.indexes) {
			continue
		}
		if ext == nil {
			ext = &rootExt{tables: make([]*tableRows, len(tables.entries))}
		}
		ext.tables[ct.ord] = &tableRows{ids: juuri.New()}
	}
	return ext
}

// rowsTxn is the uncommitted row bookkeeping of one table.
type rowsTxn struct {
	ids  juuri.Txn
	rows pvec.Vector
	all  bitmap.Bitmap
	free bitmap.Bitmap
	next uint32
}

// rowsOf returns the write state of a bitmap-indexed table's bookkeeping.
func (txn *Txn) rowsOf(tt *tableTxn) *rowsTxn {
	if tt.rows == nil {
		base := txn.root.ext.tables[tt.table.ord]
		tt.rows = &rowsTxn{ids: base.ids.Txn(nil), rows: base.rows, all: base.all, free: base.free, next: base.next}
	}
	return tt.rows
}

// freezeRows makes the uncommitted bookkeeping safe to hand out: see
// Txn.readableIndex for the rule. One pair of writers serves every table of
// the transaction, so everything freezes together.
func (w *writeState) freezeRows() {
	w.vecW.Freeze()
	w.bmW.Freeze()
	for i := range w.tables {
		if r := w.tables[i].rows; r != nil {
			r.ids.Freeze()
		}
	}
}

func (r *rowsTxn) version() *tableRows {
	return &tableRows{ids: r.ids.Tree(), rows: r.rows, all: r.all, free: r.free, next: r.next}
}

// withRows returns the row bookkeeping of a new database version: that of the
// transaction's base version with the written tables replaced.
func (txn *Txn) withRows(w *writeState) *rootExt {
	ext := txn.root.ext
	copied := false
	for i := range w.tables {
		tt := &w.tables[i]
		if tt.rows == nil {
			continue
		}
		if !copied {
			ext = &rootExt{tables: append([]*tableRows(nil), ext.tables...)}
			copied = true
		}
		ext.tables[tt.table.ord] = tt.rows.version()
	}
	return ext
}

// bitmapKeys builds the values obj has in every bitmap index of its table
// (w.bmNew) and, for an update, the values of the row it replaces (w.bmOld).
// Like the keys of the ordinary indexes they are all built before anything is
// written, so a failing indexer leaves the transaction as it was.
func (txn *Txn) bitmapKeys(ct *compiledTable, obj, existing interface{}) error {
	w := txn.w
	w.bmNew.reset()
	w.bmOld.reset()
	w.bmRanges = w.bmRanges[:0]
	for i := ct.classic; i < len(ct.indexes); i++ {
		ci := &ct.indexes[i]
		r := keyRange{newFrom: w.bmNew.len(), oldFrom: w.bmOld.len()}
		var err error
		if obj != nil {
			if r.ok, err = ci.values(&w.bmNew, &w.tmp, obj); err == nil && !r.ok && !ci.allowMissing {
				return fmt.Errorf("missing value for index '%s'", ci.name)
			}
		}
		if err == nil && existing != nil {
			r.okOld, err = ci.values(&w.bmOld, &w.tmp, existing)
		}
		if err != nil {
			return fmt.Errorf("failed to build index '%s': %v", ci.name, err)
		}
		r.newTo, r.oldTo = w.bmNew.len(), w.bmOld.len()
		w.bmRanges = append(w.bmRanges, r)
	}
	return nil
}

func contains(kl *keyList, from, to int, key []byte) bool {
	for k := from; k < to; k++ {
		if bytes.Equal(kl.key(k), key) {
			return true
		}
	}
	return false
}

// applyBitmaps brings the bookkeeping and the bitmap indexes of a table in
// line with a write whose keys bitmapKeys has built: obj replaces existing
// under the primary key idVal; either may be nil (a delete, an insert).
func (txn *Txn) applyBitmaps(tt *tableTxn, obj, existing interface{}, idVal []byte) {
	w, ct := txn.w, tt.table
	r := txn.rowsOf(tt)

	var id uint32
	switch {
	case existing != nil:
		v, ok := r.ids.Tree().Get(idVal)
		if !ok {
			panic(fmt.Errorf("memdb: row of table '%s' has no row id", ct.name))
		}
		id = v.(uint32)
		if obj == nil {
			r.ids.Delete(idVal)
			r.all, _ = w.bmW.Clear(r.all, id)
			r.free, _ = w.bmW.Set(r.free, id)
		}
	default:
		if min, ok := r.free.Min(); ok {
			id = min
			r.free, _ = w.bmW.Clear(r.free, id)
		} else {
			id = r.next
			r.next++
			if r.next == 0 {
				panic(fmt.Errorf("memdb: table '%s' is out of row ids", ct.name))
			}
		}
		r.ids.Insert(idVal, id)
		r.all, _ = w.bmW.Set(r.all, id)
	}
	r.rows = w.vecW.Set(r.rows, id, obj)

	for i, kr := range w.bmRanges {
		if kr.newFrom == kr.newTo && kr.oldFrom == kr.oldTo {
			continue
		}
		ci := &ct.indexes[ct.classic+i]
		var indexTxn *juuri.Txn
		// Leave the values the row keeps alone: an update that does not move
		// the row within the index costs the index nothing.
		for k := kr.oldFrom; k < kr.oldTo; k++ {
			key := w.bmOld.key(k)
			if contains(&w.bmNew, kr.newFrom, kr.newTo, key) {
				continue
			}
			if indexTxn == nil {
				indexTxn = txn.writableIndex(tt, ci)
			}
			cur, ok := indexTxn.Tree().Get(key)
			if !ok {
				continue // a value listed twice, already gone
			}
			set := cur.(bitmap.Bitmap)
			switch next, _ := w.bmW.Clear(set, id); {
			case next.Empty():
				indexTxn.Delete(key)
			case !next.Same(set):
				indexTxn.Insert(key, next)
			}
		}
		for k := kr.newFrom; k < kr.newTo; k++ {
			key := w.bmNew.key(k)
			if contains(&w.bmOld, kr.oldFrom, kr.oldTo, key) {
				continue
			}
			if indexTxn == nil {
				indexTxn = txn.writableIndex(tt, ci)
			}
			var set bitmap.Bitmap
			if cur, ok := indexTxn.Tree().Get(key); ok {
				set = cur.(bitmap.Bitmap)
			}
			// A set this transaction already owns is changed in place and
			// is in the tree already.
			if next, _ := w.bmW.Set(set, id); !next.Same(set) {
				indexTxn.Insert(key, next)
			}
		}
	}
}

// RowSet is a set of rows of one table, as one transaction sees them: the
// result of Txn.Where and Txn.AllRows and of combining such sets. It is an
// immutable value, cheap to copy, and safe for concurrent use. It stays valid
// for as long as it is kept, like a read transaction, whatever happens to the
// transaction it came from.
//
// Sets can only be combined if they are from the same table and the same
// state of the database: from one read transaction, or from one write
// transaction with no write in between. Anything else is a programming error
// and panics.
type RowSet struct {
	table *compiledTable
	rows  pvec.Vector
	bits  bitmap.Bitmap
}

func (s RowSet) with(o RowSet) {
	if s.table != o.table || !s.rows.Same(o.rows) {
		panic("memdb: row sets of different tables or of different states of a table cannot be combined")
	}
}

// And returns the rows that are in s and in every one of the others.
func (s RowSet) And(others ...RowSet) RowSet {
	for _, o := range others {
		s.with(o)
		s.bits = bitmap.And(s.bits, o.bits)
	}
	return s
}

// Or returns the rows that are in s or in any of the others.
func (s RowSet) Or(others ...RowSet) RowSet {
	for _, o := range others {
		s.with(o)
		s.bits = bitmap.Or(s.bits, o.bits)
	}
	return s
}

// AndNot returns the rows of s that are in none of the others.
func (s RowSet) AndNot(others ...RowSet) RowSet {
	for _, o := range others {
		s.with(o)
		s.bits = bitmap.AndNot(s.bits, o.bits)
	}
	return s
}

// Len returns the number of rows in the set. It does not visit them.
func (s RowSet) Len() int { return s.bits.Len() }

// Iterator returns the rows of the set in ascending row id order, which is
// the order of insertion as long as no row has been deleted (the id of a
// deleted row is given to a later one). Its watch channel is nil: watch the
// queries the set was built from, see Txn.WhereWatch.
func (s RowSet) Iterator() ResultIterator {
	return &rowSetIterator{ids: s.bits.Iterate(), rows: s.rows.Cursor()}
}

// All returns the rows of the set as a sequence, in Iterator's order.
func (s RowSet) All() iter.Seq[interface{}] {
	return func(yield func(interface{}) bool) {
		ids, rows := s.bits.Iterate(), s.rows.Cursor()
		for id, ok := ids.Next(); ok; id, ok = ids.Next() {
			if !yield(rows.Get(id)) {
				return
			}
		}
	}
}

type rowSetIterator struct {
	ids  bitmap.Iterator
	rows pvec.Cursor
}

func (r *rowSetIterator) WatchCh() <-chan struct{} { return nil }

func (r *rowSetIterator) Next() interface{} {
	if id, ok := r.ids.Next(); ok {
		return r.rows.Get(id)
	}
	return nil
}

// tableRowsOf returns the row bookkeeping of a table as the transaction sees
// it, uncommitted writes included.
func (txn *Txn) tableRowsOf(ct *compiledTable) *tableRows {
	if x := txn.txnExtra; x != nil && x.w != nil {
		for i := range x.w.tables {
			if tt := &x.w.tables[i]; tt.table == ct {
				if tt.rows != nil {
					// The caller keeps what it gets: later writes of this
					// transaction must copy, not mutate.
					x.w.freezeRows()
					return tt.rows.version()
				}
				break
			}
		}
	}
	return txn.root.ext.tables[ct.ord]
}

// Where returns the set of rows whose value in a bitmap index matches the
// arguments: the counterpart of Get for a BitmapIndex. As with Get, appending
// "_prefix" to the index name matches every value that starts with the
// argument, if the wrapped indexer supports prefixes.
func (txn *Txn) Where(table, index string, args ...interface{}) (RowSet, error) {
	_, set, err := txn.where(false, table, index, args)
	return set, err
}

// WhereWatch is Where plus a channel that is closed when a later transaction
// changes the result: when a row enters or leaves the set. (A prefix query is
// watched like any prefix: the channel may also fire for a change to a
// neighbouring value.)
func (txn *Txn) WhereWatch(table, index string, args ...interface{}) (<-chan struct{}, RowSet, error) {
	watch, set, err := txn.where(true, table, index, args)
	if err != nil {
		return nil, RowSet{}, err
	}
	return watch.Chan(), set, nil
}

func (txn *Txn) where(watched bool, table, index string, args []interface{}) (juuri.Watch, RowSet, error) {
	if txn.root == nil {
		panic("memdb: transaction is finished")
	}
	ct, ok := txn.db.tables.get(table)
	if !ok {
		return juuri.Watch{}, RowSet{}, fmt.Errorf("invalid table '%s'", table)
	}
	ref, ok := ct.bitmapIndex(index)
	if !ok {
		if _, classic := ct.byName.get(index); classic {
			return juuri.Watch{}, RowSet{}, fmt.Errorf("index '%s' is not a bitmap index", strings.TrimSuffix(index, prefixSuffix))
		}
		return juuri.Watch{}, RowSet{}, fmt.Errorf("invalid index '%s'", strings.TrimSuffix(index, prefixSuffix))
	}
	if len(args) == 0 {
		return juuri.Watch{}, RowSet{}, fmt.Errorf("a bitmap index is queried by value: no arguments given for index '%s'", ref.index.name)
	}

	// The value, exactly as Get would build it.
	var scratch [keyScratch]byte
	val, handled := ref.index.ext.appendArgs(scratch[:0], args, ref.prefixScan)
	if !handled {
		heapArgs := append([]interface{}(nil), args...)
		var err error
		switch {
		case !ref.prefixScan:
			val, err = ref.index.schema.Indexer.FromArgs(heapArgs...)
		case ref.index.prefix == nil:
			return juuri.Watch{}, RowSet{}, fmt.Errorf("index '%s' does not support prefix scanning", ref.index.name)
		default:
			val, err = ref.index.prefix.PrefixFromArgs(heapArgs...)
		}
		if err != nil {
			return juuri.Watch{}, RowSet{}, fmt.Errorf("index error: %v", err)
		}
	}

	set := RowSet{table: ct, rows: txn.tableRowsOf(ct).rows}
	tree := txn.readableIndex(ref.index, watched)
	if !ref.prefixScan {
		watch, cur, ok := tree.GetWatch(val)
		if ok {
			set.bits = cur.(bitmap.Bitmap)
		}
		return watch, set, nil
	}
	var it juuri.Iterator
	watch := it.SeekPrefixWatch(tree, val)
	for cur, ok := it.Next(); ok; cur, ok = it.Next() {
		set.bits = bitmap.Or(set.bits, cur.(bitmap.Bitmap))
	}
	return watch, set, nil
}

// AllRows returns the set of all rows of a table that has at least one bitmap
// index. Its Len is the size of the table; s.AndNot(...) of it is a
// complement.
func (txn *Txn) AllRows(table string) (RowSet, error) {
	if txn.root == nil {
		panic("memdb: transaction is finished")
	}
	ct, ok := txn.db.tables.get(table)
	if !ok {
		return RowSet{}, fmt.Errorf("invalid table '%s'", table)
	}
	if ct.classic == len(ct.indexes) {
		return RowSet{}, fmt.Errorf("table '%s' has no bitmap index", table)
	}
	rows := txn.tableRowsOf(ct)
	return RowSet{table: ct, rows: rows.rows, bits: rows.all}, nil
}
