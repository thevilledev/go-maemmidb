// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package bench

import (
	"fmt"
	"math/rand/v2"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Sinks keep results alive so the compiler cannot discard the measured work.
var (
	sinkAny  interface{}
	sinkInt  int
	sinkBool bool
	sinkCh   <-chan struct{}
)

const tableMain = "main"

// benchSizes returns the dataset sizes. 1M rows is opt-in (BENCH_LARGE=1)
// because populating the upstream implementation at that size takes a long
// time. BENCH_SIZES=n[,n...] overrides the list, for quick targeted runs.
func benchSizes() []int {
	if v := os.Getenv("BENCH_SIZES"); v != "" {
		var sizes []int
		for _, f := range strings.Split(v, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(f))
			if err != nil || n < 1_000 {
				panic("BENCH_SIZES: want comma-separated integers >= 1000, got " + v)
			}
			sizes = append(sizes, n)
		}
		return sizes
	}
	if os.Getenv("BENCH_LARGE") != "" {
		return []int{1_000, 100_000, 1_000_000}
	}
	return []int{1_000, 100_000}
}

// Row is the benchmark object. It exercises every built-in indexer kind.
type Row struct {
	ID      string
	Name    string
	Group   string
	Tags    []string
	Meta    map[string]string
	Age     int
	Score   uint64
	Active  bool
	UUID    string
	Parent  *string
	Version int
	Payload [4]uint64
}

// shape selects how primary keys look, which drives the radix tree's form.
type shape string

const (
	shapeUUID shape = "uuid" // random, uniform fan-out
	shapeSeq  shape = "seq"  // sequential, deep shared prefix
	shapePath shape = "path" // hierarchical, long shared prefixes
)

var (
	regions  = []string{"eu-north", "eu-west", "us-east", "us-west", "ap-south"}
	tiers    = []string{"gold", "silver", "bronze"}
	services = []string{
		"api", "auth", "billing", "cache", "cdn", "db", "dns", "edge", "gateway", "ingest",
		"jobs", "kv", "lb", "mail", "metrics", "proxy", "queue", "search", "store", "web",
	}
)

func uuidString(r *rand.Rand) string {
	var u [16]byte
	for i := 0; i < 16; i += 8 {
		v := r.Uint64()
		for j := 0; j < 8; j++ {
			u[i+j] = byte(v >> (8 * j))
		}
	}
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}

func makeID(sh shape, i int, r *rand.Rand) string {
	switch sh {
	case shapeSeq:
		return fmt.Sprintf("row-%010d", i)
	case shapePath:
		return fmt.Sprintf("/dc%d/service/%s/node-%07d", i%4, services[i%len(services)], i)
	default:
		return uuidString(r)
	}
}

func groupCount(n int) int {
	if g := n / 100; g > 10 {
		return g
	}
	return 10
}

func groupName(g int) string { return fmt.Sprintf("group-%05d", g) }

// makeRows builds n deterministic rows numbered from offset. groups is the
// number of distinct Group values, so a group holds ~n/groups rows.
func makeRows(sh shape, offset, n, groups int, seed uint64) []*Row {
	r := rand.New(rand.NewPCG(seed, uint64(offset)+0x9e3779b97f4a7c15))
	rows := make([]*Row, n)
	for k := 0; k < n; k++ {
		i := offset + k
		row := &Row{
			ID:     makeID(sh, i, r),
			Name:   fmt.Sprintf("Name-%X-%d", r.Uint32(), i),
			Group:  groupName(i % groups),
			Tags:   []string{fmt.Sprintf("tag-%02d", i%50), fmt.Sprintf("tag-%02d", (i/50)%50), fmt.Sprintf("tag-%02d", (i/2500+7)%50)},
			Meta:   map[string]string{"region": regions[i%len(regions)], "tier": tiers[i%len(tiers)]},
			Age:    i % 100,
			Score:  r.Uint64(),
			Active: i%2 == 0,
			UUID:   uuidString(r),
		}
		if i%2 == 0 {
			p := fmt.Sprintf("parent-%02d", i%50)
			row.Parent = &p
		}
		rows[k] = row
	}
	return rows
}

// funcIndexer is a reflection-free custom indexer, the style used by large
// go-memdb consumers. It keeps the suite from over-fitting to the built-in
// reflection-based indexers.
type funcIndexer struct {
	obj func(raw interface{}) (bool, []byte, error)
}

func (f *funcIndexer) FromObject(raw interface{}) (bool, []byte, error) { return f.obj(raw) }

