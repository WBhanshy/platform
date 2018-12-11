package bptree

import (
	"bytes"
	"sync/atomic"
)

const payloadEntries = 63 // causes nodes to be 1024 bytes

type payloadIndex = uint8 // enough bits to hold payloadEntries

// node are nodes in the btree.
type node struct {
	next    uint32       // pointer to the next node (or if not leaf, the rightmost edge)
	parent  uint32       // backpointer to the parent node
	deleted uint32       // deleted values in payload. a uint32 for atomics.
	count   payloadIndex // used values in payload
	leaf    bool         // set if is a leaf
	_       [2]byte      // padding
	payload [payloadEntries]Entry
}

// search returns the index into the payload where the key would be inserted.
// it returns true if the key matches.
func (n *node) search(pre uint64, key, buf []byte) (payloadIndex, bool) {
	i, j := payloadIndex(0), n.count

	for i < j {
		h := (i + j) >> 1
		enth := &n.payload[h]

		switch compare64(pre, enth.pre) {
		case 1:
			i = h + 1

		case 0:
			kh := enth.ReadKey(buf)
			switch bytes.Compare(key, kh) {
			case 1:
				i = h + 1

			case 0:
				return h, true

			case -1:
				j = h
			}

		case -1:
			j = h
		}
	}

	return i, false
}

// searchOffset returns the index into the payload where the offset would be inserted.
// This only works if the keys and offsets have the same order.
func (n *node) searchOffset(offset uint32) (payloadIndex, bool) {
	i, j := payloadIndex(0), n.count

	for i < j {
		h := (i + j) >> 1
		enth := &n.payload[h]

		switch compare32(offset, enth.Offset()) {
		case 1:
			i = h + 1

		case 0:
			return h, true

		case -1:
			j = h
		}
	}

	return i, false
}

// appendEntry appends the entry into the node. it must compare greater than any
// element inside of the node, already, and should never be called on a node that
// would have to split.
func (n *node) appendEntry(ent Entry) {
	n.payload[n.count] = ent
	n.count++
}

// getNext atomically gets the next node.
func (n *node) getNext() uint32 { return atomic.LoadUint32(&n.next) }

// delete removes the ith entry from the node. it reports if the node is now empty.
func (n *node) delete(b *T, i payloadIndex) {
	if n.payload[i].SetDeleted() {
		atomic.AddUint32(&n.deleted, 1)
	}

	// TODO(jeff): maybe we can do some thing where we change the prev and next
	// pointers on an empty node so that they are skipped during iteration. this
	// almost works, but causes iterators concurrent with deletes to possibly skip
	// some values. maybe there's a way to avoid that.

	// again:
	// 	prev, next := atomic.LoadUint32(&n.prev), atomic.LoadUint32(&n.next)

	// 	// if we have a previous node, attempt to bump it's next pointer to our next
	// 	// so that we are skipped over in forward iteration. we ensure that we only attempt
	// 	// to change their next pointer to something larger.
	// 	prevNextAddr := &b.start
	// 	if prev < uint32(len(b.nodes)) {
	// 		prevNextAddr = &b.nodes[prev].next
	// 	}

	// 	prevNext := atomic.LoadUint32(prevNextAddr)
	// 	if next > prevNext {
	// 		if !atomic.CompareAndSwapUint32(prevNextAddr, prevNext, next) {
	// 			goto again
	// 		}
	// 	}

	// 	// now that we know the previous node does not point at us, update ourselves
	// 	// to no longer point at it.
	// 	if !atomic.CompareAndSwapUint32(&n.prev, prev, ^uint32(0)) {
	// 		goto again
	// 	}

	// 	// if we have a next node, attempt to bump it's prev pointer to our prev. this ensures
	// 	// that if the next node is fully deleted, it updates the appropriate node's next
	// 	// pointer to skip it. we ensure that we only attempt to change the prev pointer
	// 	// to something smaller.
	// 	if next < uint32(len(b.nodes)) {
	// 		nextPrev := atomic.LoadUint32(&b.nodes[next].prev)
	// 		if prev == ^uint32(0) || prev < nextPrev {
	// 			if !atomic.CompareAndSwapUint32(&b.nodes[next].prev, nextPrev, prev) {
	// 				goto again
	// 			}
	// 		}

	// 		// now that we know the previous node does not point at us, update ourselves
	// 		// to no longer point at it.
	// 		if !atomic.CompareAndSwapUint32(&n.next, next, ^uint32(0)) {
	// 			goto again
	// 		}
	// 	}
}
