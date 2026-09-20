// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package memdb

import (
	"encoding/binary"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
)

// This file is the allocation-free twin of the built-in indexers in index.go.
//
// The exported Indexer methods must return freshly allocated slices and find
// their field by name, through reflection, on every call. Transactions instead
// go through an extractor, compiled once per index, which
//
//   - resolves (object type, field name) to a field location once and caches
//     it, so the hot path is a type comparison plus a direct memory load, and
//   - appends the encoded key to a caller-owned buffer.
//
// An extractor only ever takes the common, well-defined path. Whenever
// anything is unusual -- an object that is not a non-nil pointer to a struct,
// a field of an unexpected kind, a nil argument, a malformed UUID, a custom
// indexer -- it reports "not handled" and the caller invokes the exported
// method, which reproduces upstream's behaviour exactly: results, error
// messages, quirks and panics. The two paths must agree on every input they
// both accept; extract_test.go checks that they do.

// keyScratch is the size of the on-stack buffer that queries and the exported
// indexer methods build a key in. Longer keys simply spill to the heap.
const keyScratch = 64

// keyList accumulates index keys in one reusable buffer.
type keyList struct {
	buf  []byte
	ends []int
}

func (k *keyList) reset() {
	k.buf = k.buf[:0]
	k.ends = k.ends[:0]
}

func (k *keyList) len() int { return len(k.ends) }

func (k *keyList) key(i int) []byte {
	start := 0
	if i > 0 {
		start = k.ends[i-1]
	}
	return k.buf[start:k.ends[i]:k.ends[i]]
}

// end closes the key that has been appended to buf since the last end.
func (k *keyList) end() { k.ends = append(k.ends, len(k.buf)) }

// add appends a complete key: val followed by suffix.
func (k *keyList) add(val, suffix []byte) {
	k.buf = append(append(k.buf, val...), suffix...)
	k.end()
}

// reserve makes room for keys more keys totalling size bytes, so that a list
// built from scratch is allocated once instead of growing by doubling.
func (k *keyList) reserve(size, keys int) {
	if cap(k.buf)-len(k.buf) < size {
		buf := make([]byte, len(k.buf), len(k.buf)+size)
		copy(buf, k.buf)
		k.buf = buf
	}
	if cap(k.ends)-len(k.ends) < keys {
		ends := make([]int, len(k.ends), len(k.ends)+keys)
		copy(ends, k.ends)
		k.ends = ends
	}
}

// split returns the keys as separate slices. They share one backing array but
// each is capped at its own length, so appending to one never touches the next.
func (k *keyList) split() [][]byte {
	out := make([][]byte, len(k.ends))
	for i := range out {
		out[i] = k.key(i)
	}
	return out
}

type extKind uint8

const (
	extCustom extKind = iota // user-defined or unsupported: always falls back
	extString
	extStringSlice
	extStringMap
	extInt
	extUint
	extBool
	extUUID
	extFieldSet
	extConditional
	extCompound
	extCompoundMulti

	// The indexers of typed.go: a function supplies the value.
	extTypedString
	extTypedStrings
	extTypedInt
	extTypedUint
	extTypedBool
)

// extractor is the compiled form of one Indexer.
type extractor struct {
	kind    extKind
	indexer Indexer

	// The indexer's configuration (Field, Lowercase, Indexes ...) is read from
	// the user's indexer on every call rather than copied, so the extractor
	// can never disagree with the exported methods about it; the typed
	// accessors below assert indexer to the type that kind implies. They
	// replace one typed pointer per kind: a throw-away extractor lives on the
	// stack of every exported indexer method (index_fast.go), which zeroes it
	// on every call, and eleven pointers of which ten are nil cost more there
	// than an assertion does here.

	// typed is the indexer of the extTyped kinds (typed.go).
	typed typedIndexer

	// subs are the compiled sub-indexers of a compound index. A throw-away
	// extractor (see index_fast.go) has none and resolves them per call.
	subs []*extractor

	// field caches the last field resolution. Tables almost always hold a
	// single object type, so one entry suffices; it is replaced atomically
	// because snapshot writers and the primary writer share extractors.
	//
	// It is a separate object, and nil in throw-away extractors: taking the
	// address of an atomic inside the extractor would force every extractor,
	// including the ones the exported indexer methods put on their stack,
	// onto the heap.
	field *atomic.Pointer[fieldInfo]
}

