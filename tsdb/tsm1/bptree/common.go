package bptree

import "sync/atomic"

// keyPrefix returns a prefix that can be used with compare
// to sort the same way the bytes would.
func keyPrefix(key []byte) uint64 {
	var p [8]byte
	var i uint

next:
	if i < uint(len(key)) && i < 8 {
		p[i] = key[i]
		i++
		goto next
	}

	return uint64(p[7]) |
		uint64(p[6])<<0x08 |
		uint64(p[5])<<0x10 |
		uint64(p[4])<<0x18 |
		uint64(p[3])<<0x20 |
		uint64(p[2])<<0x28 |
		uint64(p[1])<<0x30 |
		uint64(p[0])<<0x38
}

// compare64 is like bytes.Compare but for uint64s.
func compare64(a, b uint64) int {
	if a == b {
		return 0
	} else if a < b {
		return -1
	}
	return 1
}

// compare32 is like bytes.Compare but for uint32s.
func compare32(a, b uint32) int {
	if a == b {
		return 0
	} else if a < b {
		return -1
	}
	return 1
}

// packed contains multiple different values in a single uint32.
// 1. If the value is 0b11111111 11111111 11111111 11111111 then it is deleted.
// 2. If the value is 0b10...... ........ xxxxxxxx xxxxxxxx then the x's are the 16 bit length
// 3. Otherwise, the value contains a pivot.
type packed uint32

const packedDeleted = 1<<32 - 1

func newPacked(length uint32) packed {
	return 1<<31 | packed(length)
}

func (p *packed) load() uint32 {
	return atomic.LoadUint32((*uint32)(p))
}

func (p *packed) SetDeleted() bool {
	l := p.load()
	return l != packedDeleted && atomic.CompareAndSwapUint32((*uint32)(p), l, packedDeleted)
}

func (p *packed) Deleted() bool {
	return p.load() == packedDeleted
}

func (p *packed) Length() (uint32, bool) {
	l := p.load()
	return uint32(uint16(l)), l>>30 == 2
}

func (p *packed) SetPivot(pivot uint32) {
	*p = packed(pivot)
}

func (p *packed) Pivot() uint32 {
	return p.load()
}
