// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

//go:build upstream

package bench

import (
	"reflect"

	memdb "github.com/hashicorp/go-memdb"
)

// Impl names the implementation this build of the benchmark suite exercises.
const Impl = "upstream (github.com/hashicorp/go-memdb)"

// The benchmark sources are written once against these aliases. A type alias
// is the aliased type, so methods, variadic signatures and composite literals
// resolve to the selected implementation with zero adapter overhead.
type (
	MemDB          = memdb.MemDB
	Txn            = memdb.Txn
	DBSchema       = memdb.DBSchema
	TableSchema    = memdb.TableSchema
	IndexSchema    = memdb.IndexSchema
	ResultIterator = memdb.ResultIterator
	WatchSet       = memdb.WatchSet
	Changes        = memdb.Changes
	Change         = memdb.Change
	FilterFunc     = memdb.FilterFunc
	FilterIterator = memdb.FilterIterator

	Indexer       = memdb.Indexer
	SingleIndexer = memdb.SingleIndexer
	MultiIndexer  = memdb.MultiIndexer
	PrefixIndexer = memdb.PrefixIndexer

	StringFieldIndex      = memdb.StringFieldIndex
	StringSliceFieldIndex = memdb.StringSliceFieldIndex
	StringMapFieldIndex   = memdb.StringMapFieldIndex
	IntFieldIndex         = memdb.IntFieldIndex
	UintFieldIndex        = memdb.UintFieldIndex
	BoolFieldIndex        = memdb.BoolFieldIndex
	UUIDFieldIndex        = memdb.UUIDFieldIndex
	FieldSetIndex         = memdb.FieldSetIndex
	ConditionalIndex      = memdb.ConditionalIndex
	ConditionalIndexFunc  = memdb.ConditionalIndexFunc
	CompoundIndex         = memdb.CompoundIndex
	CompoundMultiIndex    = memdb.CompoundMultiIndex
)

var (
	ErrNotFound = memdb.ErrNotFound
	MapType     = memdb.MapType
)

func NewMemDB(schema *DBSchema) (*MemDB, error) { return memdb.NewMemDB(schema) }

func NewWatchSet() WatchSet { return memdb.NewWatchSet() }

func NewFilterIterator(iter ResultIterator, filter FilterFunc) *FilterIterator {
	return memdb.NewFilterIterator(iter, filter)
}

func IsIntType(k reflect.Kind) (size int, okay bool) { return memdb.IsIntType(k) }

func IsUintType(k reflect.Kind) (size int, okay bool) { return memdb.IsUintType(k) }
