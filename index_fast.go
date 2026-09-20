// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package memdb

import "strconv"

// The exported methods of the built-in indexers.
//
// Each one first offers its input to the extractor machinery of extract.go,
// through a throw-away extractor on the stack (field resolution then comes
// from the process-wide field cache below). If the fast path claims the input
// AND produced a value, the result is copied into a single exact allocation
// and returned. In every other case -- not claimed, no value, error -- the
// original upstream implementation in index.go ("...Slow") decides, so return
// values, error messages, quirks and panics stay exactly upstream's.
//
// Transactions do not come through here for built-in indexers: they use the
// compiled, per-index extractors directly and allocate nothing.

// copyKey returns an exactly sized copy of a key built in scratch space. The
// result is never nil, like the slices upstream builds from strings.
func copyKey(k []byte) []byte {
	out := make([]byte, len(k))
	copy(out, k)
	return out
}

func (s *StringFieldIndex) FromObject(obj interface{}) (bool, []byte, error) {
	e := extractor{kind: extString, indexer: s}
	var scratch [keyScratch]byte
	if out, ok, handled := e.appendScalar(scratch[:0], obj); handled && ok {
		return true, copyKey(out), nil
	}
	return s.fromObjectSlow(obj)
}

// stringArg is the well-formed argument list of a string index: one string.
// The key is built straight into its own allocation.
func stringArg(args []interface{}, lowercase, terminate bool) ([]byte, bool) {
	if len(args) != 1 {
		return nil, false
	}
	arg, ok := args[0].(string)
	if !ok {
		return nil, false
	}
	out := appendString(make([]byte, 0, len(arg)+1), arg, lowercase)
	if terminate {
		out = append(out, 0)
	}
	return out, true
}

func (s *StringFieldIndex) FromArgs(args ...interface{}) ([]byte, error) {
	if out, ok := stringArg(args, s.Lowercase, true); ok {
		return out, nil
	}
	return s.fromArgsSlow(args...)
}

func (s *StringFieldIndex) PrefixFromArgs(args ...interface{}) ([]byte, error) {
	if out, ok := stringArg(args, s.Lowercase, false); ok {
		return out, nil
	}
	return s.prefixFromArgsSlow(args...)
}

func (s *StringSliceFieldIndex) FromObject(obj interface{}) (bool, [][]byte, error) {
	e := extractor{kind: extStringSlice, indexer: s}
	var kl keyList
	if ok, handled, _ := e.appendKeys(&kl, nil, obj, nil); handled && ok {
		return true, kl.split(), nil
	}
	return s.fromObjectSlow(obj)
}

func (s *StringSliceFieldIndex) FromArgs(args ...interface{}) ([]byte, error) {
	if out, ok := stringArg(args, s.Lowercase, true); ok {
		return out, nil
	}
	return s.fromArgsSlow(args...)
}

func (s *StringSliceFieldIndex) PrefixFromArgs(args ...interface{}) ([]byte, error) {
	if out, ok := stringArg(args, s.Lowercase, false); ok {
		return out, nil
	}
	return s.prefixFromArgsSlow(args...)
}

func (s *StringMapFieldIndex) FromObject(obj interface{}) (bool, [][]byte, error) {
	e := extractor{kind: extStringMap, indexer: s}
	var kl keyList
	if ok, handled, _ := e.appendKeys(&kl, nil, obj, nil); handled && ok {
		return true, kl.split(), nil
	}
	return s.fromObjectSlow(obj)
}

func (s *StringMapFieldIndex) FromArgs(args ...interface{}) ([]byte, error) {
	e := extractor{kind: extStringMap, indexer: s}
	var scratch [keyScratch]byte
	if out, ok := e.appendArgs(scratch[:0], args, false); ok {
		return copyKey(out), nil
	}
	return s.fromArgsSlow(args...)
}

func (i *IntFieldIndex) FromObject(obj interface{}) (bool, []byte, error) {
	e := extractor{kind: extInt, indexer: i}
	var scratch [8]byte
	if out, ok, handled := e.appendScalar(scratch[:0], obj); handled && ok {
		return true, copyKey(out), nil
	}
	return i.fromObjectSlow(obj)
}

func (i *IntFieldIndex) FromArgs(args ...interface{}) ([]byte, error) {
	// Upstream gets here through reflect.ValueOf, which is already cheap, so
	// this path has to be direct: a type switch and upstream's own encoder.
	if len(args) == 1 {
		switch v := args[0].(type) {
		case int:
			return encodeInt(int64(v), strconv.IntSize/8), nil
		case int8:
			return encodeInt(int64(v), 1), nil
		case int16:
			return encodeInt(int64(v), 2), nil
		case int32:
			return encodeInt(int64(v), 4), nil
		case int64:
			return encodeInt(v, 8), nil
		}
	}
	return i.fromArgsSlow(args...)
}

