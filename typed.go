// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package memdb

import (
	"fmt"
	"iter"
	"math/bits"
	"reflect"
	"strings"
	"sync/atomic"
)

// This file is an extension: go-memdb has no counterpart. It adds a typed way
// of using the same database -- nothing here changes what is stored or how the
// untyped API behaves, and the two can be mixed freely on one table.
//
// There are three parts:
//
//   - Indexers built from accessor functions (StringIndex, IntIndex, UintIndex,
//     BoolIndex, StringSliceIndex). They go where the *FieldIndex types go in a
//     schema, produce exactly the same keys for the same values, and need no
//     reflection: not in the exported methods, and not in transactions, which
//     read the value through the function and encode it into their own buffer.
//   - Table[T], a handle that inserts *T and returns *T.
//   - StringKey, IntKey and UintKey, handles on one index of a table whose
//     lookups take the key as a plain Go value. A query through the untyped API
//     has to box its arguments into interfaces, which costs an allocation per
//     call for anything but small integers, and has to resolve two names; a
//     typed key does neither.

// signedInt and unsignedInt are the integer types IntIndex and UintIndex
// accept.
type signedInt interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64
}

type unsignedInt interface {
	~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr
}

// signedWidth returns the size of N in bytes. Index keys of integers are as
// wide as the integer's type, like upstream's.
func signedWidth[N signedInt]() int {
	// The sign bit of N is the first bit that turns 1 negative. (The shift
	// counts are variables because N may be narrower than a constant count,
	// which is well defined -- the result is 0 -- but which vet rejects.)
	var one N = 1
	for _, bit := range [...]uint{7, 15, 31} {
		if one<<bit < 0 {
			return int(bit+1) / 8
		}
	}
	return 8
}

func unsignedWidth[N unsignedInt]() int {
	return bits.Len64(uint64(^N(0))) / 8
}

// typedIndexer is what the typed indexers offer to the transaction layer: the
// index value itself, to be encoded into a buffer the transaction owns. Only
// the method matching typedKind is ever called. handled=false sends the caller
// to the exported FromObject, which owns every error.
type typedIndexer interface {
	typedKind() extKind
	stringOf(obj interface{}) (val string, handled bool)
	intOf(obj interface{}) (val int64, handled bool)
	uintOf(obj interface{}) (val uint64, handled bool)
	stringsOf(obj interface{}) (vals []string, handled bool)
	// width is the key width of an integer index, in bytes.
	width() int
	lower() bool
}

// typedBase supplies the methods a given typed indexer does not need.
type typedBase struct{}

func (typedBase) stringOf(interface{}) (string, bool)    { return "", false }
func (typedBase) intOf(interface{}) (int64, bool)        { return 0, false }
func (typedBase) uintOf(interface{}) (uint64, bool)      { return 0, false }
func (typedBase) stringsOf(interface{}) ([]string, bool) { return nil, false }
func (typedBase) width() int                             { return 0 }
func (typedBase) lower() bool                            { return false }

// object returns obj as a *T. A table normally stores *T; a T stored by value
// is accepted too (and copied), as the reflection-based indexers accept it.
func object[T any](obj interface{}) (*T, error) {
	switch o := obj.(type) {
	case *T:
		if o == nil {
			return nil, fmt.Errorf("object is a nil %T", o)
		}
		return o, nil
	case T:
		return &o, nil
	}
	return nil, fmt.Errorf("object of type %T is not a %T", obj, (*T)(nil))
}

// StringIndex indexes a string that a function extracts from the object. It is
// the typed twin of StringFieldIndex: same keys, same treatment of the empty
// string as a missing value, same prefix queries.
type StringIndex[T any] struct {
	typedBase
	Get       func(*T) string
	Lowercase bool
}

func (s *StringIndex[T]) typedKind() extKind { return extTypedString }
func (s *StringIndex[T]) lower() bool        { return s.Lowercase }

func (s *StringIndex[T]) stringOf(obj interface{}) (string, bool) {
	if o, ok := obj.(*T); ok && o != nil && s.Get != nil {
		return s.Get(o), true
	}
	return "", false
}

