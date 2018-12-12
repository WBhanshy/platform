package tsm1

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/influxdata/platform/pkg/bytesutil"
	"github.com/influxdata/platform/pkg/file"
	"github.com/influxdata/platform/tsdb/tsm1/bptree"
)

// ErrFileInUse is returned when attempting to remove or close a TSM file that is still being used.
var ErrFileInUse = fmt.Errorf("file still in use")

// TSMReader is a reader for a TSM file.
type TSMReader struct {
	// refs is the count of active references to this reader.
	refs   int64
	refsWG sync.WaitGroup

	madviseWillNeed bool // Hint to the kernel with MADV_WILLNEED.
	mu              sync.RWMutex

	// accessor provides access and decoding of blocks for the reader.
	accessor blockAccessor

	// index is the index of all blocks.
	index *indirectIndex

	// tombstoner ensures tombstoned keys are not available by the index.
	tombstoner *Tombstoner

	// size is the size of the file on disk.
	size int64

	// lastModified is the last time this file was modified on disk
	lastModified int64

	// deleteMu limits concurrent deletes
	deleteMu sync.Mutex
}

type tsmReaderOption func(*TSMReader)

// WithMadviseWillNeed is an option for specifying whether to provide a MADV_WILL need hint to the kernel.
var WithMadviseWillNeed = func(willNeed bool) tsmReaderOption {
	return func(r *TSMReader) {
		r.madviseWillNeed = willNeed
	}
}

// NewTSMReader returns a new TSMReader from the given file.
func NewTSMReader(f *os.File, options ...tsmReaderOption) (*TSMReader, error) {
	t := &TSMReader{}
	for _, option := range options {
		option(t)
	}

	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	t.size = stat.Size()
	t.lastModified = stat.ModTime().UnixNano()
	t.accessor = &mmapAccessor{
		f:            f,
		mmapWillNeed: t.madviseWillNeed,
	}

	index, err := t.accessor.init()
	if err != nil {
		return nil, err
	}

	t.index = index
	t.tombstoner = NewTombstoner(t.Path(), index.ContainsKey)

	if err := t.applyTombstones(); err != nil {
		return nil, err
	}

	return t, nil
}

// WithObserver sets the observer for the TSM reader.
func (t *TSMReader) WithObserver(obs FileStoreObserver) {
	if obs == nil {
		obs = noFileStoreObserver{}
	}
	t.tombstoner.WithObserver(obs)
}

func (t *TSMReader) applyTombstones() error {
	var cur, prev Tombstone
	batch := make([][]byte, 0, 4096)

	if err := t.tombstoner.Walk(func(ts Tombstone) error {
		cur = ts
		if len(batch) > 0 {
			if prev.Min != cur.Min || prev.Max != cur.Max {
				if !bytesutil.IsSorted(batch) {
					bytesutil.Sort(batch)
				}
				t.index.DeleteRange(batch, prev.Min, prev.Max)
				batch = batch[:0]
			}
		}

		// Copy the tombstone key and re-use the buffers to avoid allocations
		n := len(batch)
		batch = batch[:n+1]
		if cap(batch[n]) < len(ts.Key) {
			batch[n] = make([]byte, len(ts.Key))
		} else {
			batch[n] = batch[n][:len(ts.Key)]
		}
		copy(batch[n], ts.Key)

		if len(batch) >= 4096 {
			if !bytesutil.IsSorted(batch) {
				bytesutil.Sort(batch)
			}
			t.index.DeleteRange(batch, prev.Min, prev.Max)
			batch = batch[:0]
		}

		prev = ts
		return nil
	}); err != nil {
		return fmt.Errorf("init: read tombstones: %v", err)
	}

	if len(batch) > 0 {
		if !bytesutil.IsSorted(batch) {
			bytesutil.Sort(batch)
		}
		t.index.DeleteRange(batch, cur.Min, cur.Max)
	}
	return nil
}

func (t *TSMReader) Free() error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.accessor.free()
}

// Path returns the path of the file the TSMReader was initialized with.
func (t *TSMReader) Path() string {
	t.mu.RLock()
	p := t.accessor.path()
	t.mu.RUnlock()
	return p
}

// ReadAt returns the values corresponding to the given index entry.
func (t *TSMReader) ReadAt(entry *IndexEntry, vals []Value) ([]Value, error) {
	t.mu.RLock()
	v, err := t.accessor.readBlock(entry, vals)
	t.mu.RUnlock()
	return v, err
}

// Read returns the values corresponding to the block at the given key and timestamp.
func (t *TSMReader) Read(key []byte, timestamp int64) ([]Value, error) {
	t.mu.RLock()
	v, err := t.accessor.read(key, timestamp)
	t.mu.RUnlock()
	return v, err
}

// ReadAll returns all values for a key in all blocks.
func (t *TSMReader) ReadAll(key []byte) ([]Value, error) {
	t.mu.RLock()
	v, err := t.accessor.readAll(key)
	t.mu.RUnlock()
	return v, err
}

