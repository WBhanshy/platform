package bptree

import (
	"sync/atomic"
)

// T is an in memory B+ tree.
type T struct {
	root  *node
	rid   uint32
	count uint32
	nodes []*node
}

// search returns the leaf node that should contain the key.
func (b *T) search(pre uint64, key, buf []byte) (*node, uint32) {
	n, nid, i := b.root, b.rid, payloadIndex(0)
	for !n.leaf {
		i, _ = n.search(pre, key, buf)
		if i >= n.count || i >= payloadEntries {
			nid = n.next
		} else {
			nid = n.payload[i].Pivot()
		}
		n = b.nodes[nid]
	}
	return n, nid
}

// searchOffset returns the leaf node that should contain the offset.
// This only works if the keys and offsets have the same order.
func (b *T) searchOffset(offset uint32) (*node, uint32) {
	n, nid, i := b.root, b.rid, payloadIndex(0)
	for !n.leaf {
		i, _ = n.searchOffset(offset)
		if i >= n.count || i >= payloadEntries {
			nid = n.next
		} else {
			nid = n.payload[i].Pivot()
		}
		n = b.nodes[nid]
	}
	return n, nid
}

// alloc creates a fresh node.
func (b *T) alloc(leaf bool) (*node, uint32) {
	n := new(node)
	n.leaf = leaf
	n.next = ^uint32(0)
	b.nodes = append(b.nodes, n)
	return n, uint32(len(b.nodes) - 1)
}

// Iterator returns an iterator over the entries in the btree.
func (b *T) Iterator() Iterator {
	var n *node
	if len(b.nodes) > 0 {
		n = b.nodes[0]
	}
	return Iterator{
		b: b,
		n: n,
		i: ^payloadIndex(0), // use -1 so Next must be called once.
	}
}

// Count returns how many entries are in the btree.
func (b *T) Count() uint32 { return atomic.LoadUint32(&b.count) }

// Has returns true if the key exists in the tree.
func (b *T) Has(key, buf []byte) bool {
	pre := keyPrefix(key)
	n, _ := b.search(pre, key, buf)
	i, exact := n.search(pre, key, buf)
	return exact && !n.payload[i].Deleted()
}
