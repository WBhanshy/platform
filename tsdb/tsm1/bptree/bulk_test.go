package bptree

import (
	"fmt"
	"testing"

	"github.com/zeebo/assert"
	"github.com/zeebo/pcg"
)

func TestBulk(t *testing.T) {
	t.Run("Basic", func(t *testing.T) {
		var bu Bulk
		var buf []byte

		for i := 0; i < 10000; i++ {
			bu.Append(appendEntry(&buf, fmt.Sprintf("%04d", i)))
		}
		bt := bu.Done()

		i := 0
		iter(bt, func(ent *Entry) {
			key := string(ent.ReadKey(buf))
			assert.Equal(t, key, fmt.Sprintf("%04d", i))
			i++
		})
	})

	t.Run("One", func(t *testing.T) {
		var bu Bulk
		var buf []byte

		bu.Append(appendEntry(&buf, "0"))
		bt := bu.Done()

		iter(bt, func(ent *Entry) {
			assert.Equal(t, string(ent.ReadKey(buf)), "0")
		})
	})

	t.Run("Zero", func(t *testing.T) {
		var bu Bulk
		bt := bu.Done()

		iter(bt, func(ent *Entry) {
			t.Fatal("expected no entries")
		})
	})

	t.Run("Seek", func(t *testing.T) {
		var bu Bulk
		var buf []byte

		for i := 0; i <= 100; i++ {
			bu.Append(appendEntry(&buf, fmt.Sprintf("%08d", 2*i)))
		}
		bt := bu.Done()

		iter := bt.Iterator()
		for i := 0; i < 10000; i++ {
			j := pcg.Uint32n(100)
			exact, ok := iter.Seek([]byte(fmt.Sprintf("%08d", 2*j)), buf)
			t.Log(j, 2*j, exact, ok)
			assert.That(t, exact && ok)

			exact, ok = iter.Seek([]byte(fmt.Sprintf("%08d", 2*j+1)), buf)
			assert.That(t, !exact && ok)
		}
	})
}

func BenchmarkBulk(b *testing.B) {
	b.Run("Basic", func(b *testing.B) {
		run := func(b *testing.B, n int64) {
			var buf []byte

			ents := make([]Entry, n)
			for i := range ents {
				ents[i] = appendEntry(&buf, fmt.Sprintf("%08d", i))
			}

			b.SetBytes(8 * n)
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				var bu Bulk
				for i := range ents {
					bu.Append(ents[i])
				}
				bu.Done()
			}
		}

		for _, size := range []int64{0, 1, 1e1, 1e2, 1e3, 1e4, 1e5, 1e6} {
			b.Run(fmt.Sprint(size), func(b *testing.B) { run(b, size) })
		}
	})
}