func (t *TSMReader) ReadBytes(e *IndexEntry, b []byte) (uint32, []byte, error) {
	t.mu.RLock()
	n, v, err := t.accessor.readBytes(e, b)
	t.mu.RUnlock()
	return n, v, err
}

// Type returns the type of values stored at the given key.
func (t *TSMReader) Type(key []byte) (byte, error) {
	return t.index.Type(key)
}

// MeasurementStats returns the on-disk measurement stats for this file, if available.
func (t *TSMReader) MeasurementStats() (MeasurementStats, error) {
	f, err := os.Open(StatsFilename(t.Path()))
	if os.IsNotExist(err) {
		return make(MeasurementStats), nil
	} else if err != nil {
		return nil, err
	}
	defer f.Close()

	stats := make(MeasurementStats)
	if _, err := stats.ReadFrom(bufio.NewReader(f)); err != nil {
		return nil, err
	}
	return stats, err
}

// Close closes the TSMReader.
func (t *TSMReader) Close() error {
	t.refsWG.Wait()

	t.mu.Lock()
	defer t.mu.Unlock()

	if err := t.accessor.close(); err != nil {
		return err
	}

	return t.index.Close()
}

// Ref records a usage of this TSMReader.  If there are active references
// when the reader is closed or removed, the reader will remain open until
// there are no more references.
func (t *TSMReader) Ref() {
	atomic.AddInt64(&t.refs, 1)
	t.refsWG.Add(1)
}

// Unref removes a usage record of this TSMReader.  If the Reader was closed
// by another goroutine while there were active references, the file will
// be closed and remove
func (t *TSMReader) Unref() {
	atomic.AddInt64(&t.refs, -1)
	t.refsWG.Done()
}

// InUse returns whether the TSMReader currently has any active references.
func (t *TSMReader) InUse() bool {
	refs := atomic.LoadInt64(&t.refs)
	return refs > 0
}

// Remove removes any underlying files stored on disk for this reader.
func (t *TSMReader) Remove() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.remove()
}

// Rename renames the underlying file to the new path.
func (t *TSMReader) Rename(path string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.accessor.rename(path)
}

