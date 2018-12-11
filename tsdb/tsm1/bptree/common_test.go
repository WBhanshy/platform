package bptree

// appendEntry places the key into the buffer and returns an Entry that
// points at it.
func appendEntry(buf *[]byte, key string) Entry {
	ent := NewEntry([]byte(key), uint32(len(*buf)))
	*buf = append(*buf, byte(len(key)>>8), byte(len(key)))
	*buf = append(*buf, key...)
	return ent
}

// iter calls the callback with every entry in the tree.
func iter(bt *T, fn func(ent *Entry)) {
	iter := bt.Iterator()
	for iter.Next() {
		fn(iter.Entry())
	}
}