func (s *StringIndex[T]) FromObject(obj interface{}) (bool, []byte, error) {
	o, err := object[T](obj)
	if err != nil {
		return false, nil, err
	}
	if s.Get == nil {
		return false, nil, fmt.Errorf("StringIndex has no Get function")
	}
	val := s.Get(o)
	if val == "" {
		return false, nil, nil
	}
	out := make([]byte, 0, len(val)+1)
	return true, append(appendString(out, val, s.Lowercase), 0), nil
}

func (s *StringIndex[T]) FromArgs(args ...interface{}) ([]byte, error) {
	return stringFromArgs(args, s.Lowercase)
}

func (s *StringIndex[T]) PrefixFromArgs(args ...interface{}) ([]byte, error) {
	val, err := stringFromArgs(args, s.Lowercase)
	if err != nil {
		return nil, err
	}
	// Strip the null terminator, the rest is a prefix
	return val[:len(val)-1], nil
}

func stringFromArgs(args []interface{}, lowercase bool) ([]byte, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("must provide only a single argument")
	}
	arg, ok := args[0].(string)
	if !ok {
		return nil, fmt.Errorf("argument must be a string: %#v", args[0])
	}
	out := make([]byte, 0, len(arg)+1)
	return append(appendString(out, arg, lowercase), 0), nil
}

// StringSliceIndex indexes every string of a slice that a function extracts
// from the object: the typed twin of StringSliceFieldIndex.
type StringSliceIndex[T any] struct {
	typedBase
	Get       func(*T) []string
	Lowercase bool
}

func (s *StringSliceIndex[T]) typedKind() extKind { return extTypedStrings }
func (s *StringSliceIndex[T]) lower() bool        { return s.Lowercase }

func (s *StringSliceIndex[T]) stringsOf(obj interface{}) ([]string, bool) {
	if o, ok := obj.(*T); ok && o != nil && s.Get != nil {
		return s.Get(o), true
	}
	return nil, false
}

func (s *StringSliceIndex[T]) FromObject(obj interface{}) (bool, [][]byte, error) {
	o, err := object[T](obj)
	if err != nil {
		return false, nil, err
	}
	if s.Get == nil {
		return false, nil, fmt.Errorf("StringSliceIndex has no Get function")
	}
	vals := s.Get(o)
	out := make([][]byte, 0, len(vals))
	for _, val := range vals {
		if val == "" {
			continue
		}
		key := make([]byte, 0, len(val)+1)
		out = append(out, append(appendString(key, val, s.Lowercase), 0))
	}
	if len(out) == 0 {
		return false, nil, nil
	}
	return true, out, nil
}

func (s *StringSliceIndex[T]) FromArgs(args ...interface{}) ([]byte, error) {
	return stringFromArgs(args, s.Lowercase)
}

func (s *StringSliceIndex[T]) PrefixFromArgs(args ...interface{}) ([]byte, error) {
	val, err := stringFromArgs(args, s.Lowercase)
	if err != nil {
		return nil, err
	}
	return val[:len(val)-1], nil
}

// IntIndex indexes a signed integer that a function extracts from the object:
// the typed twin of IntFieldIndex. Keys are as wide as N and sort numerically.
//
// Unlike IntFieldIndex, whose FromArgs encodes an argument at the width of the
// argument's own type, IntIndex accepts any signed integer type in queries and
// always encodes at the width of N, so an untyped constant works as expected.
type IntIndex[T any, N signedInt] struct {
	typedBase
	Get func(*T) N
}

func (i *IntIndex[T, N]) typedKind() extKind { return extTypedInt }
func (i *IntIndex[T, N]) width() int         { return signedWidth[N]() }

func (i *IntIndex[T, N]) intOf(obj interface{}) (int64, bool) {
	if o, ok := obj.(*T); ok && o != nil && i.Get != nil {
		return int64(i.Get(o)), true
	}
	return 0, false
}

func (i *IntIndex[T, N]) FromObject(obj interface{}) (bool, []byte, error) {
	o, err := object[T](obj)
	if err != nil {
		return false, nil, err
	}
	if i.Get == nil {
		return false, nil, fmt.Errorf("IntIndex has no Get function")
	}
	return true, appendInt(nil, int64(i.Get(o)), signedWidth[N]()), nil
}