// Remove removes any underlying files stored on disk for this reader.
func (t *TSMReader) remove() error {
	path := t.accessor.path()

	if t.InUse() {
		return ErrFileInUse
	}

	if path != "" {
		if err := os.RemoveAll(path); err != nil {
			return err
		} else if err := os.RemoveAll(StatsFilename(path)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}

	if err := t.tombstoner.Delete(); err != nil {
		return err
	}
	return nil
}

// Contains returns whether the given key is present in the index.
func (t *TSMReader) Contains(key []byte) bool {
	return t.index.Contains(key)
}

// ContainsValue returns true if key and time might exists in this file.  This function could
// return true even though the actual point does not exist.  For example, the key may
// exist in this file, but not have a point exactly at time t.
func (t *TSMReader) ContainsValue(key []byte, ts int64) bool {
	return t.index.ContainsValue(key, ts)
}

// DeleteRange removes the given points for keys between minTime and maxTime.   The series
// keys passed in must be sorted.
func (t *TSMReader) DeleteRange(keys [][]byte, minTime, maxTime int64) error {
	if len(keys) == 0 {
		return nil
	}

	batch := t.BatchDelete()
	if err := batch.DeleteRange(keys, minTime, maxTime); err != nil {
		batch.Rollback()
		return err
	}
	return batch.Commit()
}

// Delete deletes blocks indicated by keys.
func (t *TSMReader) Delete(keys [][]byte) error {
	if err := t.tombstoner.Add(keys); err != nil {
		return err
	}

	if err := t.tombstoner.Flush(); err != nil {
		return err
	}

	t.index.Delete(keys)
	return nil
}

// Iterator returns an iterator over the keys starting at the provided key. You must
// call Next before calling any of the accessors.
func (t *TSMReader) Iterator(key []byte) *TSMIndexIterator {
	return t.index.Iterator(key)
}

// OverlapsTimeRange returns true if the time range of the file intersect min and max.
func (t *TSMReader) OverlapsTimeRange(min, max int64) bool {
	return t.index.OverlapsTimeRange(min, max)
}

// OverlapsKeyRange returns true if the key range of the file intersect min and max.
func (t *TSMReader) OverlapsKeyRange(min, max []byte) bool {
	return t.index.OverlapsKeyRange(min, max)
}

// TimeRange returns the min and max time across all keys in the file.
func (t *TSMReader) TimeRange() (int64, int64) {
	return t.index.TimeRange()
}

// KeyRange returns the min and max key across all keys in the file.
func (t *TSMReader) KeyRange() ([]byte, []byte) {
	return t.index.KeyRange()
}

// KeyCount returns the count of unique keys in the TSMReader.
func (t *TSMReader) KeyCount() int {
	return t.index.KeyCount()
}

// Entries returns all index entries for key.
func (t *TSMReader) Entries(key []byte) ([]IndexEntry, error) {
	return t.index.Entries(key)
}

// ReadEntries reads the index entries for key into entries.
func (t *TSMReader) ReadEntries(key []byte, entries []IndexEntry) ([]IndexEntry, error) {
	return t.index.ReadEntries(key, entries)
}

// IndexSize returns the size of the index in bytes.
func (t *TSMReader) IndexSize() uint32 {
	return t.index.Size()
}

// Size returns the size of the underlying file in bytes.
func (t *TSMReader) Size() uint32 {
	t.mu.RLock()
	size := t.size
	t.mu.RUnlock()
	return uint32(size)
}

// LastModified returns the last time the underlying file was modified.
func (t *TSMReader) LastModified() int64 {
	t.mu.RLock()
	lm := t.lastModified
	for _, ts := range t.tombstoner.TombstoneFiles() {
		if ts.LastModified > lm {
			lm = ts.LastModified
		}
	}
	t.mu.RUnlock()
	return lm
}

// HasTombstones return true if there are any tombstone entries recorded.
func (t *TSMReader) HasTombstones() bool {
	t.mu.RLock()
	b := t.tombstoner.HasTombstones()
	t.mu.RUnlock()
	return b
}

// TombstoneFiles returns any tombstone files associated with this TSM file.
func (t *TSMReader) TombstoneFiles() []FileStat {
	t.mu.RLock()
	fs := t.tombstoner.TombstoneFiles()
	t.mu.RUnlock()
	return fs
}

// TombstoneRange returns ranges of time that are deleted for the given key.
func (t *TSMReader) TombstoneRange(key []byte) []TimeRange {
	t.mu.RLock()
	tr := t.index.TombstoneRange(key)
	t.mu.RUnlock()
	return tr
}

// Stats returns the FileStat for the TSMReader's underlying file.
func (t *TSMReader) Stats() FileStat {
	minTime, maxTime := t.index.TimeRange()
	minKey, maxKey := t.index.KeyRange()
	return FileStat{
		Path:         t.Path(),
		Size:         t.Size(),
		LastModified: t.LastModified(),
		MinTime:      minTime,
		MaxTime:      maxTime,
		MinKey:       minKey,
		MaxKey:       maxKey,
		HasTombstone: t.tombstoner.HasTombstones(),
	}
}

// BlockIterator returns a BlockIterator for the underlying TSM file.
func (t *TSMReader) BlockIterator() *BlockIterator {
	t.mu.RLock()
	iter := t.index.Iterator([]byte{})
	t.mu.RUnlock()

	return &BlockIterator{
		r:    t,
		iter: iter,
	}
}

type BatchDeleter interface {
	DeleteRange(keys [][]byte, min, max int64) error
	Commit() error
	Rollback() error
}

type batchDelete struct {
	r *TSMReader
}

func (b *batchDelete) DeleteRange(keys [][]byte, minTime, maxTime int64) error {
	if len(keys) == 0 {
		return nil
	}

	// If the keys can't exist in this TSM file, skip it.
	minKey, maxKey := keys[0], keys[len(keys)-1]
	if !b.r.index.OverlapsKeyRange(minKey, maxKey) {
		return nil
	}

	// If the timerange can't exist in this TSM file, skip it.
	if !b.r.index.OverlapsTimeRange(minTime, maxTime) {
		return nil
	}

	if err := b.r.tombstoner.AddRange(keys, minTime, maxTime); err != nil {
		return err
	}

	return nil
}

func (b *batchDelete) Commit() error {
	defer b.r.deleteMu.Unlock()
	if err := b.r.tombstoner.Flush(); err != nil {
		return err
	}

	return b.r.applyTombstones()
}

func (b *batchDelete) Rollback() error {
	defer b.r.deleteMu.Unlock()
	return b.r.tombstoner.Rollback()
}

// BatchDelete returns a BatchDeleter.  Only a single goroutine may run a BatchDelete at a time.
// Callers must either Commit or Rollback the operation.
func (r *TSMReader) BatchDelete() BatchDeleter {
	r.deleteMu.Lock()
	return &batchDelete{r: r}
}

type BatchDeleters []BatchDeleter

func (a BatchDeleters) DeleteRange(keys [][]byte, min, max int64) error {
	errC := make(chan error, len(a))
	for _, b := range a {
		go func(b BatchDeleter) { errC <- b.DeleteRange(keys, min, max) }(b)
	}

	var err error
	for i := 0; i < len(a); i++ {
		dErr := <-errC
		if dErr != nil {
			err = dErr
		}
	}
	return err
}

func (a BatchDeleters) Commit() error {
	errC := make(chan error, len(a))
	for _, b := range a {
		go func(b BatchDeleter) { errC <- b.Commit() }(b)
	}

	var err error
	for i := 0; i < len(a); i++ {
		dErr := <-errC
		if dErr != nil {
			err = dErr
		}
	}
	return err
}

func (a BatchDeleters) Rollback() error {
	errC := make(chan error, len(a))
	for _, b := range a {
		go func(b BatchDeleter) { errC <- b.Rollback() }(b)
	}

	var err error
	for i := 0; i < len(a); i++ {
		dErr := <-errC
		if dErr != nil {
			err = dErr
		}
	}
	return err
}

// indirectIndex is a TSMIndex that uses a raw byte slice representation of an index.  This
// implementation can be used for indexes that may be MMAPed into memory.
type indirectIndex struct {
	mu sync.RWMutex

	// indirectIndex works a follows.  Assuming we have an index structure in memory as
	// the diagram below:
	//
	// ┌─-───────┬─────────┬────┬──-──────┬─────────┬──────┬───-─────┬──────┐
	// │Key 1 Len│   Key   │... │Key 2 Len│  Key 2  │ ...  │  Key 3  │ ...  │
	// │ 2 bytes │ N bytes │    │ 2 bytes │ N bytes │      │ 2 bytes │      │
	// └─────────┴─────────┴────┴─────────┴─────────┴──────┴─────────┴──────┘

	// We then build a b+tree of the keys (which can be done efficiently because we know
	// the keys are in order) mapping to their first entry in the index.
	// When we have identified the first enty in the index for a given
	// key, we could perform another binary search or a linear scan.  This
	// should be fast as well since each index entry is 28 bytes and all
	// contiguous in memory.  The current implementation uses a linear scan since the
	// number of block entries is expected to be < 100 per key.

	// b is the underlying index byte slice.  This could be a copy on the heap or an MMAP
	// slice reference
	b []byte

	// tree contains a b+tree of keys to their starting offsets in the index.
	tree *bptree.T

	// minKey, maxKey are the minium and maximum (lexicographically sorted) contained in the
	// file
	minKey, maxKey []byte

	// minTime, maxTime are the minimum and maximum times contained in the file across all
	// series.
	minTime, maxTime int64

	// tombstones contains only the tombstoned keys with subset of time values deleted.  An
	// entry would exist here if a subset of the points for a key were deleted and the file
	// had not be re-compacted to remove the points on disk. It uses the offset of the key to
	// identify it.
	tombstones map[uint32][]TimeRange
}

// TimeRange holds a min and max timestamp.
type TimeRange struct {
	Min, Max int64
}

func (t TimeRange) Overlaps(min, max int64) bool {
	return t.Min <= max && t.Max >= min
}

// NewIndirectIndex returns a new indirect index.
func NewIndirectIndex() *indirectIndex {
	return &indirectIndex{
		tombstones: make(map[uint32][]TimeRange),
	}
}

// treeEntry reports the b+tree entry for the key and a boolean if it existed.
func (d *indirectIndex) treeEntry(key []byte) (*bptree.Entry, bool) {
	d.mu.RLock()
	ent, exact := d.treeEntryLocked(key)
	d.mu.RUnlock()
	return ent, exact
}

// treeEntryLocked reports the b+tree entry for the key and a boolean if it existed.
// It must be called with the mutex.
func (d *indirectIndex) treeEntryLocked(key []byte) (*bptree.Entry, bool) {
	iter := d.tree.Iterator()
	exact, _ := iter.Seek(key, d.b)
	ent := iter.Entry()
	return ent, exact
}

// ContainsKey returns true of key may exist in this index.
func (d *indirectIndex) ContainsKey(key []byte) bool {
	return bytes.Compare(key, d.minKey) >= 0 && bytes.Compare(key, d.maxKey) <= 0
}

// Entries returns all index entries for a key.
func (d *indirectIndex) Entries(key []byte) ([]IndexEntry, error) {
	return d.ReadEntries(key, nil)
}

// ReadEntries returns all index entries for a key.
func (d *indirectIndex) ReadEntries(key []byte, entries []IndexEntry) ([]IndexEntry, error) {
	if !d.ContainsKey(key) {
		return nil, nil
	}

	ent, ok := d.treeEntry(key)
	if !ok {
		return nil, nil
	}

	return readEntries(d.b[ent.EntryOffset(d.b):], entries)
}

// Entry returns the index entry for the specified key and timestamp.  If no entry
// matches the key an timestamp, nil is returned.
func (d *indirectIndex) Entry(key []byte, timestamp int64) (*IndexEntry, error) {
	entries, err := d.Entries(key)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.Contains(timestamp) {
			return &entry, nil
		}
	}
	return nil, nil
}