func (e *extractor) str() *StringFieldIndex             { return e.indexer.(*StringFieldIndex) }
func (e *extractor) strSlice() *StringSliceFieldIndex   { return e.indexer.(*StringSliceFieldIndex) }
func (e *extractor) strMap() *StringMapFieldIndex       { return e.indexer.(*StringMapFieldIndex) }
func (e *extractor) intIdx() *IntFieldIndex             { return e.indexer.(*IntFieldIndex) }
func (e *extractor) uintIdx() *UintFieldIndex           { return e.indexer.(*UintFieldIndex) }
func (e *extractor) boolIdx() *BoolFieldIndex           { return e.indexer.(*BoolFieldIndex) }
func (e *extractor) uuid() *UUIDFieldIndex              { return e.indexer.(*UUIDFieldIndex) }
func (e *extractor) fieldSet() *FieldSetIndex           { return e.indexer.(*FieldSetIndex) }
func (e *extractor) cond() *ConditionalIndex            { return e.indexer.(*ConditionalIndex) }
func (e *extractor) compound() *CompoundIndex           { return e.indexer.(*CompoundIndex) }
func (e *extractor) compoundMulti() *CompoundMultiIndex { return e.indexer.(*CompoundMultiIndex) }

// init points a zero extractor at an indexer and reports whether the indexer
// is one of the built-in kinds.
func (e *extractor) init(ix Indexer) bool {
	e.indexer = ix
	switch t := ix.(type) {
	case *StringFieldIndex:
		e.kind = extString
	case *StringSliceFieldIndex:
		e.kind = extStringSlice
	case *StringMapFieldIndex:
		e.kind = extStringMap
	case *IntFieldIndex:
		e.kind = extInt
	case *UintFieldIndex:
		e.kind = extUint
	case *BoolFieldIndex:
		e.kind = extBool
	case *UUIDFieldIndex:
		e.kind = extUUID
	case *FieldSetIndex:
		e.kind = extFieldSet
	case *ConditionalIndex:
		e.kind = extConditional
	case *CompoundIndex:
		e.kind = extCompound
	case *CompoundMultiIndex:
		e.kind = extCompoundMulti
	case typedIndexer:
		e.kind, e.typed = t.typedKind(), t
	default:
		e.kind = extCustom
		return false
	}
	return true
}

// compileExtractor builds the long-lived extractor of a schema index.
func compileExtractor(ix Indexer) *extractor {
	e := &extractor{field: new(atomic.Pointer[fieldInfo])}
	e.init(ix)
	var subIndexers []Indexer
	switch e.kind {
	case extCompound:
		subIndexers = e.compound().Indexes
	case extCompoundMulti:
		subIndexers = e.compoundMulti().Indexes
	default:
		return e
	}
	e.subs = make([]*extractor, len(subIndexers))
	for i, sub := range subIndexers {
		e.subs[i] = compileExtractor(sub)
	}
	return e
}

// subIndexers returns the user's current list of sub-indexers.
func (e *extractor) subIndexers() []Indexer {
	if e.kind == extCompoundMulti {
		return e.compoundMulti().Indexes
	}
	return e.compound().Indexes
}

// sub returns the extractor for sub-indexer i: the compiled one, or for a
// throw-away extractor one initialised in the caller's scratch.
func (e *extractor) sub(i int, scratch *extractor) *extractor {
	if e.subs != nil {
		return e.subs[i]
	}
	*scratch = extractor{}
	scratch.init(e.subIndexers()[i])
	return scratch
}

