package bptree

import (
	"fmt"
	"testing"

	"github.com/zeebo/assert"
	"github.com/zeebo/pcg"
)

func TestIterator(t *testing.T) {
	t.Run("Basic", func(t *testing.T) {
		var set = map[string]bool{}
		var buf []byte
		var bu Bulk

		for i := 0; i <= 100000; i++ {
			key := fmt.Sprintf("%08d", 2*i)
			bu.Append(appendEntry(&buf, key))
			set[key] = true
		}
		bt := bu.Done()

		assert.Equal(t, bt.count, len(set))

		last, iter := "", bt.Iterator()
		for iter.Next() {
			ent := iter.Entry()
			key := string(ent.ReadKey(buf))
			assert.That(t, last < key)
			assert.That(t, set[key])
			delete(set, key)
			last = key
		}

		assert.Equal(t, len(set), 0)
	})

	t.Run("Empty", func(t *testing.T) {
		iter := new(T).Iterator()
		assert.That(t, !iter.Next())
	})

	t.Run("Seek", func(t *testing.T) {
		var buf []byte
		var bu Bulk

		for i := 0; i <= 100000; i++ {
			bu.Append(appendEntry(&buf, fmt.Sprintf("%08d", 2*i)))
		}
		bt := bu.Done()
		iter := bt.Iterator()

		exact, ok := iter.Seek([]byte("00009998"), buf)
		assert.That(t, exact && ok)
		assert.Equal(t, string(iter.Entry().ReadKey(buf)), "00009998")

		exact, ok = iter.Seek([]byte("00009999"), buf)
		assert.That(t, !exact && ok)
		assert.Equal(t, string(iter.Entry().ReadKey(buf)), "00010000")

		exact, ok = iter.Seek([]byte{}, buf)
		assert.That(t, !exact && ok)
		assert.Equal(t, string(iter.Entry().ReadKey(buf)), "00000000")

		exact, ok = iter.Seek([]byte("00200000"), buf)
		assert.That(t, exact && ok)
		assert.Equal(t, string(iter.Entry().ReadKey(buf)), "00200000")

		exact, ok = iter.Seek([]byte("00300000"), buf)
		assert.That(t, !exact && !ok)

		for i := 0; i < 1000; i++ {
			j := pcg.Uint32n(100000)
			exact, ok = iter.Seek([]byte(fmt.Sprintf("%08d", 2*j)), buf)
			assert.That(t, exact && ok)

			exact, ok = iter.Seek([]byte(fmt.Sprintf("%08d", 2*j+1)), buf)
			assert.That(t, !exact && ok)
		}
	})

	t.Run("SeekOffset", func(t *testing.T) {
		var buf []byte
		var offsets []uint32
		var bu Bulk

		for i := 0; i <= 100000; i++ {
			offsets = append(offsets, uint32(len(buf)))
			bu.Append(appendEntry(&buf, fmt.Sprintf("%08d", 2*i)))
		}
		bt := bu.Done()
		iter := bt.Iterator()

		exact, ok := iter.SeekOffset(offsets[4999])
		assert.That(t, exact && ok)
		assert.Equal(t, string(iter.Entry().ReadKey(buf)), "00009998")

		exact, ok = iter.SeekOffset(offsets[4999] + 1)
		assert.That(t, !exact && ok)
		assert.Equal(t, string(iter.Entry().ReadKey(buf)), "00010000")

		exact, ok = iter.SeekOffset(offsets[100000])
		assert.That(t, exact && ok)
		assert.Equal(t, string(iter.Entry().ReadKey(buf)), "00200000")

		exact, ok = iter.SeekOffset(offsets[100000] + 1)
		assert.That(t, !exact && !ok)

		for i := 0; i < 1000; i++ {
			j := pcg.Uint32n(100000)
			exact, ok = iter.SeekOffset(offsets[j])
			assert.That(t, exact && ok)

			exact, ok = iter.SeekOffset(offsets[j] + 1)
			assert.That(t, !exact && ok)
		}
	})

	t.Run("Delete", func(t *testing.T) {
		var buf []byte
		var bu Bulk

		for i := 0; i < 10; i++ {
			bu.Append(appendEntry(&buf, fmt.Sprintf("%08d", i)))
		}
		bt := bu.Done()
		iter := bt.Iterator()

		assert.That(t, iter.Next())
		assert.Equal(t, string(iter.Entry().ReadKey(buf)), "00000000")

		assert.That(t, iter.DeleteEntry())
		assert.Equal(t, string(iter.Entry().ReadKey(buf)), "00000001")

		exact, ok := iter.Seek([]byte("00000009"), buf)
		assert.That(t, exact && ok)
		assert.That(t, !iter.DeleteEntry())

		iter = bt.Iterator()
		for i := 1; iter.Next(); i++ {
			assert.Equal(t, string(iter.Entry().ReadKey(buf)), fmt.Sprintf("%08d", i))
		}
	})

	t.Run("DeleteAll", func(t *testing.T) {
		var exists = map[string]struct{}{}
		var buf []byte
		var bu Bulk

		for i := 0; i < 1000; i++ {
			key := fmt.Sprintf("%08d", i)
			exists[key] = struct{}{}
			bu.Append(appendEntry(&buf, key))
		}
		bt := bu.Done()

		deleteRandom := func() {
			iter := bt.Iterator()
			if _, ok := iter.SeekOffset(pcg.Uint32n(uint32(len(buf)))); !ok {
				// if we seeked past the end, delete from the front.
				iter = bt.Iterator()
				iter.Next()
			}
			key := string(iter.Entry().ReadKey(buf))
			if _, ok := exists[key]; !ok {
				t.Fatal("attempting to delete key that already is deleted:", key)
			}
			t.Log("deleting", key)
			delete(exists, key)
			iter.DeleteEntry()
		}

		check := func() {
			existsCopy := make(map[string]struct{}, len(exists))
			for key := range exists {
				existsCopy[key] = struct{}{}
			}

			iter := bt.Iterator()
			for iter.Next() {
				key := string(iter.Entry().ReadKey(buf))
				if _, ok := existsCopy[key]; !ok {
					t.Fatal("expected", key, "to still exist")
				}
				delete(existsCopy, key)
			}

			if len(existsCopy) > 0 {
				for nid, n := range bt.nodes {
					if n.leaf {
						t.Logf("nid:%d prev:%d next:%d\n", nid, n.prev, n.next)
					}
				}
				t.Fatal("extra keys missed:", existsCopy)
			}
		}

		for i := 0; i < 1000; i++ {
			deleteRandom()
			check()
		}

		iter := bt.Iterator()
		if iter.Next() {
			t.Fatal("expected empty tree:", string(iter.Entry().ReadKey(buf)))
		}
	})
}