func (f *funcIndexer) FromArgs(args ...interface{}) ([]byte, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("must provide only a single argument")
	}
	s, ok := args[0].(string)
	if !ok {
		return nil, fmt.Errorf("argument must be a string: %#v", args[0])
	}
	b := make([]byte, 0, len(s)+1)
	b = append(b, s...)
	return append(b, 0), nil
}

func (f *funcIndexer) PrefixFromArgs(args ...interface{}) ([]byte, error) {
	v, err := f.FromArgs(args...)
	if err != nil {
		return nil, err
	}
	return v[:len(v)-1], nil
}

func stringFunc(get func(*Row) string) *funcIndexer {
	return &funcIndexer{obj: func(raw interface{}) (bool, []byte, error) {
		s := get(raw.(*Row))
		if s == "" {
			return false, nil, nil
		}
		b := make([]byte, 0, len(s)+1)
		b = append(b, s...)
		return true, append(b, 0), nil
	}}
}

// PathRow and rawIndexer back the LongestPrefix benchmarks: keys carry no
// terminator so that a key can be a true prefix of another key.
type PathRow struct {
	Path string
	Val  int
}

type rawIndexer struct{}

func (rawIndexer) FromObject(raw interface{}) (bool, []byte, error) {
	p := raw.(*PathRow).Path
	if p == "" {
		return false, nil, nil
	}
	return true, []byte(p), nil
}

func (rawIndexer) FromArgs(args ...interface{}) ([]byte, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("must provide only a single argument")
	}
	s, ok := args[0].(string)
	if !ok {
		return nil, fmt.Errorf("argument must be a string: %#v", args[0])
	}
	return []byte(s), nil
}

func (r rawIndexer) PrefixFromArgs(args ...interface{}) ([]byte, error) { return r.FromArgs(args...) }

const (
	schemaS1   = "s1"   // id only
	schemaS3   = "s3"   // id + non-unique string + string-slice (upstream's test schema shape)
	schemaWide = "wide" // 11 indexes covering every built-in indexer
	schemaFunc = "func" // 3 custom func indexers, no reflection
	schemaS50  = "s50"  // 50 tables x 3 indexes
)

func s3Indexes() map[string]*IndexSchema {
	return map[string]*IndexSchema{
		"id":    {Name: "id", Unique: true, Indexer: &StringFieldIndex{Field: "ID"}},
		"group": {Name: "group", Indexer: &StringFieldIndex{Field: "Group"}},
		"tags":  {Name: "tags", Indexer: &StringSliceFieldIndex{Field: "Tags"}},
	}
}

func buildSchema(name string) *DBSchema {
	switch name {
	case schemaS1:
		return &DBSchema{Tables: map[string]*TableSchema{
			tableMain: {Name: tableMain, Indexes: map[string]*IndexSchema{
				"id": {Name: "id", Unique: true, Indexer: &StringFieldIndex{Field: "ID"}},
			}},
		}}
	case schemaS3:
		return &DBSchema{Tables: map[string]*TableSchema{
			tableMain: {Name: tableMain, Indexes: s3Indexes()},
		}}
	case schemaWide:
		return &DBSchema{Tables: map[string]*TableSchema{
			tableMain: {Name: tableMain, Indexes: map[string]*IndexSchema{
				"id":     {Name: "id", Unique: true, Indexer: &StringFieldIndex{Field: "ID"}},
				"name":   {Name: "name", Unique: true, Indexer: &StringFieldIndex{Field: "Name", Lowercase: true}},
				"group":  {Name: "group", Indexer: &StringFieldIndex{Field: "Group"}},
				"tags":   {Name: "tags", Indexer: &StringSliceFieldIndex{Field: "Tags"}},
				"meta":   {Name: "meta", Indexer: &StringMapFieldIndex{Field: "Meta"}},
				"age":    {Name: "age", Indexer: &IntFieldIndex{Field: "Age"}},
				"score":  {Name: "score", Indexer: &UintFieldIndex{Field: "Score"}},
				"active": {Name: "active", Indexer: &BoolFieldIndex{Field: "Active"}},
				"uuid":   {Name: "uuid", Unique: true, Indexer: &UUIDFieldIndex{Field: "UUID"}},
				"parent": {Name: "parent", AllowMissing: true, Indexer: &StringFieldIndex{Field: "Parent"}},
				"group_age": {Name: "group_age", Indexer: &CompoundIndex{Indexes: []Indexer{
					&StringFieldIndex{Field: "Group"},
					&IntFieldIndex{Field: "Age"},
				}}},
			}},
		}}
	case schemaFunc:
		return &DBSchema{Tables: map[string]*TableSchema{
			tableMain: {Name: tableMain, Indexes: map[string]*IndexSchema{
				"id":    {Name: "id", Unique: true, Indexer: stringFunc(func(r *Row) string { return r.ID })},
				"group": {Name: "group", Indexer: stringFunc(func(r *Row) string { return r.Group })},
				"name":  {Name: "name", Unique: true, Indexer: stringFunc(func(r *Row) string { return r.Name })},
			}},
			"paths": {Name: "paths", Indexes: map[string]*IndexSchema{
				"id": {Name: "id", Unique: true, Indexer: rawIndexer{}},
			}},
		}}
	case schemaS50:
		tables := make(map[string]*TableSchema, 50)
		for t := 0; t < 50; t++ {
			n := fmt.Sprintf("t%02d", t)
			if t == 0 {
				n = tableMain
			}
			tables[n] = &TableSchema{Name: n, Indexes: s3Indexes()}
		}
		return &DBSchema{Tables: tables}
	}
	panic("unknown schema " + name)
}