// subsCurrent reports whether the compiled sub-extractors still mirror the
// user's compound index (which is exported and therefore mutable).
func (e *extractor) subsCurrent() bool {
	if e.subs == nil {
		return true // resolved per call
	}
	current := e.subIndexers()
	if len(current) != len(e.subs) {
		return false
	}
	for i, sub := range current {
		if sub != e.subs[i].indexer {
			return false
		}
	}
	return true
}

// scalar reports whether the extractor yields one value from one struct field
// through appendScalar without running user code.
func (e *extractor) scalar() bool {
	switch e.kind {
	case extString, extInt, extUint, extBool, extUUID, extFieldSet,
		extTypedString, extTypedInt, extTypedUint, extTypedBool:
		return true
	}
	return false
}

// fieldInfo is a resolved (object type, field name) pair.
type fieldInfo struct {
	typ  reflect.Type
	name string

	// usable is false when the fast path cannot serve this pair at all.
	usable   bool
	kind     reflect.Kind // kind of the field
	elem     reflect.Kind // element kind of a pointer or slice field
	size     int          // bytes of an integer field
	exact    bool         // map[string]string or []byte exactly, not merely by kind
	exported bool         // reachable through exported fields only
	loc      fieldLoc     // build specific, see extract_unsafe.go / extract_safe.go
}

func (e *extractor) fieldOf(obj interface{}, name string) *fieldInfo {
	typ := reflect.TypeOf(obj)
	if e.field == nil {
		return lookupField(typ, name)
	}
	if fi := e.field.Load(); fi != nil && fi.typ == typ && fi.name == name {
		return fi
	}
	fi := lookupField(typ, name)
	e.field.Store(fi)
	return fi
}

// fieldCache is the process-wide second level behind every extractor's own
// one-entry cache. It is what makes the exported indexer methods fast: they
// run on throw-away extractors and would otherwise resolve their field by
// name on every call. Collisions simply overwrite each other.
var fieldCache [512]atomic.Pointer[fieldInfo]

func lookupField(typ reflect.Type, name string) *fieldInfo {
	h := typeHash(typ)
	for i := 0; i < len(name); i++ {
		h = h*31 + uintptr(name[i])
	}
	slot := &fieldCache[(h^h>>9)%uintptr(len(fieldCache))]
	if fi := slot.Load(); fi != nil && fi.typ == typ && fi.name == name {
		return fi
	}
	fi := resolveField(typ, name)
	slot.Store(fi)
	return fi
}

// resolveField locates a field the way reflect.Value.FieldByName does, and
// refuses anything the fast path must not touch.
func resolveField(typ reflect.Type, name string) *fieldInfo {
	fi := &fieldInfo{typ: typ, name: name}
	if typ == nil || typ.Kind() != reflect.Ptr || typ.Elem().Kind() != reflect.Struct {
		// Structs passed by value, non-structs and nil take the slow path.
		return fi
	}
	sf, ok := typ.Elem().FieldByName(name)
	if !ok {
		return fi
	}

	// Walk the (possibly promoted) path. Embedded pointers are left to
	// reflection: following one that is nil must panic like upstream.
	fi.exported = true
	t := typ.Elem()
	var offset uintptr
	for depth, i := range sf.Index {
		f := t.Field(i)
		if f.PkgPath != "" {
			fi.exported = false
		}
		offset += f.Offset
		if depth < len(sf.Index)-1 {
			if f.Type.Kind() != reflect.Struct {
				return fi
			}
			t = f.Type
		}
	}

	fi.kind = sf.Type.Kind()
	switch fi.kind {
	case reflect.Ptr, reflect.Slice:
		fi.elem = sf.Type.Elem().Kind()
		fi.exact = sf.Type == reflect.TypeOf([]byte(nil))
	case reflect.Map:
		fi.exact = sf.Type == reflect.TypeOf(map[string]string(nil))
	}
	if size, ok := IsIntType(fi.kind); ok {
		fi.size = size
	} else if size, ok := IsUintType(fi.kind); ok {
		fi.size = size
	}
	fi.loc = newFieldLoc(offset, sf.Index)
	fi.usable = true
	return fi
}

