package bptree

// Bulk allows bulk loading of entries into a btree. they must
// be appended in strictly ascending order.
type Bulk struct {
	b   T
	n   *node
	nid uint32
}

// Append cheaply adds the entry to the btree. it must be strictly
// greater than any earlier entry.
func (b *Bulk) Append(ent Entry) {
	b.b.count++
	if b.n == nil {
		b.n, b.nid = b.b.alloc(true)
	} else if b.n.count == payloadEntries {
		s, sid := b.b.alloc(true)
		b.n.next = sid
		b.n, b.nid = s, sid
	}
	b.n.appendEntry(ent)
}

// Done returns the bulk loaded btree.
func (b *Bulk) Done() *T {
	if b.b.root != nil {
		return &b.b
	}
	if b.n == nil {
		return &b.b
	}

	low, high := uint32(0), uint32(len(b.b.nodes))
	for high-low > 1 {
		n, nid := b.b.alloc(false)

		for i := low; i < high-1; i++ {
			b.b.nodes[i].parent = nid

			if n.count == payloadEntries {
				n.next = i
				n, nid = b.b.alloc(false)
				continue
			}

			j := i
			for !b.b.nodes[j].leaf {
				j = b.b.nodes[j].next
			}

			ent := b.b.nodes[j+1].payload[0]
			ent.SetPivot(i)
			n.appendEntry(ent)
		}

		b.b.nodes[high-1].parent = nid
		n.next = high - 1
		low, high = high, uint32(len(b.b.nodes))
	}

	b.b.rid = uint32(len(b.b.nodes) - 1)
	b.b.root = b.b.nodes[b.b.rid]
	return &b.b
}