func (i *IntIndex[T, N]) FromArgs(args ...interface{}) ([]byte, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("must provide only a single argument")
	}
	val, ok := signedArg(args[0])
	if !ok {
		// A named integer type, such as N itself may be.
		if v := reflect.ValueOf(args[0]); v.IsValid() && v.CanInt() {
			val, ok = v.Int(), true
		}
	}
	if !ok {
		return nil, fmt.Errorf("argument must be a signed integer: %#v", args[0])
	}
	size := signedWidth[N]()
	if size < 8 && (val < -1<<(8*size-1) || val > 1<<(8*size-1)-1) {
		return nil, fmt.Errorf("argument %d overflows the index's %d-bit integer", val, 8*size)
	}
	return appendInt(nil, val, size), nil
}

// signedArg accepts a query argument of any built-in signed integer type.
func signedArg(arg interface{}) (int64, bool) {
	switch v := arg.(type) {
	case int:
		return int64(v), true
	case int8:
		return int64(v), true
	case int16:
		return int64(v), true
	case int32:
		return int64(v), true
	case int64:
		return v, true
	}
	return 0, false
}

// UintIndex indexes an unsigned integer that a function extracts from the
// object: the typed twin of UintFieldIndex, with IntIndex's handling of query
// arguments.
type UintIndex[T any, N unsignedInt] struct {
	typedBase
	Get func(*T) N
}

func (u *UintIndex[T, N]) typedKind() extKind { return extTypedUint }
func (u *UintIndex[T, N]) width() int         { return unsignedWidth[N]() }

func (u *UintIndex[T, N]) uintOf(obj interface{}) (uint64, bool) {
	if o, ok := obj.(*T); ok && o != nil && u.Get != nil {
		return uint64(u.Get(o)), true
	}
	return 0, false
}

func (u *UintIndex[T, N]) FromObject(obj interface{}) (bool, []byte, error) {
	o, err := object[T](obj)
	if err != nil {
		return false, nil, err
	}
	if u.Get == nil {
		return false, nil, fmt.Errorf("UintIndex has no Get function")
	}
	return true, appendUint(nil, uint64(u.Get(o)), unsignedWidth[N]()), nil
}

func (u *UintIndex[T, N]) FromArgs(args ...interface{}) ([]byte, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("must provide only a single argument")
	}
	val, ok := unsignedArg(args[0])
	if !ok {
		if v := reflect.ValueOf(args[0]); v.IsValid() && v.CanUint() {
			val, ok = v.Uint(), true
		}
	}
	if !ok {
		return nil, fmt.Errorf("argument must be an unsigned integer: %#v", args[0])
	}
	size := unsignedWidth[N]()
	if size < 8 && val > 1<<(8*size)-1 {
		return nil, fmt.Errorf("argument %d overflows the index's %d-bit integer", val, 8*size)
	}
	return appendUint(nil, val, size), nil
}

func unsignedArg(arg interface{}) (uint64, bool) {
	switch v := arg.(type) {
	case uint:
		return uint64(v), true
	case uint8:
		return uint64(v), true
	case uint16:
		return uint64(v), true
	case uint32:
		return uint64(v), true
	case uint64:
		return v, true
	case uintptr:
		return uint64(v), true
	}
	return 0, false
}

// BoolIndex indexes a boolean that a function extracts from the object: the
// typed twin of BoolFieldIndex (and, with a suitable function, of FieldSetIndex
// and ConditionalIndex).
type BoolIndex[T any] struct {
	typedBase
	Get func(*T) bool
}

func (b *BoolIndex[T]) typedKind() extKind { return extTypedBool }