// appendLower appends strings.ToLower(s).
func appendLower(dst []byte, s string) []byte {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			// Unicode case mapping: defer to the real thing.
			return append(dst, strings.ToLower(s)...)
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		dst = append(dst, c)
	}
	return dst
}

func appendString(dst []byte, s string, lowercase bool) []byte {
	if lowercase {
		return appendLower(dst, s)
	}
	return append(dst, s...)
}

// appendInt appends what encodeInt returns.
func appendInt(dst []byte, val int64, size int) []byte {
	scaled := val ^ int64(-1<<(size*8-1))
	return appendUint(dst, uint64(scaled), size)
}

// appendUint appends what encodeUInt returns.
func appendUint(dst []byte, val uint64, size int) []byte {
	switch size {
	case 1:
		return append(dst, uint8(val))
	case 2:
		return binary.BigEndian.AppendUint16(dst, uint16(val))
	case 4:
		return binary.BigEndian.AppendUint32(dst, uint32(val))
	default:
		return binary.BigEndian.AppendUint64(dst, val)
	}
}

func hexNibble(c byte) (byte, bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// appendUUID appends what UUIDFieldIndex.parseString returns for every input
// that function accepts, and reports false -- leaving the verdict and the
// error message to parseString -- for every input it rejects.
func appendUUID(dst []byte, s string, enforceLength bool) ([]byte, bool) {
	if len(s) > 36 || (enforceLength && len(s) != 36) {
		return dst, false
	}
	start := len(dst)
	hyphens, nibbles := 0, 0
	var hi byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '-' {
			hyphens++
			continue
		}
		v, ok := hexNibble(c)
		if !ok {
			return dst[:start], false
		}
		if nibbles%2 == 0 {
			hi = v
		} else {
			dst = append(dst, hi<<4|v)
		}
		nibbles++
	}
	if hyphens > 4 || nibbles%2 != 0 {
		return dst[:start], false
	}
	return dst, true
}

// appendObject appends the single index value of obj.
//
// handled=false means "ask the exported FromObject instead"; dst is then
// returned unchanged. With handled=true, ok mirrors FromObject's first result.
func (e *extractor) appendObject(dst []byte, obj interface{}) (out []byte, ok bool, handled bool) {
	out, ok, handled, _ = e.appendObjectErr(dst, obj)
	return out, ok, handled
}

// appendObjectErr is appendObject for the one kind that can fail after doing
// observable work: a ConditionalIndex whose user function returned an error.
// That function must not be called a second time by a fallback, so its error
// is produced here, worded exactly as ConditionalIndex.FromObject words it.
func (e *extractor) appendObjectErr(dst []byte, obj interface{}) (out []byte, ok bool, handled bool, err error) {
	if e.kind != extConditional {
		out, ok, handled = e.appendScalar(dst, obj)
		return out, ok, handled, nil
	}
	if e.cond().Conditional == nil {
		return dst, false, false, nil
	}
	res, err := e.cond().Conditional(obj)
	if err != nil {
		return dst, false, true, fmt.Errorf("ConditionalIndexFunc(%#v) failed: %v", obj, err)
	}
	if res {
		return append(dst, 1), true, true, nil
	}
	return append(dst, 0), true, true, nil
}

