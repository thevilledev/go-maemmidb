// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package bench

import (
	"context"
	"reflect"
	"time"
)

// API lock. This file compiles against whichever implementation the build tag
// selects, so it only builds if BOTH implementations expose exactly the same
// exported API: every method expression below pins a full signature of the
// real (aliased) type, not of a wrapper.
var (
	_ func(*MemDB, bool) *Txn         = (*MemDB).Txn
	_ func(*MemDB) *MemDB             = (*MemDB).Snapshot
	_ func(*MemDB) *DBSchema          = (*MemDB).DBSchema
	_ func(*DBSchema) error           = (*DBSchema).Validate
	_ func(*TableSchema) error        = (*TableSchema).Validate
	_ func(*IndexSchema) error        = (*IndexSchema).Validate
	_ func(*DBSchema) (*MemDB, error) = NewMemDB

	_ func(*Txn)                                                                       = (*Txn).TrackChanges
	_ func(*Txn)                                                                       = (*Txn).Abort
	_ func(*Txn)                                                                       = (*Txn).Commit
	_ func(*Txn, string, interface{}) error                                            = (*Txn).Insert
	_ func(*Txn, string, interface{}) error                                            = (*Txn).Delete
	_ func(*Txn, string, string, string) (bool, error)                                 = (*Txn).DeletePrefix
	_ func(*Txn, string, string, ...interface{}) (int, error)                          = (*Txn).DeleteAll
	_ func(*Txn, string, string, ...interface{}) (<-chan struct{}, interface{}, error) = (*Txn).FirstWatch
	_ func(*Txn, string, string, ...interface{}) (<-chan struct{}, interface{}, error) = (*Txn).LastWatch
	_ func(*Txn, string, string, ...interface{}) (interface{}, error)                  = (*Txn).First
	_ func(*Txn, string, string, ...interface{}) (interface{}, error)                  = (*Txn).Last
	_ func(*Txn, string, string, ...interface{}) (interface{}, error)                  = (*Txn).LongestPrefix
	_ func(*Txn, string, string, ...interface{}) (ResultIterator, error)               = (*Txn).Get
	_ func(*Txn, string, string, ...interface{}) (ResultIterator, error)               = (*Txn).GetReverse
	_ func(*Txn, string, string, ...interface{}) (ResultIterator, error)               = (*Txn).LowerBound
	_ func(*Txn, string, string, ...interface{}) (ResultIterator, error)               = (*Txn).ReverseLowerBound
	_ func(*Txn) Changes                                                               = (*Txn).Changes
	_ func(*Txn, func())                                                               = (*Txn).Defer
	_ func(*Txn) *Txn                                                                  = (*Txn).Snapshot

	_ func(*Change) bool = (*Change).Created
	_ func(*Change) bool = (*Change).Updated
	_ func(*Change) bool = (*Change).Deleted

	_ func(ResultIterator, FilterFunc) *FilterIterator = NewFilterIterator
	_ func(*FilterIterator) <-chan struct{}            = (*FilterIterator).WatchCh
	_ func(*FilterIterator) interface{}                = (*FilterIterator).Next

	_ func() WatchSet                                       = NewWatchSet
	_ func(WatchSet, <-chan struct{})                       = WatchSet.Add
	_ func(WatchSet, int, <-chan struct{}, <-chan struct{}) = WatchSet.AddWithLimit
	_ func(WatchSet, <-chan time.Time) bool                 = WatchSet.Watch
	_ func(WatchSet, context.Context) error                 = WatchSet.WatchCtx
	_ func(WatchSet, context.Context) <-chan error          = WatchSet.WatchCh

	_ error        = ErrNotFound
	_ reflect.Kind = MapType
)

// Every built-in indexer must keep satisfying the same interfaces.
var (
	_ SingleIndexer = (*StringFieldIndex)(nil)
	_ PrefixIndexer = (*StringFieldIndex)(nil)
	_ MultiIndexer  = (*StringSliceFieldIndex)(nil)
	_ PrefixIndexer = (*StringSliceFieldIndex)(nil)
	_ MultiIndexer  = (*StringMapFieldIndex)(nil)
	_ SingleIndexer = (*IntFieldIndex)(nil)
	_ SingleIndexer = (*UintFieldIndex)(nil)
	_ SingleIndexer = (*BoolFieldIndex)(nil)
	_ SingleIndexer = (*UUIDFieldIndex)(nil)
	_ PrefixIndexer = (*UUIDFieldIndex)(nil)
	_ SingleIndexer = (*FieldSetIndex)(nil)
	_ SingleIndexer = (*ConditionalIndex)(nil)
	_ SingleIndexer = (*CompoundIndex)(nil)
	_ PrefixIndexer = (*CompoundIndex)(nil)
	_ MultiIndexer  = (*CompoundMultiIndex)(nil)

	_ Indexer = (*StringFieldIndex)(nil)
	_ Indexer = (*StringSliceFieldIndex)(nil)
	_ Indexer = (*StringMapFieldIndex)(nil)
	_ Indexer = (*IntFieldIndex)(nil)
	_ Indexer = (*UintFieldIndex)(nil)
	_ Indexer = (*BoolFieldIndex)(nil)
	_ Indexer = (*UUIDFieldIndex)(nil)
	_ Indexer = (*FieldSetIndex)(nil)
	_ Indexer = (*ConditionalIndex)(nil)
	_ Indexer = (*CompoundIndex)(nil)
	_ Indexer = (*CompoundMultiIndex)(nil)
)

// Exported struct shapes are frozen: upstream's own tests (and user code) use
// unkeyed composite literals, so field order and count must never change.
var (
	_ = StringFieldIndex{"Field", false}
	_ = StringSliceFieldIndex{"Field", false}
	_ = StringMapFieldIndex{"Field", false}
	_ = IntFieldIndex{"Field"}
	_ = UintFieldIndex{"Field"}
	_ = BoolFieldIndex{"Field"}
	_ = UUIDFieldIndex{"Field"}
	_ = FieldSetIndex{"Field"}
	_ = ConditionalIndex{ConditionalIndexFunc(nil)}
	_ = CompoundIndex{[]Indexer(nil), false}
	_ = CompoundMultiIndex{[]Indexer(nil), false}
	_ = IndexSchema{"name", false, false, Indexer(nil)}
	_ = TableSchema{"name", map[string]*IndexSchema(nil)}
	_ = DBSchema{map[string]*TableSchema(nil)}
)
