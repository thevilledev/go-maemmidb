// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

//go:build memdb_safe || purego

package memdb

import "reflect"

// Field access through reflection only: no package unsafe. Still much faster
// than the exported indexer methods, because the field is addressed by its
// cached index path instead of being searched by name on every call.

// unsafeFields reports which build this is. Used by tests.
const unsafeFields = false

// fieldLoc locates a field inside its struct.
type fieldLoc struct {
	index []int
}

func newFieldLoc(_ uintptr, index []int) fieldLoc {
	return fieldLoc{index: index}
}

func isNilObject(obj interface{}) bool {
	return reflect.ValueOf(obj).IsNil()
}

func fieldValue(obj interface{}, fi *fieldInfo) reflect.Value {
	v := reflect.ValueOf(obj).Elem()
	if len(fi.loc.index) == 1 {
		return v.Field(fi.loc.index[0])
	}
	return v.FieldByIndex(fi.loc.index)
}

func readString(obj interface{}, fi *fieldInfo) string {
	return fieldValue(obj, fi).String()
}

func readStringPtr(obj interface{}, fi *fieldInfo) *string {
	v := fieldValue(obj, fi)
	if v.IsNil() {
		return nil
	}
	s := v.Elem().String()
	return &s
}

func readPtrIsNil(obj interface{}, fi *fieldInfo) bool {
	return fieldValue(obj, fi).IsNil()
}

func readBool(obj interface{}, fi *fieldInfo) bool {
	return fieldValue(obj, fi).Bool()
}

func readInt(obj interface{}, fi *fieldInfo) int64 {
	return fieldValue(obj, fi).Int()
}

func readUint(obj interface{}, fi *fieldInfo) uint64 {
	return fieldValue(obj, fi).Uint()
}

// Slices and maps are left to the exported indexer methods in this build.

func readStrings(interface{}, *fieldInfo) ([]string, bool) { return nil, false }

func readStringMap(interface{}, *fieldInfo) (map[string]string, bool) { return nil, false }

// typeHash returns a cheap hash of a type: its descriptor's address.
func typeHash(typ reflect.Type) uintptr {
	if typ == nil {
		return 0
	}
	return reflect.ValueOf(typ).Pointer() >> 4
}
