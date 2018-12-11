package bptree

import (
	"bytes"
	"sync/atomic"
)

// Iterator walks over the entries in a btree.
type Iterator struct {
	b *T
	n *node
	i payloadIndex
}

// Next advances the iterator and returns true if there is an entry.
func (i *Iterator) Next() bool {
	if i.n == nil {
		return false
	}

nextIndex:
	i.i++

nextNode:
	if i.i >= i.n.count || i.n.deleted == uint32(i.n.count) {
		if i.n.next >= uint32(len(i.b.nodes)) {
			i.n = nil
			return false
		}

		i.n, i.i = i.b.nodes[i.n.next], 0
		goto nextNode
	}

	if i.n.payload[i.i].Deleted() {
		goto nextIndex
	}

	return true
}

// Entry returns the current entry. It is only valid to call this
// if the most recent call to Next returned true.
func (i *Iterator) Entry() *Entry {
	if i.Valid() {
		return &i.n.payload[i.i]
	}
	return nil
}

// Valid returns if the iterator is in a valid state.
func (i *Iterator) Valid() bool {
	return i.n != nil && i.i < i.n.count
}

// CheckKey returns true if the iterator is at the provided key. This
// can avoid disk lookups vs bytes.Equal.
func (i *Iterator) CheckKey(key, buf []byte) bool {
	ent := i.Entry()
	if ent == nil {
		return false
	}
	return keyPrefix(key) == ent.pre && bytes.Equal(key, ent.ReadKey(buf))
}

// DeleteEntry removes the current entry and advances the iterator.
func (i *Iterator) DeleteEntry() bool {
	i.n.delete(i.b, i.i)
	atomic.AddUint32(&i.b.count, ^uint32(0)) // decrement 1
	return i.Next()
}

// Seek places the iterator so that the entry is the largest entry
// that is smaller than or equal to the provided key. It returns
// true if that entry is equal.
func (i *Iterator) Seek(key, buf []byte) (exact, ok bool) {
	pre := keyPrefix(key)
	i.n, _ = i.b.search(pre, key, buf)
	i.i, exact = i.n.search(pre, key, buf)

	// if the search put us at the end of the node, or on to a deleted
	// node, go to the next index.
	if i.i == i.n.count || i.n.payload[i.i].Deleted() {
		if !i.Next() {
			return false, false
		}

		entn := i.n.payload[i.i]
		if pre == entn.pre {
			return true, true
		}
		return bytes.Equal(key, entn.ReadKey(buf)), true
	}

	return exact, true
}

// SeekOffset places the iterator so that the entry is located
// at the given offset. It only works if the keys and offsets are
// in the same order.
func (i *Iterator) SeekOffset(offset uint32) (exact, ok bool) {
	i.n, _ = i.b.searchOffset(offset)
	i.i, exact = i.n.searchOffset(offset)

	// if the search put us at the end of the node, or on to a deleted
	// node, go to the next index.
	if i.i == i.n.count || i.n.payload[i.i].Deleted() {
		if !i.Next() {
			return false, false
		}

		return i.n.payload[i.i].Offset() == offset, true
	}

	return exact, true
}