func (u *UintFieldIndex) FromObject(obj interface{}) (bool, []byte, error) {
	e := extractor{kind: extUint, indexer: u}
	var scratch [8]byte
	if out, ok, handled := e.appendScalar(scratch[:0], obj); handled && ok {
		return true, copyKey(out), nil
	}
	return u.fromObjectSlow(obj)
}

func (u *UintFieldIndex) FromArgs(args ...interface{}) ([]byte, error) {
	if len(args) == 1 {
		switch v := args[0].(type) {
		case uint:
			return encodeUInt(uint64(v), strconv.IntSize/8), nil
		case uint8:
			return encodeUInt(uint64(v), 1), nil
		case uint16:
			return encodeUInt(uint64(v), 2), nil
		case uint32:
			return encodeUInt(uint64(v), 4), nil
		case uint64:
			return encodeUInt(v, 8), nil
		}
	}
	return u.fromArgsSlow(args...)
}

func (i *BoolFieldIndex) FromObject(obj interface{}) (bool, []byte, error) {
	e := extractor{kind: extBool, indexer: i}
	var scratch [1]byte
	if out, ok, handled := e.appendScalar(scratch[:0], obj); handled && ok {
		return true, copyKey(out), nil
	}
	return i.fromObjectSlow(obj)
}

func (u *UUIDFieldIndex) FromObject(obj interface{}) (bool, []byte, error) {
	e := extractor{kind: extUUID, indexer: u}
	var scratch [16]byte
	if out, ok, handled := e.appendScalar(scratch[:0], obj); handled && ok {
		return true, copyKey(out), nil
	}
	return u.fromObjectSlow(obj)
}

func (u *UUIDFieldIndex) FromArgs(args ...interface{}) ([]byte, error) {
	// A []byte argument is returned as is by upstream, not copied: leave it.
	if len(args) == 1 {
		if s, ok := args[0].(string); ok {
			return u.parseString(s, true)
		}
	}
	return u.fromArgsSlow(args...)
}

func (u *UUIDFieldIndex) PrefixFromArgs(args ...interface{}) ([]byte, error) {
	if len(args) == 1 {
		if s, ok := args[0].(string); ok {
			return u.parseString(s, false)
		}
	}
	return u.prefixFromArgsSlow(args...)
}

// parseString parses a UUID from the string. If enforceLength is false, it will
// parse a partial UUID. An error is returned if the input, stripped of hyphens,
// is not even length.
func (u *UUIDFieldIndex) parseString(s string, enforceLength bool) ([]byte, error) {
	var scratch [18]byte
	if out, ok := appendUUID(scratch[:0], s, enforceLength); ok {
		return copyKey(out), nil
	}
	return u.parseStringSlow(s, enforceLength)
}

func (f *FieldSetIndex) FromObject(obj interface{}) (bool, []byte, error) {
	e := extractor{kind: extFieldSet, indexer: f}
	var scratch [1]byte
	if out, ok, handled := e.appendScalar(scratch[:0], obj); handled && ok {
		return true, copyKey(out), nil
	}
	return f.fromObjectSlow(obj)
}

func (c *CompoundIndex) FromObject(raw interface{}) (bool, []byte, error) {
	e := extractor{kind: extCompound, indexer: c}
	var scratch [2 * keyScratch]byte
	if out, ok, handled := e.appendScalar(scratch[:0], raw); handled && ok && len(out) > 0 {
		return true, copyKey(out), nil
	}
	return c.fromObjectSlow(raw)
}

func (c *CompoundIndex) FromArgs(args ...interface{}) ([]byte, error) {
	e := extractor{kind: extCompound, indexer: c}
	var scratch [2 * keyScratch]byte
	if out, ok := e.appendArgs(scratch[:0], args, false); ok && len(out) > 0 {
		return copyKey(out), nil
	}
	return c.fromArgsSlow(args...)
}

func (c *CompoundIndex) PrefixFromArgs(args ...interface{}) ([]byte, error) {
	e := extractor{kind: extCompound, indexer: c}
	var scratch [2 * keyScratch]byte
	if out, ok := e.appendArgs(scratch[:0], args, true); ok && len(out) > 0 {
		return copyKey(out), nil
	}
	return c.prefixFromArgsSlow(args...)
}

func (c *CompoundMultiIndex) FromObject(raw interface{}) (bool, [][]byte, error) {
	e := extractor{kind: extCompoundMulti, indexer: c}
	// The intermediate values get a modest buffer up front (a key list is
	// filled through a pointer, so it cannot live on the stack); the keys
	// themselves are sized exactly.
	var kl, tmp keyList
	tmp.reserve(96, 8)
	if ok, handled, _ := e.appendKeys(&kl, &tmp, raw, nil); handled && ok {
		return true, kl.split(), nil
	}
	return c.fromObjectSlow(raw)
}
