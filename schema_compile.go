// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package memdb

import (
	"fmt"
	"slices"
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
	// bitmap marks a bitmap index (see bitmap_index.go): its tree maps an
	// index value to the set of row ids that have it, not value+id to a row.
	bitmap bool
}

// indexRef is what an index name given to a query resolves to.
type indexRef struct {
	index      *compiledIndex
	prefixScan bool
}

// compiledTable is a TableSchema resolved for the hot paths.
type compiledTable struct {
	name string
	// ord is the table's position among the tables of the schema.
	ord int
	// indexes is in a fixed order: "id" first, then the other ordinary indexes
	// sorted by name, then the bitmap indexes sorted by name. Every write walks
	// it in this order, so writes are deterministic.
	indexes []compiledIndex
	// classic is the number of ordinary indexes: indexes[:classic]. A table
	// without bitmap indexes -- every table of a go-memdb schema -- has
	// classic == len(indexes), and none of the bitmap machinery runs for it.
	classic int
	// byName resolves the index names accepted by queries, see resolveNames.
	byName nameIndex[indexRef]
	// bitmapByName does the same for the bitmap indexes, which only Where and
	// WhereWatch accept. It is nil for a table without any.
	bitmapByName *nameIndex[indexRef]
}

// bitmapIndex resolves the name of a bitmap index.
func (t *compiledTable) bitmapIndex(name string) (indexRef, bool) {
	if t.bitmapByName == nil {
		return indexRef{}, false
	}
	return t.bitmapByName.get(name)
}

// nameIndex resolves the names of a schema: the tables of a database, or the
// index names a table's queries accept. Every query starts with two of these
// lookups, and on a small table they used to cost as much as the tree descent
// itself.
//
// A schema has few names and they rarely share a length, so the names are kept
// sorted by length: a lookup compares its argument with the handful of names
// that are exactly as long, and callers almost always pass the very string
// constant the schema was built from, which compares equal by address without
// looking at a single byte. A length that many names share (fifty tables called
// "table-NN") is searched by bisection. There is no map: two small arrays are
// also cheaper to build than one, which NewMemDB's callers notice.
type nameIndex[V any] struct {
	// entries is sorted by length of name, then by name; the names of length
	// l are entries[off[l]:off[l+1]].
	entries []nameEntry[V]
	off     []uint32
}

type nameEntry[V any] struct {
	name string
	val  V
}

// crowdedLength is the number of names of one length beyond which comparing
// them one by one stops being faster than bisecting.
const crowdedLength = 4

// nameIndexBuilder places names by length in linear time and two allocations
// (a counting sort). NewMemDB is called often enough, in test suites above
// all, for a map or a comparison sort per table to show. Usage: start; count
// every name; counted; place every name; done.
type nameIndexBuilder[V any] struct {
	x nameIndex[V]
}

func (b *nameIndexBuilder[V]) start(names, longest int) {
	if names > 0 {
		b.x = nameIndex[V]{entries: make([]nameEntry[V], names), off: make([]uint32, longest+2)}
	}
}

// count announces a name of length l. off[l+1] is the number of such names
// until counted turns it into the position where they start.
func (b *nameIndexBuilder[V]) count(l int) { b.x.off[l+1]++ }

func (b *nameIndexBuilder[V]) counted() {
	for l := 1; l < len(b.x.off); l++ {
		b.x.off[l] += b.x.off[l-1]
	}
}

// place stores a name at the cursor of its length and advances the cursor.
func (b *nameIndexBuilder[V]) place(name string, val V) {
	l := len(name)
	b.x.entries[b.x.off[l]] = nameEntry[V]{name, val}
	b.x.off[l]++
}

func (b *nameIndexBuilder[V]) done() nameIndex[V] {
	x := b.x
	if len(x.off) == 0 {
		return x
	}
	// Every cursor now stands at the end of its length, which is the start
	// of the next one: shift them back.
	copy(x.off[1:], x.off)
	x.off[0] = 0
	// Only a length that enough names share to be bisected needs an order.
	for l := 0; l+1 < len(x.off); l++ {
		if bucket := x.entries[x.off[l]:x.off[l+1]]; len(bucket) > crowdedLength {
			slices.SortFunc(bucket, func(a, b nameEntry[V]) int { return strings.Compare(a.name, b.name) })
		}
	}
	return x
}

// newNameIndex builds the index of the given names.
func newNameIndex[V any](names []nameEntry[V]) nameIndex[V] {
	longest := 0
	for i := range names {
		longest = max(longest, len(names[i].name))
	}
	var b nameIndexBuilder[V]
	b.start(len(names), longest)
	for i := range names {
		b.count(len(names[i].name))
	}
	b.counted()
	for i := range names {
		b.place(names[i].name, names[i].val)
	}
	return b.done()
}

