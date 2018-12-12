package tsm1

import (
	"fmt"

	"github.com/influxdata/platform/tsdb/tsm1/bptree"
)

// TSMIndexIterator allows one to iterate over the TSM index.
type TSMIndexIterator struct {
	b    []byte
	n    int
	d    *indirectIndex
	iter bptree.Iterator

	// if true, don't need to advance iter on the call to Next
	first  bool
	peeked bool

	ok  bool
	err error
	ent *bptree.Entry

	key     []byte
	typ     byte
	entries []IndexEntry
}

// Next advances the iterator and reports if it is still valid.
func (t *TSMIndexIterator) Next() bool {
	if t.n != t.d.KeyCount() {
		t.err, t.ok = fmt.Errorf("Key count changed during iteration"), false
	}
	if !t.ok || t.err != nil {
		return false
	}
	if !t.peeked && !t.first {
		t.ok = t.iter.Next()
	}
	if !t.ok {
		return false
	}

	t.peeked = false
	t.first = false
	t.ent = t.iter.Entry()

	t.key = nil
	t.typ = 0
	t.entries = t.entries[:0]
	return true
}

// Peek reports the next key or nil if there is not one.
func (t *TSMIndexIterator) Peek() []byte {
	if !t.ok || t.err != nil {
		return nil
	}

	if !t.peeked {
		t.ok = t.iter.Next()
		t.peeked = true
	}
	if !t.ok {
		return nil
	}
	return t.iter.Entry().ReadKey(t.b)
}

// Key reports the current key.
func (t *TSMIndexIterator) Key() []byte {
	if t.key == nil {
		t.key = t.ent.ReadKey(t.b)
		t.typ = t.b[t.ent.EntryOffset(t.b)]
	}
	return t.key
}

// Type reports the current type.
func (t *TSMIndexIterator) Type() byte {
	if t.key == nil {
		t.key = t.ent.ReadKey(t.b)
		t.typ = t.b[t.ent.EntryOffset(t.b)]
	}
	return t.typ
}

// Entries reports the current list of entries.
func (t *TSMIndexIterator) Entries() []IndexEntry {
	if len(t.entries) == 0 {
		t.entries, t.err = readEntries(t.b[t.ent.EntryOffset(t.b):], t.entries)
	}
	return t.entries
}

// Err reports if an error stopped the iteration.
func (t *TSMIndexIterator) Err() error {
	return t.err
}