// KeyCount returns the count of unique keys in the index.
func (d *indirectIndex) KeyCount() int {
	d.mu.RLock()
	n := int(d.tree.Count())
	d.mu.RUnlock()
	return n
}

// Iterator returns an iterator over the keys starting at the provided key. You must
// call Next before calling any of the accessors.
func (d *indirectIndex) Iterator(key []byte) *TSMIndexIterator {
	d.mu.RLock()
	iter := d.tree.Iterator()
	_, ok := iter.Seek(key, d.b)

	ti := &TSMIndexIterator{
		d:     d,
		n:     int(d.tree.Count()),
		b:     d.b,
		iter:  iter,
		first: true,
		ok:    ok,
		ent:   iter.Entry(),
	}
	d.mu.RUnlock()

	return ti
}

// Delete removes the given keys from the index.
func (d *indirectIndex) Delete(keys [][]byte) {
	if len(keys) == 0 {
		return
	}

	d.mu.Lock()
	iter := d.tree.Iterator()
	for _, key := range keys {
		// often, the passed in keys are contiguous. calling DeleteEntry advances
		// the iterator, so just check if we're on the right key to avoid seeking.
		if !iter.CheckKey(key, d.b) {
			exact, _ := iter.Seek(key, d.b)
			if !exact {
				continue
			}
		}

		delete(d.tombstones, iter.Entry().Offset())
		iter.DeleteEntry()
	}
	d.mu.Unlock()
}