func BenchmarkIterator(b *testing.B) {
	b.Run("Seek", func(b *testing.B) {
		var buf []byte
		var bu Bulk

		for i := 0; i < 100000; i++ {
			bu.Append(appendEntry(&buf, fmt.Sprintf("%08d", i)))
		}
		bt := bu.Done()
		iter := bt.Iterator()

		b.ResetTimer()
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			n := pcg.Uint32n(100000) * 8
			iter.Seek(buf[n:n+8], buf)
		}
	})

	b.Run("SeekOffset", func(b *testing.B) {
		var buf []byte
		var bu Bulk

		for i := 0; i < 100000; i++ {
			bu.Append(appendEntry(&buf, fmt.Sprintf("%08d", i)))
		}
		bt := bu.Done()
		iter := bt.Iterator()

		b.ResetTimer()
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			iter.SeekOffset(pcg.Uint32n(uint32(len(buf))))
		}
	})

	b.Run("Next", func(b *testing.B) {
		var buf []byte
		var bu Bulk

		for i := 0; i < 100000; i++ {
			bu.Append(appendEntry(&buf, fmt.Sprintf("%08d", i)))
		}
		bt := bu.Done()
		iter := bt.Iterator()

		b.ResetTimer()
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			if !iter.Next() {
				iter = bt.Iterator()
			}
		}
	})

	b.Run("Delete", func(b *testing.B) {
		makeIterator := func() Iterator {
			var buf []byte
			var bu Bulk
			for i := 0; i < 100000; i++ {
				bu.Append(appendEntry(&buf, fmt.Sprintf("%08d", i)))
			}
			return bu.Done().Iterator()
		}

		iter := makeIterator()
		iter.Next()

		b.ResetTimer()
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			for !iter.DeleteEntry() {
				b.StopTimer()
				iter = makeIterator()
				iter.Next()
				b.StartTimer()
			}
		}
	})

	b.Run("FullyDeletedNext", func(b *testing.B) {
		var buf []byte
		var bu Bulk

		for i := 0; i < 1000000; i++ {
			bu.Append(appendEntry(&buf, fmt.Sprintf("%08d", i)))
		}
		bt := bu.Done()

		iter := bt.Iterator()
		iter.Next()
		for iter.DeleteEntry() {
		}

		b.ResetTimer()
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			iter := bt.Iterator()
			iter.Next()
		}
	})

	b.Run("RandomNext", func(b *testing.B) {
		var buf []byte
		var bu Bulk

		for i := 0; i < 1000000; i++ {
			bu.Append(appendEntry(&buf, fmt.Sprintf("%08d", i)))
		}
		bt := bu.Done()

		iter := bt.Iterator()
		iter.Next()
		for i := 0; i < 1000000/2; i++ {
			if _, ok := iter.SeekOffset(pcg.Uint32n(uint32(len(buf)))); ok {
				iter.DeleteEntry()
			}
		}

		iter = bt.Iterator()

		b.ResetTimer()
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			if !iter.Next() {
				iter = bt.Iterator()
			}
		}
	})
}
