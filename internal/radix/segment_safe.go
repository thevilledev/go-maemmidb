// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

//go:build memdb_safe || purego

package radix

// Without package unsafe a node cannot hold its own path segment (a string
// cannot be made to point into the node), so segments are ordinary strings.

// inlineSegments reports which build this is. Used by tests.
const inlineSegments = false

// newLeafNode returns a childless node with the path segment a+b.
func newLeafNode(a, b string) *node {
	switch {
	case len(a)+len(b) == 1:
		if len(a) == 1 {
			return &node{prefix: oneByte[a[0]]}
		}
		return &node{prefix: oneByte[b[0]]}
	case b == "":
		return &node{prefix: a}
	case a == "":
		return &node{prefix: b}
	}
	return &node{prefix: a + b}
}

// bytesToString copies b; the result may be retained.
func bytesToString(b []byte) string {
	return string(b)
}