// intOf reports the boolean as 0 or 1.
func (b *BoolIndex[T]) intOf(obj interface{}) (int64, bool) {
	if o, ok := obj.(*T); ok && o != nil && b.Get != nil {
		if b.Get(o) {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

func (b *BoolIndex[T]) FromObject(obj interface{}) (bool, []byte, error) {
	o, err := object[T](obj)
	if err != nil {
		return false, nil, err
	}
	if b.Get == nil {
		return false, nil, fmt.Errorf("BoolIndex has no Get function")
	}
	if b.Get(o) {
		return true, []byte{1}, nil
	}
	return true, []byte{0}, nil
}

func (b *BoolIndex[T]) FromArgs(args ...interface{}) ([]byte, error) {
	return fromBoolArgs(args)
}

// Table is a typed handle on a table that stores *T. It carries no state but
// the table's name: declare it once, next to the schema, and use it with any
// database that has the table.
//
//	var people = memdb.NewTable[Person]("person")
//
//	err := people.Insert(txn, &Person{...})
//	p, err := people.First(txn, "id", "joe@aol.com") // p is a *Person
type Table[T any] struct {
	name string
}

// NewTable returns the typed handle of the named table.
func NewTable[T any](name string) Table[T] { return Table[T]{name: name} }

// Name returns the table's name.
func (t Table[T]) Name() string { return t.name }

// Insert is Txn.Insert.
func (t Table[T]) Insert(txn *Txn, obj *T) error { return txn.Insert(t.name, obj) }

// Delete is Txn.Delete.
func (t Table[T]) Delete(txn *Txn, obj *T) error { return txn.Delete(t.name, obj) }

// typedResult asserts a query result to *T; a miss is a nil *T.
func typedResult[T any](raw interface{}, err error) (*T, error) {
	if raw == nil || err != nil {
		return nil, err
	}
	return raw.(*T), nil
}

// First is Txn.First.
func (t Table[T]) First(txn *Txn, index string, args ...interface{}) (*T, error) {
	return typedResult[T](txn.First(t.name, index, args...))
}

// Last is Txn.Last.
func (t Table[T]) Last(txn *Txn, index string, args ...interface{}) (*T, error) {
	return typedResult[T](txn.Last(t.name, index, args...))
}

// FirstWatch is Txn.FirstWatch.
func (t Table[T]) FirstWatch(txn *Txn, index string, args ...interface{}) (<-chan struct{}, *T, error) {
	ch, raw, err := txn.FirstWatch(t.name, index, args...)
	obj, err := typedResult[T](raw, err)
	return ch, obj, err
}

// Rows is the result of a typed query: All ranges over it, WatchCh is the
// query's watch channel (see ResultIterator).
type Rows[T any] struct {
	it ResultIterator
}

// All returns the rows as a single-use sequence.
func (r Rows[T]) All() iter.Seq[*T] { return AllOf[*T](r.it) }

// Next returns the next row, or nil when there are no more.
func (r Rows[T]) Next() *T {
	if obj := r.it.Next(); obj != nil {
		return obj.(*T)
	}
	return nil
}

// WatchCh is ResultIterator.WatchCh.
func (r Rows[T]) WatchCh() <-chan struct{} { return r.it.WatchCh() }

// Get is Txn.Get.
func (t Table[T]) Get(txn *Txn, index string, args ...interface{}) (Rows[T], error) {
	it, err := txn.Get(t.name, index, args...)
	return Rows[T]{it}, err
}

// GetReverse is Txn.GetReverse.
func (t Table[T]) GetReverse(txn *Txn, index string, args ...interface{}) (Rows[T], error) {
	it, err := txn.GetReverse(t.name, index, args...)
	return Rows[T]{it}, err
}

// LowerBound is Txn.LowerBound.
func (t Table[T]) LowerBound(txn *Txn, index string, args ...interface{}) (Rows[T], error) {
	it, err := txn.LowerBound(t.name, index, args...)
	return Rows[T]{it}, err
}

// ReverseLowerBound is Txn.ReverseLowerBound.
func (t Table[T]) ReverseLowerBound(txn *Txn, index string, args ...interface{}) (Rows[T], error) {
	it, err := txn.ReverseLowerBound(t.name, index, args...)
	return Rows[T]{it}, err
}

// keyHandle is the part StringKey, IntKey and UintKey share: the names of a
// table and one of its indexes, and what they resolved to last time. The
// resolution depends on the database (a handle may serve several), so it is
// cached per compiled schema and simply redone when another one shows up.
type keyHandle struct {
	table, index string
	resolved     atomic.Pointer[keyResolution]
}

type keyResolution struct {
	schema *compiled
	ref    indexRef
}

func (k *keyHandle) resolve(txn *Txn) (*compiledIndex, error) {
	if r := k.resolved.Load(); r != nil && r.schema == txn.db.compiled {
		return r.ref.index, nil
	}
	ct, ok := txn.db.tables.get(k.table)
	if !ok {
		return nil, fmt.Errorf("invalid table '%s'", k.table)
	}
	ref, ok := ct.byName.get(k.index)
	if !ok || ref.prefixScan {
		return nil, fmt.Errorf("invalid index '%s'", strings.TrimSuffix(k.index, prefixSuffix))
	}
	k.resolved.Store(&keyResolution{schema: txn.db.compiled, ref: ref})
	return ref.index, nil
}

// firstByKey is First for a key that is already encoded.
func firstByKey[T any](txn *Txn, ci *compiledIndex, key []byte, prefix bool) *T {
	tree := txn.readableIndex(ci, false)
	var obj interface{}
	if ci.unique && !prefix {
		obj, _ = tree.Get(key)
	} else {
		_, obj, _ = tree.FirstPrefix(key)
	}
	if obj == nil {
		return nil
	}
	return obj.(*T)
}

func rowsByKey[T any](txn *Txn, ci *compiledIndex, key []byte) Rows[T] {
	it := &radixIterator{}
	it.watch = it.iter.SeekPrefixWatch(txn.readableIndex(ci, true), key)
	return Rows[T]{it}
}

// StringKey is a handle on one index of a table whose values are strings: an
// index built from a StringFieldIndex, StringSliceFieldIndex, UUIDFieldIndex,
// StringIndex or StringSliceIndex. (With any other indexer it still works, by
// going through the untyped path.) Declare it once and share it; it is safe
// for concurrent use.
//
//	var personByEmail = people.StringKey("id")
//
//	p, err := personByEmail.First(txn, "joe@aol.com")
type StringKey[T any] struct {
	keyHandle
}

// StringKey returns a typed handle on the named index. The name is the plain
// index name: prefix queries have methods of their own.
func (t Table[T]) StringKey(index string) *StringKey[T] {
	return &StringKey[T]{keyHandle{table: t.name, index: index}}
}

// First returns the first row whose index value is key, like Txn.First.
func (k *StringKey[T]) First(txn *Txn, key string) (*T, error) {
	ci, err := k.resolve(txn)
	if err != nil {
		return nil, err
	}
	var scratch [keyScratch]byte
	if val, ok := ci.ext.appendStringArg(scratch[:0], key, false); ok {
		return firstByKey[T](txn, ci, val, false), nil
	}
	return typedResult[T](txn.First(k.table, k.index, key))
}

// FirstPrefix returns the first row whose index value starts with prefix, like
// Txn.First on the index's "_prefix" name.
func (k *StringKey[T]) FirstPrefix(txn *Txn, prefix string) (*T, error) {
	ci, err := k.resolve(txn)
	if err != nil {
		return nil, err
	}
	var scratch [keyScratch]byte
	if val, ok := ci.ext.appendStringArg(scratch[:0], prefix, true); ok {
		return firstByKey[T](txn, ci, val, true), nil
	}
	return typedResult[T](txn.First(k.table, k.index+prefixSuffix, prefix))
}

// Get returns the rows whose index value is key, like Txn.Get.
func (k *StringKey[T]) Get(txn *Txn, key string) (Rows[T], error) {
	ci, err := k.resolve(txn)
	if err != nil {
		return Rows[T]{}, err
	}
	var scratch [keyScratch]byte
	if val, ok := ci.ext.appendStringArg(scratch[:0], key, false); ok {
		return rowsByKey[T](txn, ci, val), nil
	}
	it, err := txn.Get(k.table, k.index, key)
	return Rows[T]{it}, err
}

// GetPrefix returns the rows whose index value starts with prefix, like
// Txn.Get on the index's "_prefix" name.
func (k *StringKey[T]) GetPrefix(txn *Txn, prefix string) (Rows[T], error) {
	ci, err := k.resolve(txn)
	if err != nil {
		return Rows[T]{}, err
	}
	var scratch [keyScratch]byte
	if val, ok := ci.ext.appendStringArg(scratch[:0], prefix, true); ok {
		return rowsByKey[T](txn, ci, val), nil
	}
	it, err := txn.Get(k.table, k.index+prefixSuffix, prefix)
	return Rows[T]{it}, err
}

// IntKey is a handle on one index of a table whose values are signed integers
// of type N: an index built from an IntFieldIndex on a field of that type, or
// from an IntIndex. See StringKey.
type IntKey[T any, N signedInt] struct {
	keyHandle
}

// IntKeyOf returns a typed handle on the named integer index. (It is a
// function because a method cannot introduce the type parameter N.)
func IntKeyOf[N signedInt, T any](t Table[T], index string) *IntKey[T, N] {
	return &IntKey[T, N]{keyHandle{table: t.name, index: index}}
}

func (k *IntKey[T, N]) encode(ci *compiledIndex, dst []byte, key N) ([]byte, bool) {
	if kind := ci.ext.kind; kind != extInt && kind != extTypedInt {
		return dst, false
	}
	return appendInt(dst, int64(key), signedWidth[N]()), true
}

// First returns the first row whose index value is key, like Txn.First.
func (k *IntKey[T, N]) First(txn *Txn, key N) (*T, error) {
	ci, err := k.resolve(txn)
	if err != nil {
		return nil, err
	}
	var scratch [8]byte
	if val, ok := k.encode(ci, scratch[:0], key); ok {
		return firstByKey[T](txn, ci, val, false), nil
	}
	return typedResult[T](txn.First(k.table, k.index, key))
}

// Get returns the rows whose index value is key, like Txn.Get.
func (k *IntKey[T, N]) Get(txn *Txn, key N) (Rows[T], error) {
	ci, err := k.resolve(txn)
	if err != nil {
		return Rows[T]{}, err
	}
	var scratch [8]byte
	if val, ok := k.encode(ci, scratch[:0], key); ok {
		return rowsByKey[T](txn, ci, val), nil
	}
	it, err := txn.Get(k.table, k.index, key)
	return Rows[T]{it}, err
}

// LowerBound returns the rows whose index value is at least key, in ascending
// order, like Txn.LowerBound.
func (k *IntKey[T, N]) LowerBound(txn *Txn, key N) (Rows[T], error) {
	ci, err := k.resolve(txn)
	if err != nil {
		return Rows[T]{}, err
	}
	var scratch [8]byte
	if val, ok := k.encode(ci, scratch[:0], key); ok {
		it := &radixIterator{}
		it.iter.SeekLowerBound(txn.readableIndex(ci, true), val)
		return Rows[T]{it}, nil
	}
	it, err := txn.LowerBound(k.table, k.index, key)
	return Rows[T]{it}, err
}

// UintKey is IntKey for unsigned integers: an index built from a
// UintFieldIndex on a field of type N, or from a UintIndex.
type UintKey[T any, N unsignedInt] struct {
	keyHandle
}

// UintKeyOf returns a typed handle on the named unsigned integer index.
func UintKeyOf[N unsignedInt, T any](t Table[T], index string) *UintKey[T, N] {
	return &UintKey[T, N]{keyHandle{table: t.name, index: index}}
}

func (k *UintKey[T, N]) encode(ci *compiledIndex, dst []byte, key N) ([]byte, bool) {
	if kind := ci.ext.kind; kind != extUint && kind != extTypedUint {
		return dst, false
	}
	return appendUint(dst, uint64(key), unsignedWidth[N]()), true
}

// First returns the first row whose index value is key, like Txn.First.
func (k *UintKey[T, N]) First(txn *Txn, key N) (*T, error) {
	ci, err := k.resolve(txn)
	if err != nil {
		return nil, err
	}
	var scratch [8]byte
	if val, ok := k.encode(ci, scratch[:0], key); ok {
		return firstByKey[T](txn, ci, val, false), nil
	}
	return typedResult[T](txn.First(k.table, k.index, key))
}

// Get returns the rows whose index value is key, like Txn.Get.
func (k *UintKey[T, N]) Get(txn *Txn, key N) (Rows[T], error) {
	ci, err := k.resolve(txn)
	if err != nil {
		return Rows[T]{}, err
	}
	var scratch [8]byte
	if val, ok := k.encode(ci, scratch[:0], key); ok {
		return rowsByKey[T](txn, ci, val), nil
	}
	it, err := txn.Get(k.table, k.index, key)
	return Rows[T]{it}, err
}

// LowerBound returns the rows whose index value is at least key, in ascending
// order, like Txn.LowerBound.
func (k *UintKey[T, N]) LowerBound(txn *Txn, key N) (Rows[T], error) {
	ci, err := k.resolve(txn)
	if err != nil {
		return Rows[T]{}, err
	}
	var scratch [8]byte
	if val, ok := k.encode(ci, scratch[:0], key); ok {
		it := &radixIterator{}
		it.iter.SeekLowerBound(txn.readableIndex(ci, true), val)
		return Rows[T]{it}, nil
	}
	it, err := txn.LowerBound(k.table, k.index, key)
	return Rows[T]{it}, err
}