func (e *extractor) appendScalar(dst []byte, obj interface{}) (out []byte, ok bool, handled bool) {
	switch e.kind {
	case extString:
		fi := e.fieldOf(obj, e.str().Field)
		if !fi.usable || isNilObject(obj) {
			return dst, false, false
		}
		var val string
		switch {
		case fi.kind == reflect.String:
			val = readString(obj, fi)
		case fi.kind == reflect.Ptr && fi.elem == reflect.String:
			p := readStringPtr(obj, fi)
			if p == nil {
				return dst, false, true
			}
			val = *p
		default:
			return dst, false, false
		}
		if val == "" {
			return dst, false, true
		}
		dst = appendString(dst, val, e.str().Lowercase)
		return append(dst, 0), true, true

	case extInt:
		fi := e.fieldOf(obj, e.intIdx().Field)
		if !fi.usable || isNilObject(obj) {
			return dst, false, false
		}
		if _, isInt := IsIntType(fi.kind); !isInt {
			return dst, false, false
		}
		return appendInt(dst, readInt(obj, fi), fi.size), true, true

	case extUint:
		fi := e.fieldOf(obj, e.uintIdx().Field)
		if !fi.usable || isNilObject(obj) {
			return dst, false, false
		}
		if _, isUint := IsUintType(fi.kind); !isUint {
			return dst, false, false
		}
		return appendUint(dst, readUint(obj, fi), fi.size), true, true

	case extBool:
		fi := e.fieldOf(obj, e.boolIdx().Field)
		if !fi.usable || fi.kind != reflect.Bool || isNilObject(obj) {
			return dst, false, false
		}
		if readBool(obj, fi) {
			return append(dst, 1), true, true
		}
		return append(dst, 0), true, true

	case extUUID:
		fi := e.fieldOf(obj, e.uuid().Field)
		if !fi.usable || fi.kind != reflect.String || isNilObject(obj) {
			return dst, false, false
		}
		val := readString(obj, fi)
		if val == "" {
			return dst, false, true
		}
		if out, ok := appendUUID(dst, val, true); ok {
			return out, true, true
		}
		return dst, false, false

	case extFieldSet:
		fi := e.fieldOf(obj, e.fieldSet().Field)
		if !fi.usable || !fi.exported || isNilObject(obj) {
			return dst, false, false
		}
		var set bool
		switch fi.kind {
		case reflect.Ptr:
			set = !readPtrIsNil(obj, fi)
		case reflect.String:
			set = readString(obj, fi) != ""
		case reflect.Bool:
			set = readBool(obj, fi)
		default:
			// Everything else is compared through interfaces upstream, which
			// has its own rules (and panics for uncomparable types).
			return dst, false, false
		}
		if set {
			return append(dst, 1), true, true
		}
		return append(dst, 0), true, true

	case extTypedString:
		val, handled := e.typed.stringOf(obj)
		if !handled {
			return dst, false, false
		}
		if val == "" {
			return dst, false, true
		}
		dst = appendString(dst, val, e.typed.lower())
		return append(dst, 0), true, true

	case extTypedInt:
		val, handled := e.typed.intOf(obj)
		if !handled {
			return dst, false, false
		}
		return appendInt(dst, val, e.typed.width()), true, true

	case extTypedUint:
		val, handled := e.typed.uintOf(obj)
		if !handled {
			return dst, false, false
		}
		return appendUint(dst, val, e.typed.width()), true, true

	case extTypedBool:
		val, handled := e.typed.intOf(obj)
		if !handled {
			return dst, false, false
		}
		return append(dst, byte(val)), true, true

	case extCompound:
		if !e.subsCurrent() {
			return dst, false, false
		}
		start := len(dst)
		var scratch extractor
		for i := range e.compound().Indexes {
			sub := e.sub(i, &scratch)
			if !sub.scalar() {
				// Nesting, user code and multi-valued sub-indexers stay
				// on the one obviously correct path.
				return dst[:start], false, false
			}
			out, ok, handled := sub.appendScalar(dst, obj)
			if !handled {
				return dst[:start], false, false
			}
			if !ok {
				if e.compound().AllowMissing {
					break
				}
				return dst[:start], false, true
			}
			dst = out
		}
		return dst, true, true
	}
	return dst, false, false
}

// maxCompoundDepth bounds the sub-indexers a CompoundMultiIndex may have on
// the fast path (the traversal below keeps its state in fixed arrays).
const maxCompoundDepth = 8

