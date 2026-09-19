// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package memdb

import (
	"fmt"
	"sort"
	"strings"
)

// prefixSuffix is the index-name suffix that requests a prefix scan.
const prefixSuffix = "_prefix"

// compiledIndex is an IndexSchema resolved once, at NewMemDB time, into what
// the hot paths need: a fixed slot in the database root, the indexer already
// asserted to its concrete interfaces, and its flags.
//
// The user's schema objects are only read, never written: schemas are shared
// between databases, and indexer identity matters to callers.
type compiledIndex struct {
	schema *IndexSchema
	table  *compiledTable
	name   string

	// slot is the position of this index's tree in dbRoot.trees; ord is its
	// position within its table.
	slot int
	ord  int

	single SingleIndexer // exactly one of single/multi is set
	multi  MultiIndexer
	prefix PrefixIndexer // nil if prefix scans are unsupported
	// ext is the allocation-free fast path for the built-in indexers.
	ext *extractor

	unique       bool
	allowMissing bool
}

// indexRef is what an index name given to a query resolves to.
type indexRef struct {
	index      *compiledIndex
	prefixScan bool
}

// compiledTable is a TableSchema resolved for the hot paths.
type compiledTable struct {
	name string
	// indexes is in a fixed order: "id" first, the rest sorted by name. Every
	// write walks it in this order, so writes are deterministic.
	indexes []compiledIndex
	// byName resolves the index names accepted by queries, see resolveNames.
	byName map[string]indexRef
}

func (t *compiledTable) id() *compiledIndex { return &t.indexes[0] }

// keys appends the keys of obj in this index to kl: the index values, each
// made unique by appending the primary key if the index is not unique. It
// reports whether the object has a value in this index at all.
func (ci *compiledIndex) keys(kl, tmp *keyList, obj interface{}, idVal []byte) (bool, error) {
	var suffix []byte
	if !ci.unique {
		// Handle non-unique index by computing a unique index.
		// This is done by appending the primary key which must
		// be unique anyways.
		suffix = idVal
	}

	ok, handled, err := ci.ext.appendKeys(kl, tmp, obj, suffix)
	if handled {
		return ok, err
	}

	// Not a case for the fast path: the exported indexer decides. Its
	// slices are copied, never extended in place.
	if ci.single != nil {
		ok, val, err := ci.single.FromObject(obj)
		if err != nil {
			return false, err
		}
		if ok {
			kl.add(val, suffix)
		}
		return ok, nil
	}
	ok, vals, err := ci.multi.FromObject(obj)
	if err != nil {
		return false, err
	}
	if ok {
		for _, val := range vals {
			kl.add(val, suffix)
		}
	}
	return ok, nil
}

// primaryKey appends the primary key of obj to dst.
func (t *compiledTable) primaryKey(dst []byte, obj interface{}) ([]byte, error) {
	idx := t.id()
	out, ok, handled, err := idx.ext.appendObjectErr(dst, obj)
	if !handled {
		var val []byte
		ok, val, err = idx.single.FromObject(obj)
		out = append(dst, val...)
	}
	if err != nil {
		return dst, fmt.Errorf("failed to build primary index: %v", err)
	}
	if !ok {
		return dst, fmt.Errorf("object missing primary index")
	}
	return out, nil
}

// compileSchema resolves a validated schema. It returns the tables by name and
// the total number of index slots.
func compileSchema(schema *DBSchema) (map[string]*compiledTable, int) {
	tableNames := make([]string, 0, len(schema.Tables))
	total := 0
	for name, ts := range schema.Tables {
		tableNames = append(tableNames, name)
		total += len(ts.Indexes)
	}
	sort.Strings(tableNames)

	// One backing array for every index of every table.
	all := make([]compiledIndex, 0, total)
	tables := make(map[string]*compiledTable, len(tableNames))

	for _, tname := range tableNames {
		ts := schema.Tables[tname]
		names := make([]string, 0, len(ts.Indexes))
		for iname := range ts.Indexes {
			if iname != id {
				names = append(names, iname)
			}
		}
		sort.Strings(names)
		names = append([]string{id}, names...)

		start := len(all)
		for ord, iname := range names {
			is := ts.Indexes[iname]
			ci := compiledIndex{
				schema:       is,
				name:         iname,
				slot:         len(all),
				ord:          ord,
				unique:       is.Unique,
				allowMissing: is.AllowMissing,
			}
			switch indexer := is.Indexer.(type) {
			case SingleIndexer:
				ci.single = indexer
			case MultiIndexer:
				ci.multi = indexer
			}
			ci.prefix, _ = is.Indexer.(PrefixIndexer)
			ci.ext = compileExtractor(is.Indexer)
			all = append(all, ci)
		}

		ct := &compiledTable{name: tname, indexes: all[start:len(all):len(all)]}
		for i := range ct.indexes {
			ct.indexes[i].table = ct
		}
		ct.resolveNames()
		tables[tname] = ct
	}
	return tables, total
}

// resolveNames precomputes index-name resolution so that queries need one map
// lookup instead of suffix checks and string trimming. It reproduces
// go-memdb's rule exactly: a trailing "_prefix" is ALWAYS stripped first and
// turns the query into a prefix scan. Consequently an index that is itself
// named "x_prefix" cannot be addressed by that name (it resolves to a prefix
// scan of index "x", if that exists) but only as "x_prefix_prefix".
func (t *compiledTable) resolveNames() {
	t.byName = make(map[string]indexRef, 2*len(t.indexes))
	for i := range t.indexes {
		ci := &t.indexes[i]
		if !strings.HasSuffix(ci.name, prefixSuffix) {
			t.byName[ci.name] = indexRef{index: ci}
		}
		t.byName[ci.name+prefixSuffix] = indexRef{index: ci, prefixScan: true}
	}
}
