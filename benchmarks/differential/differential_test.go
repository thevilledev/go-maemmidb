// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

// Package differential drives github.com/hashicorp/go-memdb and
// github.com/thevilledev/go-maemmidb with identical random workloads and
// demands identical observable behaviour: results and their order, errors
// (message for message), Changes(), transaction/snapshot isolation, and which
// watch channels fire at each commit.
//
// The same object pointers are inserted into both databases, so results are
// compared by identity.
package differential

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	up "github.com/hashicorp/go-memdb"

	mm "github.com/thevilledev/go-maemmidb"
)

// Obj exercises every built-in indexer.
type Obj struct {
	ID     string
	Name   string
	Group  string
	Tags   []string
	Meta   map[string]string
	Age    int
	Small  int8
	Score  uint64
	Active bool
	UUID   string
	Parent *string
	Rev    int
}

const table = "objs"

// The two schemas must be built separately (the types differ) but identically.
// Deliberately absent: a CompoundMultiIndex with AllowMissing and three or more
// sub-indexers, whose upstream implementation corrupts keys through slice
// aliasing in a way that depends on allocator size classes (see
// COMPATIBILITY.md); we fix that rather than reproduce it.

func oldSchema() *up.DBSchema {
	return &up.DBSchema{Tables: map[string]*up.TableSchema{
		table: {Name: table, Indexes: map[string]*up.IndexSchema{
			"id":        {Name: "id", Unique: true, Indexer: &up.StringFieldIndex{Field: "ID"}},
			"name":      {Name: "name", Unique: true, Indexer: &up.StringFieldIndex{Field: "Name", Lowercase: true}},
			"group":     {Name: "group", Indexer: &up.StringFieldIndex{Field: "Group"}},
			"tags":      {Name: "tags", AllowMissing: true, Indexer: &up.StringSliceFieldIndex{Field: "Tags"}},
			"meta":      {Name: "meta", AllowMissing: true, Indexer: &up.StringMapFieldIndex{Field: "Meta"}},
			"age":       {Name: "age", Indexer: &up.IntFieldIndex{Field: "Age"}},
			"small":     {Name: "small", Indexer: &up.IntFieldIndex{Field: "Small"}},
			"score":     {Name: "score", Indexer: &up.UintFieldIndex{Field: "Score"}},
			"active":    {Name: "active", Indexer: &up.BoolFieldIndex{Field: "Active"}},
			"uuid":      {Name: "uuid", Unique: true, Indexer: &up.UUIDFieldIndex{Field: "UUID"}},
			"parent":    {Name: "parent", AllowMissing: true, Indexer: &up.StringFieldIndex{Field: "Parent"}},
			"hasparent": {Name: "hasparent", Indexer: &up.FieldSetIndex{Field: "Parent"}},
			"adult": {Name: "adult", Indexer: &up.ConditionalIndex{Conditional: func(o interface{}) (bool, error) {
				return o.(*Obj).Age >= 18, nil
			}}},
			"group_age": {Name: "group_age", Indexer: &up.CompoundIndex{Indexes: []up.Indexer{
				&up.StringFieldIndex{Field: "Group"}, &up.IntFieldIndex{Field: "Age"}}}},
			"group_parent": {Name: "group_parent", AllowMissing: true, Indexer: &up.CompoundIndex{AllowMissing: true, Indexes: []up.Indexer{
				&up.StringFieldIndex{Field: "Group"}, &up.StringFieldIndex{Field: "Parent"}}}},
			"group_tags": {Name: "group_tags", AllowMissing: true, Indexer: &up.CompoundMultiIndex{Indexes: []up.Indexer{
				&up.StringFieldIndex{Field: "Group"}, &up.StringSliceFieldIndex{Field: "Tags"}}}},
			"tags_meta": {Name: "tags_meta", AllowMissing: true, Indexer: &up.CompoundMultiIndex{Indexes: []up.Indexer{
				&up.StringSliceFieldIndex{Field: "Tags"}, &up.StringMapFieldIndex{Field: "Meta"}}}},
		}},
	}}
}