// appendKeys appends the keys of obj to kl: one per index value, each
// followed by suffix (the primary key, for non-unique indexes). tmp is scratch
// space for compound multi-indexes; without it they are not handled.
func (e *extractor) appendKeys(kl, tmp *keyList, obj interface{}, suffix []byte) (ok bool, handled bool, err error) {
	switch e.kind {
	case extCompoundMulti:
		if tmp == nil || !e.subsCurrent() || len(e.compoundMulti().Indexes) > maxCompoundDepth {
			return false, false, nil
		}

		// Collect the values of every sub-indexer; bounds[d]..bounds[d+1]
		// are the values at depth d.
		tmp.reset()
		var bounds [maxCompoundDepth + 1]int
		var scratch extractor
		levels := 0
		for i := range e.compoundMulti().Indexes {
			sub := e.sub(i, &scratch)
			var subOK, subHandled bool
			switch {
			case sub.scalar():
				var out []byte
				out, subOK, subHandled = sub.appendScalar(tmp.buf, obj)
				if subHandled && subOK {
					tmp.buf = out
					tmp.end()
				}
			case sub.kind == extStringSlice || sub.kind == extStringMap || sub.kind == extTypedStrings:
				subOK, subHandled, _ = sub.appendKeys(tmp, nil, obj, nil)
			}
			if !subHandled {
				return false, false, nil
			}
			if !subOK {
				if e.compoundMulti().AllowMissing {
					break
				}
				return false, true, nil
			}
			levels++
			bounds[levels] = tmp.len()
		}

		// Size the output exactly. With c[d] values of total length l[d] at
		// depth d, the keys of depth k number c[0]*...*c[k], and every value
		// of depth d <= k occurs in (that number / c[d]) of them.
		size, keys := 0, 0
		first := levels - 1
		if e.compoundMulti().AllowMissing {
			first = 0
		}
		for k := first; k < levels; k++ {
			count := 1
			for d := 0; d <= k; d++ {
				count *= bounds[d+1] - bounds[d]
			}
			for d := 0; d <= k; d++ {
				c := bounds[d+1] - bounds[d]
				l := tmp.ends[bounds[d+1]-1]
				if bounds[d] > 0 {
					l -= tmp.ends[bounds[d]-1]
				}
				size += l * (count / c)
			}
			size += count * len(suffix)
			keys += count
		}
		if levels > 0 {
			kl.reserve(size, keys)
		}

		// Depth-first product in upstream's order. With AllowMissing every
		// proper prefix is a key too. (Upstream builds those prefixes with
		// append on a shared slice, so with three or more levels a sibling
		// can overwrite a prefix that was already emitted; here every key
		// is written out in full, which is what upstream intends.)
		var pos, plen [maxCompoundDepth]int
		var pfxBuf [2 * keyScratch]byte
		pfx := pfxBuf[:0]
		for depth := 0; depth >= 0 && levels > 0; {
			if pos[depth] == bounds[depth+1]-bounds[depth] {
				pos[depth] = 0
				depth--
				if depth >= 0 {
					pfx = pfx[:plen[depth]]
				}
				continue
			}
			v := tmp.key(bounds[depth] + pos[depth])
			pos[depth]++
			if depth == levels-1 {
				kl.buf = append(append(append(kl.buf, pfx...), v...), suffix...)
				kl.end()
				continue
			}
			plen[depth] = len(pfx)
			pfx = append(pfx, v...)
			if e.compoundMulti().AllowMissing {
				kl.buf = append(append(kl.buf, pfx...), suffix...)
				kl.end()
			}
			depth++
		}
		return true, true, nil

	case extStringSlice:
		fi := e.fieldOf(obj, e.strSlice().Field)
		if !fi.usable || fi.kind != reflect.Slice || fi.elem != reflect.String || isNilObject(obj) {
			return false, false, nil
		}
		vals, readable := readStrings(obj, fi)
		if !readable {
			return false, false, nil
		}
		size, keys := 0, 0
		for _, val := range vals {
			if val != "" {
				size += len(val) + 1 + len(suffix)
				keys++
			}
		}
		kl.reserve(size, keys)
		for _, val := range vals {
			if val == "" {
				continue
			}
			kl.buf = appendString(kl.buf, val, e.strSlice().Lowercase)
			kl.buf = append(append(kl.buf, 0), suffix...)
			kl.end()
			ok = true
		}
		return ok, true, nil

	case extTypedStrings:
		vals, handled := e.typed.stringsOf(obj)
		if !handled {
			return false, false, nil
		}
		lowercase := e.typed.lower()
		size, keys := 0, 0
		for _, val := range vals {
			if val != "" {
				size += len(val) + 1 + len(suffix)
				keys++
			}
		}
		kl.reserve(size, keys)
		for _, val := range vals {
			if val == "" {
				continue
			}
			kl.buf = appendString(kl.buf, val, lowercase)
			kl.buf = append(append(kl.buf, 0), suffix...)
			kl.end()
			ok = true
		}
		return ok, true, nil

	case extStringMap:
		fi := e.fieldOf(obj, e.strMap().Field)
		if !fi.usable || !fi.exact || fi.kind != reflect.Map || isNilObject(obj) {
			return false, false, nil
		}
		m, readable := readStringMap(obj, fi)
		if !readable {
			return false, false, nil
		}
		size, keys := 0, 0
		for k, v := range m {
			if k != "" {
				size += len(k) + len(v) + 2 + len(suffix)
				keys++
			}
		}
		kl.reserve(size, keys)
		for k, v := range m {
			if k == "" {
				continue
			}
			kl.buf = appendString(kl.buf, k, e.strMap().Lowercase)
			kl.buf = append(kl.buf, 0)
			kl.buf = appendString(kl.buf, v, e.strMap().Lowercase)
			kl.buf = append(append(kl.buf, 0), suffix...)
			kl.end()
			ok = true
		}
		return ok, true, nil

	case extCustom:
		return false, false, nil
	}

	start := len(kl.buf)
	out, ok, handled, err := e.appendObjectErr(kl.buf, obj)
	if !handled || !ok || err != nil {
		kl.buf = out[:start]
		return ok, handled, err
	}
	kl.buf = append(out, suffix...)
	kl.end()
	return true, true, nil
}

