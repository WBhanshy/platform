package bptree

import "encoding/binary"

// Entry is a compact structure for representing a key in a b+tree.
type Entry struct {
	pre    uint64 // first 8 bytes of key
	meta   packed // 4 bytes of packed metadata
	offset uint32 // offset as to where the key exists
}

// NewEntry constructs an entry for storing into the b+tree.
func NewEntry(key []byte, offset uint32) Entry {
	return Entry{
		pre:    keyPrefix(key),
		meta:   newPacked(uint32(len(key))),
		offset: offset,
	}
}

// ReadKey returns the byte slice containing the key out of the buffer.
func (e *Entry) ReadKey(buf []byte) []byte {
	return buf[e.Offset()+2 : e.Offset()+2+e.Length(buf)]
}

// Offset returns the offset at which the index entry starts.
func (e *Entry) Offset() uint32 { return e.offset }

// Length returns the length of the key.
func (e *Entry) Length(buf []byte) uint32 {
	if length, ok := e.meta.Length(); ok {
		return length
	}
	return uint32(binary.BigEndian.Uint16(buf[e.offset:]))
}

// EntryOffset returns the offset right after the key, where the metadata and
// index entries start.
func (e *Entry) EntryOffset(buf []byte) uint32 {
	return e.Offset() + 2 + e.Length(buf)
}

// Pivot returns the stored pivot. It should be called on non-leaf entries.
func (e *Entry) Pivot() uint32 { return e.meta.Pivot() }

// SetPivot sets the pivot for the entry. It should be called on non-leaf entries.
func (e *Entry) SetPivot(pivot uint32) { e.meta.SetPivot(pivot) }

// SetDeleted marks the entry as deleted. It reports if this call marked it.
func (e *Entry) SetDeleted() bool { return e.meta.SetDeleted() }

// Deleted returns if the entry has been deleted.
func (e *Entry) Deleted() bool { return e.meta.Deleted() }