// fixture is a populated database plus the rows used to drive it.
type fixture struct {
	db     *MemDB
	rows   []*Row // in the database
	alt    []*Row // same keys as rows, different non-indexed Version
	moved  []*Row // same ID as rows, different Group/Age/Name (secondary keys change)
	extra  []*Row // NOT in the database
	perm   []int  // random visiting order over rows
	groups int
}

const extraRows = 4096

var (
	fixMu    sync.Mutex
	fixtures = map[string]*fixture{}
)

// dropFixtures releases every cached database, for benchmarks that measure
// the heap itself.
func dropFixtures() {
	fixMu.Lock()
	defer fixMu.Unlock()
	clear(fixtures)
	runtime.GC()
}

// getFixture returns a cached, populated database. Benchmarks that leave the
// database logically unchanged share the "" variant; benchmarks that commit
// lasting changes ask for a private variant.
//
// Databases are cached for the life of the process and never evicted: dropping
// one makes the heap shrink and grow again, and whichever benchmark runs next
// pays for the page faults. Isolation comes from the outside instead --
// scripts/bench-compare.sh runs every benchmark family in a process of its
// own, so a family only ever shares a heap with its own databases.
func getFixture(b *testing.B, schema string, sh shape, size int, variant string) *fixture {
	b.Helper()
	key := fmt.Sprintf("%s/%s/%d/%s", schema, sh, size, variant)
	fixMu.Lock()
	defer fixMu.Unlock()
	if f, ok := fixtures[key]; ok {
		return f
	}

	db, err := NewMemDB(buildSchema(schema))
	if err != nil {
		b.Fatalf("NewMemDB: %v", err)
	}
	groups := groupCount(size)
	f := &fixture{
		db:     db,
		rows:   makeRows(sh, 0, size, groups, 1),
		extra:  makeRows(sh, size, extraRows, groups, 1),
		groups: groups,
	}
	f.alt = make([]*Row, size)
	f.moved = make([]*Row, size)
	for i, r := range f.rows {
		a := *r
		a.Version++
		f.alt[i] = &a
		m := *r
		m.Group = groupName((i + 1) % groups)
		m.Age = (r.Age + 1) % 100
		m.Name = r.Name + "-moved"
		f.moved[i] = &m
	}
	f.perm = rand.New(rand.NewPCG(7, uint64(size))).Perm(size)

	const batch = 1000
	for i := 0; i < size; i += batch {
		txn := db.Txn(true)
		for j := i; j < i+batch && j < size; j++ {
			if err := txn.Insert(tableMain, f.rows[j]); err != nil {
				b.Fatalf("populate: %v", err)
			}
		}
		txn.Commit()
	}

	if schema == schemaFunc {
		txn := db.Txn(true)
		for i := 0; i < size; i++ {
			svc, region := i%50, i%7
			for _, p := range []string{
				fmt.Sprintf("/svc%02d", svc),
				fmt.Sprintf("/svc%02d/region%d", svc, region),
				fmt.Sprintf("/svc%02d/region%d/node%07d", svc, region, i),
			} {
				if err := txn.Insert("paths", &PathRow{Path: p, Val: i}); err != nil {
					b.Fatalf("populate paths: %v", err)
				}
			}
		}
		txn.Commit()
	}

	fixtures[key] = f
	return f
}

func must(b *testing.B, err error) {
	if err != nil {
		b.Helper()
		b.Fatal(err)
	}
}