// DeleteRange removes the given keys with data between minTime and maxTime from the index.
func (d *indirectIndex) DeleteRange(keys [][]byte, minTime, maxTime int64) {
	// If we're deleting everything, we won't need to worry about partial deletes.
	if minTime == math.MinInt64 && maxTime == math.MaxInt64 {
		d.Delete(keys)
		return
	}

	// Is the range passed in outside of the time range for the file?
	min, max := d.TimeRange()
	if minTime > max || maxTime < min {
		return
	}

	// insertTombstone inserts a time range for the minTime and
	// maxTime into the sorted list of tombstones. Sadly, we have
	// to make a copy here in case of concurrent DeleteRange calls.
	var tsc []TimeRange
	insertTombstone := func(ts []TimeRange) []TimeRange {
		n := sort.Search(len(ts), func(i int) bool {
			if ts[i].Min == minTime {
				return ts[i].Max >= maxTime
			}
			return ts[i].Min > minTime
		})

		if cap(tsc) < len(ts)+1 {
			tsc = make([]TimeRange, 0, len(ts)+1)
		}
		tsc = append(tsc[:0], ts[:n]...)
		tsc = append(tsc, TimeRange{minTime, maxTime})
		tsc = append(tsc, ts[n:]...)
		return tsc
	}

	// General outline:
	// Under the read lock, determine the set of actions we need to
	// take and on what keys to take them. Then, under the write
	// lock, perform those actions. We keep track of some state
	// during the read lock to make double checking under the
	// write lock cheap.

	// tombstones maps the index of the key to the desired list
	// of sorted tombstones after the delete. As a special case,
	// if the Ranges field is nil, that means that the key
	// should be deleted.
	type tombstoneEntry struct {
		KeyOffset uint32
		Ranges    []TimeRange
	}

	d.mu.RLock()
	iter := d.tree.Iterator()
	var entries []IndexEntry
	var tombstones []tombstoneEntry
	var err error

	for _, key := range keys {
		// often, the passed in keys are contiguous. check the iterator's next entry to see
		// if it matches the key to avoid a seek.
		if !iter.Next() || !iter.CheckKey(key, d.b) {
			exact, _ := iter.Seek(key, d.b)
			if !exact {
				continue
			}
		}

		ent := iter.Entry()
		keyOffset := ent.Offset()
		entries, err = readEntries(d.b[ent.EntryOffset(d.b):], entries)
		if err != nil {
			// If we have an error reading the entries for a key, we should just pretend
			// the whole key is deleted. Maybe a better idea is to report this up somehow
			// but that's for another time.
			tombstones = append(tombstones, tombstoneEntry{KeyOffset: keyOffset})
			continue
		}

		// Is the time range passed outside of the time range we have stored for this key?
		min, max := entries[0].MinTime, entries[len(entries)-1].MaxTime
		if minTime > max || maxTime < min {
			continue
		}

		// Does the range passed in cover every value for the key?
		if minTime <= min && maxTime >= max {
			tombstones = append(tombstones, tombstoneEntry{KeyOffset: keyOffset})
			continue
		}

		// Get the sorted list of tombstones with our new range and check
		// to see if they fully cover the key's entries.
		ts := insertTombstone(d.tombstones[keyOffset])
		if timeRangesCoverEntries(ts, entries) {
			tombstones = append(tombstones, tombstoneEntry{KeyOffset: keyOffset})
			continue
		}

		// We're adding a tombstone. Store a copy because `insertTombstone` reuses
		// the same slice across calls.
		tombstones = append(tombstones, tombstoneEntry{
			KeyOffset: keyOffset,
			Ranges:    append([]TimeRange(nil), ts...),
		})
	}
	d.mu.RUnlock()

	// Nothing in tombstones means no deletes are going to happen.
	if len(tombstones) == 0 {
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	// Reset the iterator in case it became invalid during
	// modifications after dropping the read lock.
	iter = d.tree.Iterator()

	for _, tsEntry := range tombstones {
		// often, the passed in keys are contiguous. check the iterator's next entry to see
		// if it matches the key to avoid a seek.
		if !iter.Next() || iter.Entry().Offset() != tsEntry.KeyOffset {
			exact, _ := iter.SeekOffset(tsEntry.KeyOffset)
			if !exact {
				// Someone else already deleted this key. Clean
				// up the tombstones map just in case.
				delete(d.tombstones, tsEntry.KeyOffset)
				continue
			}
		}

		// If the desired list is nil, the key should be
		// fully deleted.
		if tsEntry.Ranges == nil {
			delete(d.tombstones, tsEntry.KeyOffset)
			iter.DeleteEntry()
			continue
		}

		// Check the existing tombstones. If the length did not
		// change, then we know that we don't need to double
		// check coverage, since we only ever increase the
		// number of tombstones for a key.
		dts := d.tombstones[tsEntry.KeyOffset]
		if len(dts) == len(tsEntry.Ranges)-1 {
			d.tombstones[tsEntry.KeyOffset] = tsEntry.Ranges
			continue
		}

		// Since the length changed, we have to do the expensive overlap check again.
		// We re-read the entries again under the write lock because this should be
		// rare and only during concurrent deletes to the same key. We could make
		// a copy of the entries before getting here, but that penalizes the common
		// no-concurrent case.
		entries, err = readEntries(d.b[iter.Entry().EntryOffset(d.b):], entries)
		if err != nil {
			// If we have an error reading the entries for a key, we should just pretend
			// the whole key is deleted. Maybe a better idea is to report this up somehow
			// but that's for another time.
			delete(d.tombstones, tsEntry.KeyOffset)
			iter.DeleteEntry()
			continue
		}

		ts := insertTombstone(dts)
		if timeRangesCoverEntries(ts, entries) {
			delete(d.tombstones, tsEntry.KeyOffset)
			iter.DeleteEntry()
		} else {
			d.tombstones[tsEntry.KeyOffset] = append([]TimeRange(nil), ts...)
		}
	}
}

// TombstoneRange returns ranges of time that are deleted for the given key.
func (d *indirectIndex) TombstoneRange(key []byte) []TimeRange {
	var r []TimeRange
	d.mu.RLock()
	ent, exact := d.treeEntryLocked(key)
	if exact {
		r = d.tombstones[ent.Offset()]
	}
	d.mu.RUnlock()
	return r
}

// Contains return true if the given key exists in the index.
func (d *indirectIndex) Contains(key []byte) bool {
	d.mu.RLock()
	ok := d.tree.Has(key, d.b)
	d.mu.RUnlock()
	return ok
}

// ContainsValue returns true if key and time might exist in this file.
func (d *indirectIndex) ContainsValue(key []byte, timestamp int64) bool {
	entry, err := d.Entry(key, timestamp)
	if entry == nil || err != nil {
		return false
	}

	for _, t := range d.TombstoneRange(key) {
		if t.Min <= timestamp && t.Max >= timestamp {
			return false
		}
	}
	return true
}

// Type returns the block type of the values stored for the key.
func (d *indirectIndex) Type(key []byte) (byte, error) {
	ent, ok := d.treeEntry(key)
	if !ok {
		return 0, fmt.Errorf("key does not exist: %s", key)
	}
	return d.b[ent.EntryOffset(d.b)], nil
}

// OverlapsTimeRange returns true if the time range of the file intersect min and max.
func (d *indirectIndex) OverlapsTimeRange(min, max int64) bool {
	return d.minTime <= max && d.maxTime >= min
}

// OverlapsKeyRange returns true if the min and max keys of the file overlap the arguments min and max.
func (d *indirectIndex) OverlapsKeyRange(min, max []byte) bool {
	return bytes.Compare(d.minKey, max) <= 0 && bytes.Compare(d.maxKey, min) >= 0
}

// KeyRange returns the min and max keys in the index.
func (d *indirectIndex) KeyRange() ([]byte, []byte) {
	return d.minKey, d.maxKey
}

// TimeRange returns the min and max time across all keys in the index.
func (d *indirectIndex) TimeRange() (int64, int64) {
	return d.minTime, d.maxTime
}

// MarshalBinary returns a byte slice encoded version of the index.
func (d *indirectIndex) MarshalBinary() ([]byte, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	return d.b, nil
}

// UnmarshalBinary populates an index from an encoded byte slice
// representation of an index.
func (d *indirectIndex) UnmarshalBinary(b []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Keep a reference to the actual index bytes
	d.b = b
	if len(b) == 0 {
		return nil
	}

	var minKey, maxKey []byte
	var bulk bptree.Bulk
	var minTime, maxTime int64 = math.MaxInt64, 0
	var i uint32
	iMax := uint32(len(b))
	for i < iMax {
		// First two bytes are the key length
		if i+2 >= iMax {
			return fmt.Errorf("indirectIndex: not enough data for key length value")
		}
		keyLen := uint32(binary.BigEndian.Uint16(b[i : i+2]))

		// Then the key follows
		if i+2+keyLen >= iMax {
			return fmt.Errorf("indirectIndex: not enough data for key")
		}
		key := b[i+2 : i+2+keyLen]

		// Record minimum and maximum keys.
		maxKey = key
		if minKey == nil {
			minKey = key
		}

		// Append the entry into our tree. The offset should be at the start of the entry.
		bulk.Append(bptree.NewEntry(key, i))

		// Advance the index. The byte after the key is the type.
		i += 2 + keyLen + 1

		// Next is the count of the index entries.
		if i+indexCountSize >= iMax {
			return fmt.Errorf("indirectIndex: not enough data for index entries count")
		}
		count := uint32(binary.BigEndian.Uint16(b[i : i+indexCountSize]))
		i += indexCountSize

		// There should always be at least one entry.
		if count == 0 {
			return fmt.Errorf("indirectIndex: key with no index entries")
		}

		// Find the min time for the block
		if i+8 >= iMax {
			return fmt.Errorf("indirectIndex: not enough data for min time")
		}
		minT := int64(binary.BigEndian.Uint64(b[i : i+8]))
		if minT < minTime {
			minTime = minT
		}

		// Advance to the last block.
		i += (count - 1) * indexEntrySize

		// Find the max time for the block
		if i+16 >= iMax {
			return fmt.Errorf("indirectIndex: not enough data for max time")
		}
		maxT := int64(binary.BigEndian.Uint64(b[i+8 : i+16]))
		if maxT > maxTime {
			maxTime = maxT
		}

		// Advance to the next key.
		i += indexEntrySize
	}

	d.minKey = minKey
	d.maxKey = maxKey
	d.minTime = minTime
	d.maxTime = maxTime
	d.tree = bulk.Done()
	return nil
}

// Size returns the size of the current index in bytes.
func (d *indirectIndex) Size() uint32 {
	d.mu.RLock()
	defer d.mu.RUnlock()

	return uint32(len(d.b))
}

// Close .. closes the indirect index.
func (d *indirectIndex) Close() error {
	return nil
}

// mmapAccess is mmap based block accessor.  It access blocks through an
// MMAP file interface.
type mmapAccessor struct {
	accessCount uint64 // Counter incremented everytime the mmapAccessor is accessed
	freeCount   uint64 // Counter to determine whether the accessor can free its resources

	mmapWillNeed bool // If true then mmap advise value MADV_WILLNEED will be provided the kernel for b.

	mu sync.RWMutex
	b  []byte
	f  *os.File

	index *indirectIndex
}

func (m *mmapAccessor) init() (*indirectIndex, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := verifyVersion(m.f); err != nil {
		return nil, err
	}

	var err error

	if _, err := m.f.Seek(0, 0); err != nil {
		return nil, err
	}

	stat, err := m.f.Stat()
	if err != nil {
		return nil, err
	}

	m.b, err = mmap(m.f, 0, int(stat.Size()))
	if err != nil {
		return nil, err
	}
	if len(m.b) < 8 {
		return nil, fmt.Errorf("mmapAccessor: byte slice too small for indirectIndex")
	}

	// Hint to the kernel that we will be reading the file.  It would be better to hint
	// that we will be reading the index section, but that's not been
	// implemented as yet.
	if m.mmapWillNeed {
		if err := madviseWillNeed(m.b); err != nil {
			return nil, err
		}
	}

	indexOfsPos := len(m.b) - 8
	indexStart := binary.BigEndian.Uint64(m.b[indexOfsPos : indexOfsPos+8])
	if indexStart >= uint64(indexOfsPos) {
		return nil, fmt.Errorf("mmapAccessor: invalid indexStart")
	}

	m.index = NewIndirectIndex()
	if err := m.index.UnmarshalBinary(m.b[indexStart:indexOfsPos]); err != nil {
		return nil, err
	}

	// Allow resources to be freed immediately if requested
	m.incAccess()
	atomic.StoreUint64(&m.freeCount, 1)

	return m.index, nil
}

func (m *mmapAccessor) free() error {
	accessCount := atomic.LoadUint64(&m.accessCount)
	freeCount := atomic.LoadUint64(&m.freeCount)

	// Already freed everything.
	if freeCount == 0 && accessCount == 0 {
		return nil
	}

	// Were there accesses after the last time we tried to free?
	// If so, don't free anything and record the access count that we
	// see now for the next check.
	if accessCount != freeCount {
		atomic.StoreUint64(&m.freeCount, accessCount)
		return nil
	}

	// Reset both counters to zero to indicate that we have freed everything.
	atomic.StoreUint64(&m.accessCount, 0)
	atomic.StoreUint64(&m.freeCount, 0)

	m.mu.RLock()
	defer m.mu.RUnlock()

	return madviseDontNeed(m.b)
}

func (m *mmapAccessor) incAccess() {
	atomic.AddUint64(&m.accessCount, 1)
}

func (m *mmapAccessor) rename(path string) error {
	m.incAccess()

	m.mu.Lock()
	defer m.mu.Unlock()

	err := munmap(m.b)
	if err != nil {
		return err
	}

	if err := m.f.Close(); err != nil {
		return err
	}

	if err := file.RenameFile(m.f.Name(), path); err != nil {
		return err
	}

	m.f, err = os.Open(path)
	if err != nil {
		return err
	}

	if _, err := m.f.Seek(0, 0); err != nil {
		return err
	}

	stat, err := m.f.Stat()
	if err != nil {
		return err
	}

	m.b, err = mmap(m.f, 0, int(stat.Size()))
	if err != nil {
		return err
	}

	if m.mmapWillNeed {
		return madviseWillNeed(m.b)
	}
	return nil
}

func (m *mmapAccessor) read(key []byte, timestamp int64) ([]Value, error) {
	entry, err := m.index.Entry(key, timestamp)
	if entry == nil || err != nil {
		return nil, err
	}

	return m.readBlock(entry, nil)
}

func (m *mmapAccessor) readBlock(entry *IndexEntry, values []Value) ([]Value, error) {
	m.incAccess()

	m.mu.RLock()
	defer m.mu.RUnlock()

	if int64(len(m.b)) < entry.Offset+int64(entry.Size) {
		return nil, ErrTSMClosed
	}
	//TODO: Validate checksum
	var err error
	values, err = DecodeBlock(m.b[entry.Offset+4:entry.Offset+int64(entry.Size)], values)
	if err != nil {
		return nil, err
	}

	return values, nil
}

func (m *mmapAccessor) readBytes(entry *IndexEntry, b []byte) (uint32, []byte, error) {
	m.incAccess()

	m.mu.RLock()
	if int64(len(m.b)) < entry.Offset+int64(entry.Size) {
		m.mu.RUnlock()
		return 0, nil, ErrTSMClosed
	}

	// return the bytes after the 4 byte checksum
	crc, block := binary.BigEndian.Uint32(m.b[entry.Offset:entry.Offset+4]), m.b[entry.Offset+4:entry.Offset+int64(entry.Size)]
	m.mu.RUnlock()

	return crc, block, nil
}

// readAll returns all values for a key in all blocks.
func (m *mmapAccessor) readAll(key []byte) ([]Value, error) {
	m.incAccess()

	blocks, err := m.index.Entries(key)
	if len(blocks) == 0 || err != nil {
		return nil, err
	}

	tombstones := m.index.TombstoneRange(key)

	m.mu.RLock()
	defer m.mu.RUnlock()

	var temp []Value
	var values []Value
	for _, block := range blocks {
		var skip bool
		for _, t := range tombstones {
			// Should we skip this block because it contains points that have been deleted
			if t.Min <= block.MinTime && t.Max >= block.MaxTime {
				skip = true
				break
			}
		}

		if skip {
			continue
		}
		//TODO: Validate checksum
		temp = temp[:0]
		// The +4 is the 4 byte checksum length
		temp, err = DecodeBlock(m.b[block.Offset+4:block.Offset+int64(block.Size)], temp)
		if err != nil {
			return nil, err
		}

		// Filter out any values that were deleted
		for _, t := range tombstones {
			temp = Values(temp).Exclude(t.Min, t.Max)
		}

		values = append(values, temp...)
	}

	return values, nil
}

func (m *mmapAccessor) path() string {
	m.mu.RLock()
	path := m.f.Name()
	m.mu.RUnlock()
	return path
}

func (m *mmapAccessor) close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.b == nil {
		return nil
	}

	err := munmap(m.b)
	if err != nil {
		return err
	}

	m.b = nil
	return m.f.Close()
}