func (x *nameIndex[V]) get(name string) (V, bool) {
	if l := len(name); l+1 < len(x.off) {
		bucket := x.entries[x.off[l]:x.off[l+1]]
		if len(bucket) > crowdedLength {
			lo, hi := 0, len(bucket)
			for lo < hi {
				mid := int(uint(lo+hi) >> 1)
				if bucket[mid].name < name {
					lo = mid + 1
				} else {
					hi = mid
				}
			}
			bucket = bucket[lo:min(lo+1, len(bucket))]
		}
		for i := range bucket {
			if bucket[i].name == name {
				return bucket[i].val, true
			}
		}
	}
	var zero V
	return zero, false
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
// the total number of index slots. The only errors are misuses of extensions
// that DBSchema.Validate, which is go-memdb's, knows nothing about.
func compileSchema(schema *DBSchema) (nameIndex[*compiledTable], int, error) {
	tableNames := make([]string, 0, len(schema.Tables))
	total := 0
	for name, ts := range schema.Tables {
		tableNames = append(tableNames, name)
		total += len(ts.Indexes)
	}
	slices.Sort(tableNames)

	// One backing array for every index of every table.
	all := make([]compiledIndex, 0, total)
	var tables nameIndexBuilder[*compiledTable]
	longest := 0
	for _, tname := range tableNames {
		longest = max(longest, len(tname))
	}
	tables.start(len(tableNames), longest)
	for _, tname := range tableNames {
		tables.count(len(tname))
	}
	tables.counted()

	for tableOrd, tname := range tableNames {
		ts := schema.Tables[tname]
		// "id", then the ordinary indexes, then the bitmap indexes: one
		// exactly sized slice.
		names := make([]string, 1, len(ts.Indexes)+1)
		names[0] = id
		var bitmapNames []string
		for iname, is := range ts.Indexes {
			if _, isBitmap := is.Indexer.(*BitmapIndex); isBitmap {
				bitmapNames = append(bitmapNames, iname)
			} else if iname != id {
				names = append(names, iname)
			}
		}
		slices.Sort(names[1:])
		slices.Sort(bitmapNames)
		classic := len(names)
		names = append(names, bitmapNames...)

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
			indexer := is.Indexer
			if ord >= classic {
				// A bitmap index is compiled from the indexer it wraps.
				indexer = is.Indexer.(*BitmapIndex).Indexer
				if err := validateBitmapIndex(tname, is, indexer); err != nil {
					return nameIndex[*compiledTable]{}, 0, err
				}
				ci.bitmap = true
			}
			switch indexer := indexer.(type) {
			case SingleIndexer:
				ci.single = indexer
			case MultiIndexer:
				ci.multi = indexer
			}
			ci.prefix, _ = indexer.(PrefixIndexer)
			ci.ext = compileExtractor(indexer)
			all = append(all, ci)
		}

		ct := &compiledTable{name: tname, ord: tableOrd, indexes: all[start:len(all):len(all)], classic: classic}
		for i := range ct.indexes {
			ct.indexes[i].table = ct
		}
		ct.resolveNames()
		tables.place(tname, ct)
	}
	return tables.done(), total, nil
}

// resolveNames precomputes index-name resolution so that queries need one map
// lookup instead of suffix checks and string trimming. It reproduces
// go-memdb's rule exactly: a trailing "_prefix" is ALWAYS stripped first and
// turns the query into a prefix scan. Consequently an index that is itself
// named "x_prefix" cannot be addressed by that name (it resolves to a prefix
// scan of index "x", if that exists) but only as "x_prefix_prefix".
func (t *compiledTable) resolveNames() {
	t.byName = indexNames(t.indexes[:t.classic])
	if t.classic < len(t.indexes) {
		names := indexNames(t.indexes[t.classic:])
		t.bitmapByName = &names
	}
}

func indexNames(indexes []compiledIndex) nameIndex[indexRef] {
	names, longest := 0, 0
	for i := range indexes {
		ci := &indexes[i]
		if !strings.HasSuffix(ci.name, prefixSuffix) {
			names++
		}
		names++
		longest = max(longest, len(ci.name)+len(prefixSuffix))
	}
	var b nameIndexBuilder[indexRef]
	b.start(names, longest)
	for i := range indexes {
		ci := &indexes[i]
		if !strings.HasSuffix(ci.name, prefixSuffix) {
			b.count(len(ci.name))
		}
		b.count(len(ci.name) + len(prefixSuffix))
	}
	b.counted()
	for i := range indexes {
		ci := &indexes[i]
		if !strings.HasSuffix(ci.name, prefixSuffix) {
			b.place(ci.name, indexRef{index: ci})
		}
		b.place(ci.name+prefixSuffix, indexRef{index: ci, prefixScan: true})
	}
	return b.done()
}
