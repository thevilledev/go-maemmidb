// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package memdb

import "iter"

// This file is an extension: go-memdb has no counterpart.

// All adapts a ResultIterator to a range-over-func iterator:
//
//	it, err := txn.Get("person", "age", 30)
//	if err != nil {
//		return err
//	}
//	for obj := range memdb.All(it) {
//		p := obj.(*Person)
//		...
//	}
//
// It works with every ResultIterator, including a FilterIterator. The sequence
// consumes the iterator: it yields what Next has not returned yet and can be
// ranged over once. Breaking out of the loop leaves the rest in the iterator.
func All(it ResultIterator) iter.Seq[interface{}] {
	return func(yield func(interface{}) bool) {
		for obj := it.Next(); obj != nil; obj = it.Next() {
			if !yield(obj) {
				return
			}
		}
	}
}

// AllOf is All with every result asserted to T, which is normally the pointer
// type the table stores:
//
//	for p := range memdb.AllOf[*Person](it) {
//		...
//	}
//
// Like a plain type assertion, it panics on an object of another type.
func AllOf[T any](it ResultIterator) iter.Seq[T] {
	return func(yield func(T) bool) {
		for obj := it.Next(); obj != nil; obj = it.Next() {
			if !yield(obj.(T)) {
				return
			}
		}
	}
}