func newSchema() *mm.DBSchema {
	return &mm.DBSchema{Tables: map[string]*mm.TableSchema{
		table: {Name: table, Indexes: map[string]*mm.IndexSchema{
			"id":        {Name: "id", Unique: true, Indexer: &mm.StringFieldIndex{Field: "ID"}},
			"name":      {Name: "name", Unique: true, Indexer: &mm.StringFieldIndex{Field: "Name", Lowercase: true}},
			"group":     {Name: "group", Indexer: &mm.StringFieldIndex{Field: "Group"}},
			"tags":      {Name: "tags", AllowMissing: true, Indexer: &mm.StringSliceFieldIndex{Field: "Tags"}},
			"meta":      {Name: "meta", AllowMissing: true, Indexer: &mm.StringMapFieldIndex{Field: "Meta"}},
			"age":       {Name: "age", Indexer: &mm.IntFieldIndex{Field: "Age"}},
			"small":     {Name: "small", Indexer: &mm.IntFieldIndex{Field: "Small"}},
			"score":     {Name: "score", Indexer: &mm.UintFieldIndex{Field: "Score"}},
			"active":    {Name: "active", Indexer: &mm.BoolFieldIndex{Field: "Active"}},
			"uuid":      {Name: "uuid", Unique: true, Indexer: &mm.UUIDFieldIndex{Field: "UUID"}},
			"parent":    {Name: "parent", AllowMissing: true, Indexer: &mm.StringFieldIndex{Field: "Parent"}},
			"hasparent": {Name: "hasparent", Indexer: &mm.FieldSetIndex{Field: "Parent"}},
			"adult": {Name: "adult", Indexer: &mm.ConditionalIndex{Conditional: func(o interface{}) (bool, error) {
				return o.(*Obj).Age >= 18, nil
			}}},
			"group_age": {Name: "group_age", Indexer: &mm.CompoundIndex{Indexes: []mm.Indexer{
				&mm.StringFieldIndex{Field: "Group"}, &mm.IntFieldIndex{Field: "Age"}}}},
			"group_parent": {Name: "group_parent", AllowMissing: true, Indexer: &mm.CompoundIndex{AllowMissing: true, Indexes: []mm.Indexer{
				&mm.StringFieldIndex{Field: "Group"}, &mm.StringFieldIndex{Field: "Parent"}}}},
			"group_tags": {Name: "group_tags", AllowMissing: true, Indexer: &mm.CompoundMultiIndex{Indexes: []mm.Indexer{
				&mm.StringFieldIndex{Field: "Group"}, &mm.StringSliceFieldIndex{Field: "Tags"}}}},
			"tags_meta": {Name: "tags_meta", AllowMissing: true, Indexer: &mm.CompoundMultiIndex{Indexes: []mm.Indexer{
				&mm.StringSliceFieldIndex{Field: "Tags"}, &mm.StringMapFieldIndex{Field: "Meta"}}}},
		}},
	}}
}

// txnAPI is the part of *Txn both implementations share by signature.
type txnAPI interface {
	Insert(table string, obj interface{}) error
	Delete(table string, obj interface{}) error
	DeleteAll(table, index string, args ...interface{}) (int, error)
	DeletePrefix(table, prefixIndex, prefix string) (bool, error)
	First(table, index string, args ...interface{}) (interface{}, error)
	Last(table, index string, args ...interface{}) (interface{}, error)
	FirstWatch(table, index string, args ...interface{}) (<-chan struct{}, interface{}, error)
	LastWatch(table, index string, args ...interface{}) (<-chan struct{}, interface{}, error)
	LongestPrefix(table, index string, args ...interface{}) (interface{}, error)
	TrackChanges()
	Commit()
	Abort()
}

// iterAPI is ResultIterator, structurally.
type iterAPI interface {
	WatchCh() <-chan struct{}
	Next() interface{}
}

func drain(it iterAPI, limit int) []interface{} {
	var out []interface{}
	for v := it.Next(); v != nil && len(out) < limit; v = it.Next() {
		out = append(out, v)
	}
	return out
}

