// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

//go:build !memdb_safe && !purego

package memdb

import (
	"reflect"
	"unsafe"
)

// Field access by offset. This is the default build; compile with the
// memdb_safe (or purego) tag for the reflection-only variant.
//
// Everything here reads a field of a struct through a pointer that came out of
// an interface value holding a non-nil pointer-to-struct (callers check both),
// at an offset reflect reported for exactly that struct type. The struct is
// kept alive by the interface value for the duration of the read.

// unsafeFields reports which build this is. Used by tests.
const unsafeFields = true

// fieldLoc locates a field inside its struct.
type fieldLoc struct {
	offset uintptr
}

func newFieldLoc(offset uintptr, _ []int) fieldLoc {
	return fieldLoc{offset: offset}
}

// eface is the runtime representation of an empty interface.
type eface struct {
	typ  unsafe.Pointer
	data unsafe.Pointer
}

// objPointer returns the pointer stored in an interface that holds a pointer.
func objPointer(obj interface{}) unsafe.Pointer {
	return (*eface)(unsafe.Pointer(&obj)).data
}

func isNilObject(obj interface{}) bool {
	return objPointer(obj) == nil
}

func fieldPointer(obj interface{}, fi *fieldInfo) unsafe.Pointer {
	return unsafe.Add(objPointer(obj), fi.loc.offset)
}

func readString(obj interface{}, fi *fieldInfo) string {
	return *(*string)(fieldPointer(obj, fi))
}

func readStringPtr(obj interface{}, fi *fieldInfo) *string {
	return *(**string)(fieldPointer(obj, fi))
}

func readPtrIsNil(obj interface{}, fi *fieldInfo) bool {
	return *(*unsafe.Pointer)(fieldPointer(obj, fi)) == nil
}

func readBool(obj interface{}, fi *fieldInfo) bool {
	return *(*bool)(fieldPointer(obj, fi))
}

func readInt(obj interface{}, fi *fieldInfo) int64 {
	p := fieldPointer(obj, fi)
	switch fi.size {
	case 1:
		return int64(*(*int8)(p))
	case 2:
		return int64(*(*int16)(p))
	case 4:
		return int64(*(*int32)(p))
	default:
		return *(*int64)(p)
	}
}

func readUint(obj interface{}, fi *fieldInfo) uint64 {
	p := fieldPointer(obj, fi)
	switch fi.size {
	case 1:
		return uint64(*(*uint8)(p))
	case 2:
		return uint64(*(*uint16)(p))
	case 4:
		return uint64(*(*uint32)(p))
	default:
		return *(*uint64)(p)
	}
}

// readStrings reads a slice whose elements have kind string. Named element
// types share the layout of string, so the slice can be viewed as []string.
func readStrings(obj interface{}, fi *fieldInfo) ([]string, bool) {
	return *(*[]string)(fieldPointer(obj, fi)), true
}

// readStringMap reads a field whose type is exactly map[string]string.
func readStringMap(obj interface{}, fi *fieldInfo) (map[string]string, bool) {
	return *(*map[string]string)(fieldPointer(obj, fi)), true
}

// typeHash returns a cheap hash of a type: its descriptor's address.
func typeHash(typ reflect.Type) uintptr {
	return uintptr((*eface)(unsafe.Pointer(&typ)).data) >> 4
}
