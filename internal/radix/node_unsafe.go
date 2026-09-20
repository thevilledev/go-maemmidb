// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

//go:build !memdb_safe && !purego

package radix

import "unsafe"

// node is a radix tree node. Everything except the watch slot is immutable
// once the node is visible to anyone but the transaction that owns it.
//
// A node is allocated together with room for its children (see newNode): the
// child array directly follows the header. This declaration has no slice for
// it -- the array's address is the node's plus a constant, its length is a
// 16-bit field -- which makes the header 16 bytes smaller than the pure-safe
// build's (node_safe.go) and, more importantly, lets everything a lookup or an
// iteration step reads fit the header's first cache line: the path segment,
// the label bitmap that locates the next child, and the value.
type node struct {
	// prefix is the compressed path segment leading to this node, including
	// the label byte its parent indexes it by. Empty only for the root.
	prefix string
	// val is the value of the key that ends exactly at this node; it is
	// meaningful iff leaf is non-nil.
	val interface{}
	// bitmap has one bit per present label; the child for label is the
	// rank(label)-th element of the child array. A node is childless iff the
	// bitmap is zero.
	bitmap [4]uint64

	leaf  *leaf
	watch slot
	// epoch is the ownership stamp (plus leafOwnedBit).
	epoch uint64
	// nkids and ckids are the length and the capacity of the child array.
	nkids, ckids uint16
}

// kidsOffset is where the child array of a size-classed node starts: right
// after the header, which is pointer-aligned.
const kidsOffset = unsafe.Sizeof(node{})

// The accessors below are the only code that turns a *node into the address
// of a child slot. They rely on two facts that newNode establishes and nothing
// changes: a node with ckids > 0 is the first field of a size-classed struct
// whose array has ckids elements, so slots 0..ckids-1 lie inside the node's own
// allocation (and are typed as pointers, so the garbage collector sees them);
// and nkids <= ckids.

func (n *node) kidCount() int { return int(n.nkids) }
func (n *node) kidCap() int   { return int(n.ckids) }

func (n *node) childless() bool {
	return n.bitmap[0]|n.bitmap[1]|n.bitmap[2]|n.bitmap[3] == 0
}

// kid returns child i, which the caller knows to exist: from rank, or because
// i < kidCount().
func (n *node) kid(i int) *node {
	return *(**node)(unsafe.Add(unsafe.Pointer(n), kidsOffset+uintptr(i)*unsafe.Sizeof(n)))
}

func (n *node) setKid(i int, c *node) {
	*(**node)(unsafe.Add(unsafe.Pointer(n), kidsOffset+uintptr(i)*unsafe.Sizeof(n))) = c
}

// kidList returns the children as a slice of the node's own storage.
func (n *node) kidList() []*node {
	if n.ckids == 0 {
		return nil
	}
	return unsafe.Slice((**node)(unsafe.Add(unsafe.Pointer(n), kidsOffset)), n.ckids)[:n.nkids]
}

// setKidCount changes the number of children, within the node's capacity.
func (n *node) setKidCount(count int) {
	if count > int(n.ckids) {
		panic("radix: child array overflow")
	}
	n.nkids = uint16(count)
}

// Size-classed nodes: a node header followed by the child array. A *node
// obtained from one of these points at the start of the allocation, so the
// garbage collector keeps the array alive, and scans it, without anything
// ever needing to know which class a node came from.
//
// The header is 96 bytes, so the classes are 128, 160, 224, 352 ... bytes. There
// is deliberately no class for two children: it would be 112 bytes, and 112-byte
// objects do not start on cache line boundaries -- the header's hot first line
// would straddle two. 128-byte objects do, and four children in 128 bytes is
// still no more than two children cost with a slice header.
type (
	node4 struct {
		node
		arr [4]*node
	}
	node8 struct {
		node
		arr [8]*node
	}
	node16 struct {
		node
		arr [16]*node
	}
	node32 struct {
		node
		arr [32]*node
	}
	node64 struct {
		node
		arr [64]*node
	}
	node128 struct {
		node
		arr [128]*node
	}
	node256 struct {
		node
		arr [256]*node
	}
)

// The accessors assume that the array starts where the header ends.
var _ = [1]struct{}{}[unsafe.Offsetof(node4{}.arr)-kidsOffset]

// newNode returns a node with room for capacity children.
func newNode(capacity int) *node {
	switch {
	case capacity <= 0:
		return &node{}
	case capacity <= 4:
		x := &node4{}
		x.ckids = 4
		return &x.node
	case capacity <= 8:
		x := &node8{}
		x.ckids = 8
		return &x.node
	case capacity <= 16:
		x := &node16{}
		x.ckids = 16
		return &x.node
	case capacity <= 32:
		x := &node32{}
		x.ckids = 32
		return &x.node
	case capacity <= 64:
		x := &node64{}
		x.ckids = 64
		return &x.node
	case capacity <= 128:
		x := &node128{}
		x.ckids = 128
		return &x.node
	default:
		x := &node256{}
		x.ckids = 256
		return &x.node
	}
}
