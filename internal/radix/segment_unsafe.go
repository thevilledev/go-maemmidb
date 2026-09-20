// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

//go:build !memdb_safe && !purego

package radix

import "unsafe"

// Childless nodes -- the great majority: one per stored key -- keep their path
// segment inside their own allocation. That saves an allocation per inserted
// key and, more importantly, a cache miss per lookup: the bytes that finish the
// key comparison sit right behind the node that was just fetched.
//
// The only unsafe operation is building a string over the node's own trailing
// byte array. The array is written once, before the string exists, and never
// again, so the string is immutable as strings must be; being an interior
// pointer into the node, it keeps nothing alive but the node itself.
//
// The array sizes make every class land exactly on an allocator size class
// (96-byte header + 32, 48 or 64). The smallest is 128 bytes on purpose, see
// the size-classed nodes in node_unsafe.go: it starts on a cache line boundary.
//
// The rule that keeps this from pinning dead nodes in memory: an inline segment
// is never shared with another node. Whoever copies a childless node, or takes
// a substring of its segment for a different node, copies the bytes (copyNode,
// the split in Insert, mergeChild).

type (
	leaf32 struct {
		node
		seg [32]byte
	}
	leaf48 struct {
		node
		seg [48]byte
	}
	leaf64 struct {
		node
		seg [64]byte
	}
)

// inlineSegments reports which build this is. Used by tests.
const inlineSegments = true

// newLeafNode returns a childless node with the path segment a+b.
func newLeafNode(a, b string) *node {
	n := len(a) + len(b)
	switch {
	case n == 1:
		if len(a) == 1 {
			return &node{prefix: oneByte[a[0]]}
		}
		return &node{prefix: oneByte[b[0]]}
	case n <= 32:
		x := &leaf32{}
		copy(x.seg[copy(x.seg[:], a):], b)
		x.prefix = unsafe.String(&x.seg[0], n)
		return &x.node
	case n <= 48:
		x := &leaf48{}
		copy(x.seg[copy(x.seg[:], a):], b)
		x.prefix = unsafe.String(&x.seg[0], n)
		return &x.node
	case n <= 64:
		x := &leaf64{}
		copy(x.seg[copy(x.seg[:], a):], b)
		x.prefix = unsafe.String(&x.seg[0], n)
		return &x.node
	}
	// Too long to inline. The result must not alias a: the caller may have
	// passed a view of a scratch buffer (bytesToString), and a + "" would
	// hand that very string back without copying it.
	buf := make([]byte, 0, n)
	buf = append(append(buf, a...), b...)
	return &node{prefix: unsafe.String(unsafe.SliceData(buf), n)}
}

// bytesToString views b as a string for the duration of a call that copies it.
func bytesToString(b []byte) string {
	return unsafe.String(unsafe.SliceData(b), len(b))
}