// appendArgs appends the key for query arguments: what FromArgs returns or,
// with prefix set, what PrefixFromArgs returns. handled=false defers to those
// methods, which also own every error message.
func (e *extractor) appendArgs(dst []byte, args []interface{}, prefix bool) ([]byte, bool) {
	switch e.kind {
	case extString, extStringSlice, extTypedString, extTypedStrings:
		if len(args) != 1 {
			return dst, false
		}
		arg, ok := args[0].(string)
		if !ok {
			return dst, false
		}
		// (Spelled out rather than left to appendStringArg: this is the path
		// of nearly every query, and the call is not inlined.)
		var lowercase bool
		switch e.kind {
		case extString:
			lowercase = e.str().Lowercase
		case extStringSlice:
			lowercase = e.strSlice().Lowercase
		default:
			lowercase = e.typed.lower()
		}
		dst = appendString(dst, arg, lowercase)
		if prefix {
			// PrefixFromArgs strips the terminator again.
			return dst, true
		}
		return append(dst, 0), true

	case extTypedInt:
		if prefix || len(args) != 1 {
			return dst, false
		}
		// Any signed integer type, at the width of the index.
		val, ok := signedArg(args[0])
		size := e.typed.width()
		if !ok || (size < 8 && (val < -1<<(8*size-1) || val > 1<<(8*size-1)-1)) {
			return dst, false
		}
		return appendInt(dst, val, size), true

	case extTypedUint:
		if prefix || len(args) != 1 {
			return dst, false
		}
		val, ok := unsignedArg(args[0])
		size := e.typed.width()
		if !ok || (size < 8 && val > 1<<(8*size)-1) {
			return dst, false
		}
		return appendUint(dst, val, size), true

	case extStringMap:
		if prefix || len(args) == 0 || len(args) > 2 {
			return dst, false
		}
		start := len(dst)
		for _, a := range args {
			s, ok := a.(string)
			if !ok {
				return dst[:start], false
			}
			dst = append(appendString(dst, s, e.strMap().Lowercase), 0)
		}
		return dst, true

	case extInt:
		if prefix || len(args) != 1 {
			return dst, false
		}
		// Keys are as wide as the ARGUMENT's type, exactly like upstream.
		switch v := args[0].(type) {
		case int:
			return appendInt(dst, int64(v), strconv.IntSize/8), true
		case int8:
			return appendInt(dst, int64(v), 1), true
		case int16:
			return appendInt(dst, int64(v), 2), true
		case int32:
			return appendInt(dst, int64(v), 4), true
		case int64:
			return appendInt(dst, v, 8), true
		}
		return dst, false

	case extUint:
		if prefix || len(args) != 1 {
			return dst, false
		}
		switch v := args[0].(type) {
		case uint:
			return appendUint(dst, uint64(v), strconv.IntSize/8), true
		case uint8:
			return appendUint(dst, uint64(v), 1), true
		case uint16:
			return appendUint(dst, uint64(v), 2), true
		case uint32:
			return appendUint(dst, uint64(v), 4), true
		case uint64:
			return appendUint(dst, v, 8), true
		}
		return dst, false

	case extBool, extFieldSet, extConditional, extTypedBool:
		if prefix || len(args) != 1 {
			return dst, false
		}
		v, ok := args[0].(bool)
		if !ok {
			return dst, false
		}
		if v {
			return append(dst, 1), true
		}
		return append(dst, 0), true

	case extUUID:
		if len(args) != 1 {
			return dst, false
		}
		switch v := args[0].(type) {
		case string:
			return appendUUID(dst, v, !prefix)
		case []byte:
			if !prefix && len(v) != 16 {
				return dst, false
			}
			return append(dst, v...), true
		}
		return dst, false

	case extCompound:
		if !e.subsCurrent() {
			return dst, false
		}
		if prefix {
			if len(args) > len(e.compound().Indexes) {
				return dst, false
			}
		} else if len(args) != len(e.compound().Indexes) {
			return dst, false
		}
		start := len(dst)
		var scratch extractor
		for i := range args {
			sub := e.sub(i, &scratch)
			if sub.kind == extCompound || sub.kind == extCompoundMulti || sub.kind == extCustom {
				return dst[:start], false
			}
			last := prefix && i+1 == len(args)
			if last && sub.kind != extString && sub.kind != extStringSlice && sub.kind != extUUID &&
				sub.kind != extTypedString && sub.kind != extTypedStrings {
				// Not a PrefixIndexer: upstream reports an error.
				return dst[:start], false
			}
			out, ok := sub.appendArgs(dst, args[i:i+1], last)
			if !ok {
				return dst[:start], false
			}
			dst = out
		}
		return dst, true
	}
	return dst, false
}

// appendStringArg is appendArgs for the one argument shape that typed keys
// (typed.go) have: a single string. It handles the index kinds whose argument
// is a string.
func (e *extractor) appendStringArg(dst []byte, arg string, prefix bool) ([]byte, bool) {
	var lowercase bool
	switch e.kind {
	case extString:
		lowercase = e.str().Lowercase
	case extStringSlice:
		lowercase = e.strSlice().Lowercase
	case extTypedString, extTypedStrings:
		lowercase = e.typed.lower()
	case extUUID:
		return appendUUID(dst, arg, !prefix)
	default:
		return dst, false
	}
	dst = appendString(dst, arg, lowercase)
	if prefix {
		// PrefixFromArgs strips the terminator again.
		return dst, true
	}
	return append(dst, 0), true
}