func sameObjs(a, b []interface{}) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func errText(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

// sameInsertErr compares Insert errors. The primary-key checks run first and
// must agree word for word. The per-index checks run in index order, which
// upstream takes from Go map iteration: when an object violates several
// indexes, upstream reports a random one of them, so only the kind of error
// can be compared.
func sameInsertErr(a, b error) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if a.Error() == b.Error() {
		return true
	}
	perIndex := func(e error) bool {
		m := e.Error()
		return strings.HasPrefix(m, "missing value for index '") || strings.HasPrefix(m, "failed to build index '")
	}
	return perIndex(a) && perIndex(b)
}

func fired(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

type harness struct {
	t    testing.TB
	r    *rand.Rand
	seed int64
	log  []string

	odb *up.MemDB
	ndb *mm.MemDB
}

func (h *harness) logf(format string, args ...interface{}) {
	if len(h.log) < 400 {
		h.log = append(h.log, fmt.Sprintf(format, args...))
	}
}

func (h *harness) failf(format string, args ...interface{}) {
	h.t.Helper()
	h.t.Fatalf("seed %d: %s\n--- trace ---\n%s", h.seed, fmt.Sprintf(format, args...), strings.Join(h.log, "\n"))
}

var (
	groups  = []string{"alpha", "alphabet", "beta", "Beta", ""}
	tagPool = []string{"a", "ab", "abc", "b", "", "A"}
	metaKV  = []string{"k", "k2", "", "v", "vv"}
	uuids   = []string{
		"00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000002",
		"00000000-0000-0000-0000-0000000000ff", "a0000000-0000-0000-0000-000000000000",
		"A0000000-0000-0000-0000-00000000000A", "not-a-uuid", "",
	}
)

func (h *harness) randObj() *Obj {
	r := h.r
	o := &Obj{
		ID:     fmt.Sprintf("id-%02d", r.Intn(24)),
		Group:  groups[r.Intn(len(groups))],
		Age:    r.Intn(40) - 5,
		Small:  int8(r.Intn(256) - 128),
		Score:  uint64(r.Intn(5)) << uint(r.Intn(60)),
		Active: r.Intn(2) == 0,
		UUID:   uuids[r.Intn(len(uuids))],
		Rev:    r.Int(),
	}
	o.Name = "Name-" + o.ID
	if r.Intn(12) == 0 {
		o.Name = ""
	}
	if r.Intn(10) == 0 {
		o.ID = ""
	}
	for i := r.Intn(4); i > 0; i-- {
		o.Tags = append(o.Tags, tagPool[r.Intn(len(tagPool))])
	}
	if n := r.Intn(3); n > 0 {
		o.Meta = map[string]string{}
		for ; n > 0; n-- {
			o.Meta[metaKV[r.Intn(len(metaKV))]] = metaKV[r.Intn(len(metaKV))]
		}
	}
	if r.Intn(2) == 0 {
		p := groups[r.Intn(len(groups))]
		o.Parent = &p
	}
	return o
}

type query struct {
	index string
	args  []interface{}
}

func (q query) String() string { return fmt.Sprintf("%s%v", q.index, q.args) }

// randQuery produces valid and deliberately invalid queries: unknown indexes,
// wrong argument counts and types, prefix scans on indexes without support.
func (h *harness) randQuery() query {
	r := h.r
	str := func() interface{} {
		pool := []string{"", "a", "al", "alpha", "alphabet", "beta", "Beta", "id-", "id-0", "id-1", "id-07", "name-id-1", "Name-id-03", "k", "v", "zzz"}
		return pool[r.Intn(len(pool))]
	}
	var q query
	switch r.Intn(20) {
	case 0:
		q = query{"id", nil}
	case 1:
		q = query{"id", []interface{}{str()}}
	case 2:
		q = query{"id_prefix", []interface{}{str()}}
	case 3:
		q = query{"name", []interface{}{str()}}
	case 4:
		q = query{"group", []interface{}{str()}}
	case 5:
		q = query{"group_prefix", []interface{}{str()}}
	case 6:
		q = query{"tags", []interface{}{str()}}
	case 7:
		q = query{"tags_prefix", []interface{}{str()}}
	case 8:
		q = query{"meta", []interface{}{str(), str()}}
	case 9:
		q = query{"meta", []interface{}{str()}}
	case 10:
		q = query{"age", []interface{}{r.Intn(40) - 5}}
	case 11:
		q = query{"small", []interface{}{int8(r.Intn(256) - 128)}}
	case 12:
		q = query{"score", []interface{}{uint64(r.Intn(5)) << uint(r.Intn(60))}}
	case 13:
		q = query{"active", []interface{}{r.Intn(2) == 0}}
	case 14:
		q = query{"uuid", []interface{}{uuids[r.Intn(len(uuids))]}}
	case 15:
		q = query{"uuid_prefix", []interface{}{[]string{"0000", "00000000-0000", "a", "A0", "0", "zz", ""}[r.Intn(7)]}}
	case 16:
		q = query{"group_age", []interface{}{str(), r.Intn(40) - 5}}
	case 17:
		q = query{"group_age_prefix", []interface{}{str()}}
	case 18:
		opts := []query{
			{"hasparent", []interface{}{true}}, {"adult", []interface{}{false}},
			{"parent", []interface{}{str()}}, {"group_parent", []interface{}{str(), str()}},
			{"group_parent_prefix", []interface{}{str()}}, {"group_tags", []interface{}{str(), str()}},
			{"tags_meta", []interface{}{str(), str(), str()}}, {"tags_meta", []interface{}{str(), str()}},
			{"group", nil}, {"age", nil}, {"tags_meta", nil},
		}
		q = opts[r.Intn(len(opts))]
	default:
		// Invalid on purpose.
		opts := []query{
			{"nope", []interface{}{str()}}, {"nope_prefix", []interface{}{str()}},
			{"age_prefix", []interface{}{1}}, {"age_prefix", nil}, {"active_prefix", []interface{}{true}},
			{"id", []interface{}{1}}, {"id", []interface{}{str(), str()}}, {"age", []interface{}{"x"}},
			{"age", []interface{}{int32(3)}}, {"score", []interface{}{3}}, {"active", []interface{}{"t"}},
			{"meta", []interface{}{str(), str(), str()}}, {"group_age", []interface{}{str()}},
			{"group_age", []interface{}{1, 2}}, {"uuid", []interface{}{7}}, {"id_prefix_prefix", []interface{}{str()}},
		}
		q = opts[r.Intn(len(opts))]
	}
	return q
}

const drainLimit = 1 << 20

// compareQuery runs one random read against both transactions.
func (h *harness) compareQuery(ot *up.Txn, nt *mm.Txn) {
	q := h.randQuery()
	kind := h.r.Intn(7)
	h.logf("  query kind=%d %v", kind, q)
	switch kind {
	case 0:
		ov, oe := ot.First(table, q.index, q.args...)
		nv, ne := nt.First(table, q.index, q.args...)
		if ov != nv || errText(oe) != errText(ne) {
			h.failf("First(%v): upstream %v,%s new %v,%s", q, ov, errText(oe), nv, errText(ne))
		}
	case 1:
		ov, oe := ot.Last(table, q.index, q.args...)
		nv, ne := nt.Last(table, q.index, q.args...)
		if ov != nv || errText(oe) != errText(ne) {
			h.failf("Last(%v): upstream %v,%s new %v,%s", q, ov, errText(oe), nv, errText(ne))
		}
	case 2:
		oi, oe := ot.Get(table, q.index, q.args...)
		ni, ne := nt.Get(table, q.index, q.args...)
		h.compareIters("Get", q, oi, oe, ni, ne, true)
	case 3:
		oi, oe := ot.GetReverse(table, q.index, q.args...)
		ni, ne := nt.GetReverse(table, q.index, q.args...)
		h.compareIters("GetReverse", q, oi, oe, ni, ne, true)
	case 4:
		oi, oe := ot.LowerBound(table, q.index, q.args...)
		ni, ne := nt.LowerBound(table, q.index, q.args...)
		h.compareIters("LowerBound", q, oi, oe, ni, ne, false)
	case 5:
		oi, oe := ot.ReverseLowerBound(table, q.index, q.args...)
		ni, ne := nt.ReverseLowerBound(table, q.index, q.args...)
		h.compareIters("ReverseLowerBound", q, oi, oe, ni, ne, false)
	case 6:
		ov, oe := ot.LongestPrefix(table, q.index, q.args...)
		nv, ne := nt.LongestPrefix(table, q.index, q.args...)
		if ov != nv || errText(oe) != errText(ne) {
			h.failf("LongestPrefix(%v): upstream %v,%s new %v,%s", q, ov, errText(oe), nv, errText(ne))
		}
	}
}

func (h *harness) compareIters(what string, q query, oi up.ResultIterator, oe error, ni mm.ResultIterator, ne error, watchable bool) {
	if errText(oe) != errText(ne) {
		h.failf("%s(%v): upstream err %s, new err %s", what, q, errText(oe), errText(ne))
	}
	if oe != nil {
		return
	}
	if (oi.WatchCh() == nil) != (ni.WatchCh() == nil) {
		h.failf("%s(%v): WatchCh nil-ness differs (watchable=%v)", what, q, watchable)
	}
	if o, n := drain(oi, drainLimit), drain(ni, drainLimit); !sameObjs(o, n) {
		h.failf("%s(%v): upstream %d rows %v, new %d rows %v", what, q, len(o), o, len(n), n)
	}
}

// watchPair is one logical watch taken from both databases.
type watchPair struct {
	desc     string
	id       string // set for watches that a DeletePrefix("id_prefix") may cover
	old, new <-chan struct{}
}

func (h *harness) takeWatches() []watchPair {
	ot, nt := h.odb.Txn(false), h.ndb.Txn(false)
	var ws []watchPair
	for i := 0; i < 8; i++ {
		q := h.randQuery()
		switch h.r.Intn(3) {
		case 0:
			och, _, oe := ot.FirstWatch(table, q.index, q.args...)
			nch, _, ne := nt.FirstWatch(table, q.index, q.args...)
			if errText(oe) != errText(ne) {
				h.failf("FirstWatch(%v): upstream err %s, new err %s", q, errText(oe), errText(ne))
			}
			if oe == nil {
				ws = append(ws, watchPair{"FirstWatch " + q.String(), idOf(q), och, nch})
			}
		case 1:
			och, _, oe := ot.LastWatch(table, q.index, q.args...)
			nch, _, ne := nt.LastWatch(table, q.index, q.args...)
			if errText(oe) != errText(ne) {
				h.failf("LastWatch(%v): upstream err %s, new err %s", q, errText(oe), errText(ne))
			}
			if oe == nil {
				ws = append(ws, watchPair{"LastWatch " + q.String(), idOf(q), och, nch})
			}
		default:
			oi, oe := ot.Get(table, q.index, q.args...)
			ni, ne := nt.Get(table, q.index, q.args...)
			if errText(oe) != errText(ne) {
				h.failf("Get(%v): upstream err %s, new err %s", q, errText(oe), errText(ne))
			}
			if oe == nil {
				ws = append(ws, watchPair{"Get.WatchCh " + q.String(), idOf(q), oi.WatchCh(), ni.WatchCh()})
			}
		}
	}
	return ws
}

// idOf returns the id (prefix) a query on the id index looks for.
func idOf(q query) string {
	if (q.index == "id" || q.index == "id_prefix") && len(q.args) == 1 {
		if s, ok := q.args[0].(string); ok {
			return s
		}
	}
	return "\x00none"
}

func (h *harness) compareChanges(oc up.Changes, nc mm.Changes) {
	if (oc == nil) != (nc == nil) || len(oc) != len(nc) {
		h.failf("Changes: upstream nil=%v len=%d, new nil=%v len=%d", oc == nil, len(oc), nc == nil, len(nc))
	}
	for i := range oc {
		if oc[i].Table != nc[i].Table || oc[i].Before != nc[i].Before || oc[i].After != nc[i].After {
			h.failf("Changes[%d]: upstream %+v, new %+v", i, oc[i], nc[i])
		}
	}
}

type heldIters struct {
	desc string
	old  up.ResultIterator
	new  mm.ResultIterator
}

// writeTxn runs one random write transaction on a database pair. It returns
// the id prefixes cut by DeletePrefix, or aborted=true.
func (h *harness) writeTxn(odb *up.MemDB, ndb *mm.MemDB, live []*Obj) (cut []string, aborted bool) {
	ot, nt := odb.Txn(true), ndb.Txn(true)
	track := h.r.Intn(2) == 0
	if track {
		ot.TrackChanges()
		nt.TrackChanges()
	}
	h.logf("write txn (track=%v)", track)
	var held []heldIters
	failed := false

	ops := 1 + h.r.Intn(10)
	for j := 0; j < ops && !failed; j++ {
		switch h.r.Intn(14) {
		case 0, 1, 2, 3, 4, 5:
			o := h.randObj()
			h.logf("  insert %+v", *o)
			oe, ne := ot.Insert(table, o), nt.Insert(table, o)
			if !sameInsertErr(oe, ne) {
				h.failf("Insert(%+v): upstream %s, new %s", *o, errText(oe), errText(ne))
			}
			// A failed insert leaves upstream in a state that depends on
			// map iteration order; nothing after it is comparable.
			failed = oe != nil
		case 6, 7:
			var o *Obj
			if len(live) > 0 && h.r.Intn(4) != 0 {
				o = live[h.r.Intn(len(live))]
			} else {
				o = h.randObj()
			}
			h.logf("  delete %q", o.ID)
			oe, ne := ot.Delete(table, o), nt.Delete(table, o)
			if errText(oe) != errText(ne) {
				h.failf("Delete(%q): upstream %s, new %s", o.ID, errText(oe), errText(ne))
			}
			if (oe == up.ErrNotFound) != (ne == mm.ErrNotFound) {
				h.failf("Delete(%q): ErrNotFound identity differs", o.ID)
			}
			failed = oe != nil && oe != up.ErrNotFound
		case 8:
			q := h.randQuery()
			h.logf("  deleteall %v", q)
			on, oe := ot.DeleteAll(table, q.index, q.args...)
			nn, ne := nt.DeleteAll(table, q.index, q.args...)
			if on != nn || errText(oe) != errText(ne) {
				h.failf("DeleteAll(%v): upstream %d,%s new %d,%s", q, on, errText(oe), nn, errText(ne))
			}
		case 9:
			// Only on the id index with plain-string prefixes: elsewhere the
			// raw-prefix quirk makes upstream panic or cut unrelated keys.
			prefix := []string{"id-0", "id-1", "id-2", "id-07", "id-", "zz"}[h.r.Intn(6)]
			h.logf("  deleteprefix %q", prefix)
			ook, oe := ot.DeletePrefix(table, "id_prefix", prefix)
			nok, ne := nt.DeletePrefix(table, "id_prefix", prefix)
			if ook != nok || errText(oe) != errText(ne) {
				h.failf("DeletePrefix(%q): upstream %v,%s new %v,%s", prefix, ook, errText(oe), nok, errText(ne))
			}
			if nok {
				cut = append(cut, prefix)
			}
		case 10:
			// DeletePrefix misuse: not a prefix index name, unknown index,
			// index without prefix support, prefix that matches nothing.
			idx := []string{"id", "nope_prefix", "age_prefix", "group_prefix"}[h.r.Intn(4)]
			h.logf("  deleteprefix (misuse) %q", idx)
			ook, oe := ot.DeletePrefix(table, idx, "no-such-prefix")
			nok, ne := nt.DeletePrefix(table, idx, "no-such-prefix")
			if ook != nok || errText(oe) != errText(ne) {
				h.failf("DeletePrefix(%q): upstream %v,%s new %v,%s", idx, ook, errText(oe), nok, errText(ne))
			}
		case 11:
			// An iterator over uncommitted state must keep showing it.
			q := h.randQuery()
			oi, oe := ot.Get(table, q.index, q.args...)
			ni, ne := nt.Get(table, q.index, q.args...)
			if errText(oe) != errText(ne) {
				h.failf("in-txn Get(%v): upstream err %s, new err %s", q, errText(oe), errText(ne))
			}
			if oe == nil {
				h.logf("  hold iterator %v", q)
				held = append(held, heldIters{q.String(), oi, ni})
			}
		default:
			h.compareQuery(ot, nt)
		}
	}

	for _, it := range held {
		if o, n := drain(it.old, drainLimit), drain(it.new, drainLimit); !sameObjs(o, n) {
			h.failf("held iterator %s: upstream %v, new %v", it.desc, o, n)
		}
	}
	if !failed {
		h.compareChanges(ot.Changes(), nt.Changes())
	}

	if failed || h.r.Intn(5) == 0 {
		h.logf("  abort")
		ot.Abort()
		nt.Abort()
		h.compareChanges(ot.Changes(), nt.Changes())
		return nil, true
	}

	if h.r.Intn(4) == 0 {
		// Txn.Snapshot must show the uncommitted state and keep showing it.
		os, ns := ot.Snapshot(), nt.Snapshot()
		o := h.randObj()
		if oe, ne := ot.Insert(table, o), nt.Insert(table, o); !sameInsertErr(oe, ne) {
			h.failf("Insert after Snapshot: upstream %s, new %s", errText(oe), errText(ne))
		} else if oe != nil {
			ot.Abort()
			nt.Abort()
			return nil, true
		}
		for i := 0; i < 4; i++ {
			h.compareQuery(os, ns)
		}
	}

	h.logf("  commit")
	ot.Commit()
	nt.Commit()
	h.compareChanges(ot.Changes(), nt.Changes())
	return cut, false
}

func (h *harness) liveObjs() []*Obj {
	txn := h.ndb.Txn(false)
	it, err := txn.Get(table, "id")
	if err != nil {
		h.failf("list: %v", err)
	}
	var out []*Obj
	for v := it.Next(); v != nil; v = it.Next() {
		out = append(out, v.(*Obj))
	}
	return out
}

func run(t testing.TB, seed int64, txns int) {
	h := &harness{t: t, r: rand.New(rand.NewSource(seed)), seed: seed}
	var err error
	if h.odb, err = up.NewMemDB(oldSchema()); err != nil {
		t.Fatal(err)
	}
	if h.ndb, err = mm.NewMemDB(newSchema()); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < txns; i++ {
		h.log = h.log[:0]
		watches := h.takeWatches()
		live := h.liveObjs()

		if h.r.Intn(6) == 0 {
			// Writes to a snapshot database are isolated and notify nobody.
			h.logf("snapshot db")
			os, ns := h.odb.Snapshot(), h.ndb.Snapshot()
			h.writeTxn(os, ns, live)
			ot, nt := os.Txn(false), ns.Txn(false)
			for j := 0; j < 6; j++ {
				h.compareQuery(ot, nt)
			}
			for _, w := range watches {
				if fired(w.old) || fired(w.new) {
					h.failf("%s fired by a snapshot write (upstream=%v new=%v)", w.desc, fired(w.old), fired(w.new))
				}
			}
		}

		cut, aborted := h.writeTxn(h.odb, h.ndb, live)
		for _, w := range watches {
			o, n := fired(w.old), fired(w.new)
			if o == n {
				continue
			}
			if aborted {
				h.failf("%s fired on abort (upstream=%v new=%v)", w.desc, o, n)
			}
			if n && underAny(w.id, cut) {
				// Known upstream defect (go-immutable-radix DeletePrefix
				// forgets to notify inside a subtree whose root was already
				// written in the same transaction); we do notify.
				continue
			}
			h.failf("%s: upstream fired=%v, new fired=%v", w.desc, o, n)
		}

		ot, nt := h.odb.Txn(false), h.ndb.Txn(false)
		for j := 0; j < 12; j++ {
			h.compareQuery(ot, nt)
		}
	}
}

func underAny(id string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(id, p) {
			return true
		}
	}
	return false
}

func TestDifferential(t *testing.T) {
	seeds := int64(150)
	if testing.Short() {
		seeds = 20
	}
	for seed := int64(1); seed <= seeds; seed++ {
		run(t, seed, 40)
	}
}

func FuzzOps(f *testing.F) {
	for seed := int64(0); seed < 8; seed++ {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed int64) {
		run(t, seed, 25)
	})
}
