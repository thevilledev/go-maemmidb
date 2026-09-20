// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

//go:build !upstream && !base

package bench

import (
	"iter"

	mm "github.com/thevilledev/go-maemmidb"
)

// apilock.go pins the API go-maemmidb shares with go-memdb, in both builds.
// This file pins the API it adds, so that a change to an extension's signature
// is a compile error here before it is a surprise to anyone else.

type lockRow struct {
	S string
	L []string
	I int32
	U uint16
	B bool
}

var (
	// Bitmap indexes and row sets.
	_ mm.MultiIndexer  = (*mm.BitmapIndex)(nil)
	_ mm.PrefixIndexer = (*mm.BitmapIndex)(nil)
	_                  = mm.BitmapIndex{Indexer: mm.Indexer(nil)}

	_ func(*mm.Txn, string, string, ...interface{}) (mm.RowSet, error)                  = (*mm.Txn).Where
	_ func(*mm.Txn, string, string, ...interface{}) (<-chan struct{}, mm.RowSet, error) = (*mm.Txn).WhereWatch
	_ func(*mm.Txn, string) (mm.RowSet, error)                                          = (*mm.Txn).AllRows

	_ func(mm.RowSet, ...mm.RowSet) mm.RowSet    = mm.RowSet.And
	_ func(mm.RowSet, ...mm.RowSet) mm.RowSet    = mm.RowSet.Or
	_ func(mm.RowSet, ...mm.RowSet) mm.RowSet    = mm.RowSet.AndNot
	_ func(mm.RowSet) int                        = mm.RowSet.Len
	_ func(mm.RowSet) mm.ResultIterator          = mm.RowSet.Iterator
	_ func(mm.RowSet) iter.Seq[interface{}]      = mm.RowSet.All
	_ func(mm.ResultIterator) iter.Seq[any]      = mm.All
	_ func(mm.ResultIterator) iter.Seq[*lockRow] = mm.AllOf[*lockRow]

	// Indexers built from accessor functions.
	_ mm.SingleIndexer = &mm.StringIndex[lockRow]{Get: func(*lockRow) string { return "" }, Lowercase: true}
	_ mm.PrefixIndexer = (*mm.StringIndex[lockRow])(nil)
	_ mm.MultiIndexer  = &mm.StringSliceIndex[lockRow]{Get: func(*lockRow) []string { return nil }, Lowercase: true}
	_ mm.PrefixIndexer = (*mm.StringSliceIndex[lockRow])(nil)
	_ mm.SingleIndexer = &mm.IntIndex[lockRow, int32]{Get: func(*lockRow) int32 { return 0 }}
	_ mm.SingleIndexer = &mm.UintIndex[lockRow, uint16]{Get: func(*lockRow) uint16 { return 0 }}
	_ mm.SingleIndexer = &mm.BoolIndex[lockRow]{Get: func(*lockRow) bool { return false }}

	// Typed tables, rows and keys.
	_ func(string) mm.Table[lockRow]                                                              = mm.NewTable[lockRow]
	_ func(mm.Table[lockRow]) string                                                              = mm.Table[lockRow].Name
	_ func(mm.Table[lockRow], *mm.Txn, *lockRow) error                                            = mm.Table[lockRow].Insert
	_ func(mm.Table[lockRow], *mm.Txn, *lockRow) error                                            = mm.Table[lockRow].Delete
	_ func(mm.Table[lockRow], *mm.Txn, string, ...interface{}) (*lockRow, error)                  = mm.Table[lockRow].First
	_ func(mm.Table[lockRow], *mm.Txn, string, ...interface{}) (*lockRow, error)                  = mm.Table[lockRow].Last
	_ func(mm.Table[lockRow], *mm.Txn, string, ...interface{}) (<-chan struct{}, *lockRow, error) = mm.Table[lockRow].FirstWatch
	_ func(mm.Table[lockRow], *mm.Txn, string, ...interface{}) (mm.Rows[lockRow], error)          = mm.Table[lockRow].Get
	_ func(mm.Table[lockRow], *mm.Txn, string, ...interface{}) (mm.Rows[lockRow], error)          = mm.Table[lockRow].GetReverse
	_ func(mm.Table[lockRow], *mm.Txn, string, ...interface{}) (mm.Rows[lockRow], error)          = mm.Table[lockRow].LowerBound
	_ func(mm.Table[lockRow], *mm.Txn, string, ...interface{}) (mm.Rows[lockRow], error)          = mm.Table[lockRow].ReverseLowerBound
	_ func(mm.Table[lockRow], string) *mm.StringKey[lockRow]                                      = mm.Table[lockRow].StringKey

	_ func(mm.Rows[lockRow]) iter.Seq[*lockRow] = mm.Rows[lockRow].All
	_ func(mm.Rows[lockRow]) *lockRow           = mm.Rows[lockRow].Next
	_ func(mm.Rows[lockRow]) <-chan struct{}    = mm.Rows[lockRow].WatchCh

	_ func(*mm.StringKey[lockRow], *mm.Txn, string) (*lockRow, error)         = (*mm.StringKey[lockRow]).First
	_ func(*mm.StringKey[lockRow], *mm.Txn, string) (*lockRow, error)         = (*mm.StringKey[lockRow]).FirstPrefix
	_ func(*mm.StringKey[lockRow], *mm.Txn, string) (mm.Rows[lockRow], error) = (*mm.StringKey[lockRow]).Get
	_ func(*mm.StringKey[lockRow], *mm.Txn, string) (mm.Rows[lockRow], error) = (*mm.StringKey[lockRow]).GetPrefix

	_ func(mm.Table[lockRow], string) *mm.IntKey[lockRow, int32]                    = mm.IntKeyOf[int32, lockRow]
	_ func(*mm.IntKey[lockRow, int32], *mm.Txn, int32) (*lockRow, error)            = (*mm.IntKey[lockRow, int32]).First
	_ func(*mm.IntKey[lockRow, int32], *mm.Txn, int32) (mm.Rows[lockRow], error)    = (*mm.IntKey[lockRow, int32]).Get
	_ func(*mm.IntKey[lockRow, int32], *mm.Txn, int32) (mm.Rows[lockRow], error)    = (*mm.IntKey[lockRow, int32]).LowerBound
	_ func(mm.Table[lockRow], string) *mm.UintKey[lockRow, uint16]                  = mm.UintKeyOf[uint16, lockRow]
	_ func(*mm.UintKey[lockRow, uint16], *mm.Txn, uint16) (*lockRow, error)         = (*mm.UintKey[lockRow, uint16]).First
	_ func(*mm.UintKey[lockRow, uint16], *mm.Txn, uint16) (mm.Rows[lockRow], error) = (*mm.UintKey[lockRow, uint16]).Get
	_ func(*mm.UintKey[lockRow, uint16], *mm.Txn, uint16) (mm.Rows[lockRow], error) = (*mm.UintKey[lockRow, uint16]).LowerBound
)