// readEntries reads entries from the buffer assuming it starts right after the key.
func readEntries(b []byte, entries []IndexEntry) ([]IndexEntry, error) {
	if len(b) < indexCountSize+indexTypeSize {
		return nil, errors.New("readEntries: data too short for headers")
	}

	// 2 byte count of index entries
	count := int(binary.BigEndian.Uint16(b[indexTypeSize : indexTypeSize+indexCountSize]))
	if cap(entries) < count {
		entries = make([]IndexEntry, count)
	} else {
		entries = entries[:count]
	}

	b = b[indexCountSize+indexTypeSize:]
	for i := 0; i < len(entries); i++ {
		if err := entries[i].UnmarshalBinary(b); err != nil {
			return nil, err
		}
		b = b[indexEntrySize:]
	}

	return entries, nil
}

// timeRangesCoverEntries returns true if the time ranges fully cover the entries.
func timeRangesCoverEntries(ts []TimeRange, entries []IndexEntry) (covers bool) {
	mustCover := entries[0].MinTime
	for len(entries) > 0 && len(ts) > 0 {
		switch {
		// If the tombstone does not include mustCover, we
		// know we do not fully cover every entry.
		case ts[0].Min > mustCover:
			return false

		// Otherwise, if the tombstone covers the rest of
		// the entry, consume it and bump mustCover to the
		// start of the next entry.
		case ts[0].Max >= entries[0].MaxTime:
			entries = entries[1:]
			if len(entries) > 0 {
				mustCover = entries[0].MinTime
			}

		// Otherwise, we're still inside of an entry, and
		// so the tombstone must adjoin the current tombstone.
		default:
			if ts[0].Max >= mustCover {
				mustCover = ts[0].Max + 1
			}
			ts = ts[1:]
		}
	}

	return len(entries) == 0
}
