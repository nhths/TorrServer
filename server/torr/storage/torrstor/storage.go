package torrstor

import (
	"os"
	"sort"
	"sync"
	"time"

	"server/log"
	"server/torr/storage"

	"github.com/anacrolix/torrent/metainfo"
	ts "github.com/anacrolix/torrent/storage"
)

type Storage struct {
	storage.Storage

	caches   map[metainfo.Hash]*Cache
	capacity int64
	mu       sync.Mutex

	// diskBudgetBytes is the on-disk quota for cached pieces.
	// 0 disables the LRU eviction policy. Read/written under
	// s.mu by SetDiskBudget and read by JettisonIfOver.
	diskBudgetBytes int64

	// diskUsedBytes is the running total of on-disk piece
	// bytes. Maintained incrementally by AddDiskUsed /
	// SubDiskUsed (which acquire diskUsedMu) and reconciled
	// by RefreshDiskUsedIfStale.
	diskUsedBytes int64
	diskUsedMu    sync.RWMutex

	// lastDiskVerify is the wall-clock time of the last
	// stat-walk that reconciled diskUsedBytes. Read by
	// RefreshDiskUsedIfStale to throttle the walk.
	lastDiskVerify time.Time

	// jettisonMu serialises eviction passes so that a
	// ticker-driven pass and a manual pass do not race on
	// the same cache.
	jettisonMu sync.Mutex
}

func NewStorage(capacity int64) *Storage {
	stor := new(Storage)
	stor.capacity = capacity
	stor.caches = make(map[metainfo.Hash]*Cache)
	return stor
}

// SetDiskBudget updates the on-disk quota. The next
// JettisonIfOver pass uses the new value; existing over-budget
// usage is reclaimed gradually by the eviction ticker.
func (s *Storage) SetDiskBudget(budget int64) {
	s.mu.Lock()
	s.diskBudgetBytes = budget
	s.mu.Unlock()
}

// DiskBudget returns the currently configured quota in bytes.
func (s *Storage) DiskBudget() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.diskBudgetBytes
}

// DiskUsed returns the current on-disk usage in bytes.
func (s *Storage) DiskUsed() int64 {
	s.diskUsedMu.RLock()
	defer s.diskUsedMu.RUnlock()
	return s.diskUsedBytes
}

// AddDiskUsed increments the on-disk counter by n (n>0). Piece
// creation sites call this; counter drift is corrected by
// RefreshDiskUsedIfStale.
func (s *Storage) AddDiskUsed(n int64) {
	if n <= 0 {
		return
	}
	s.diskUsedMu.Lock()
	s.diskUsedBytes += n
	s.diskUsedMu.Unlock()
}

// SubDiskUsed decrements the on-disk counter by n (n>0),
// clamped at zero. Piece release / file removal sites call
// this.
func (s *Storage) SubDiskUsed(n int64) {
	if n <= 0 {
		return
	}
	s.diskUsedMu.Lock()
	if n > s.diskUsedBytes {
		n = s.diskUsedBytes
	}
	s.diskUsedBytes -= n
	s.diskUsedMu.Unlock()
}

// OnDiskBytes sums the on-disk size of every piece in the
// cache that has a backing file. Used by RefreshDiskUsedIfStale
// to reconcile the counter.
func (c *Cache) OnDiskBytes() int64 {
	c.muReaders.Lock()
	defer c.muReaders.Unlock()
	var total int64
	for _, p := range c.pieces {
		if p.dPiece == nil {
			continue
		}
		if info, err := os.Stat(p.dPiece.name); err == nil {
			total += info.Size()
		}
	}
	return total
}

// MaxPieceAccess returns the most-recent Accessed timestamp of
// any piece in the cache, used as the LRU freshness signal.
func (c *Cache) MaxPieceAccess() int64 {
	c.muReaders.Lock()
	defer c.muReaders.Unlock()
	var max int64
	for _, p := range c.pieces {
		if p.Accessed > max {
			max = p.Accessed
		}
	}
	return max
}

// RefreshDiskUsedIfStale runs a stat-walk across every cache
// but only if more than 10 minutes have elapsed since the
// last pass. The walk itself is O(pieces) and safe to call
// from the eviction ticker.
func (s *Storage) RefreshDiskUsedIfStale() {
	const interval = 10 * time.Minute
	s.diskUsedMu.RLock()
	fresh := time.Since(s.lastDiskVerify) < interval
	s.diskUsedMu.RUnlock()
	if fresh {
		return
	}

	s.mu.Lock()
	caches := make([]*Cache, 0, len(s.caches))
	for _, c := range s.caches {
		caches = append(caches, c)
	}
	s.mu.Unlock()

	var total int64
	for _, c := range caches {
		total += c.OnDiskBytes()
	}
	s.diskUsedMu.Lock()
	s.diskUsedBytes = total
	s.lastDiskVerify = time.Now()
	s.diskUsedMu.Unlock()
}

// JettisonIfOver evicts the oldest torrents (by piece access
// freshness) until on-disk usage falls within the configured
// quota, or until no further cache can be evicted. It is the
// single entry point used by the rare-seed cache ticker.
func (s *Storage) JettisonIfOver() {
	s.mu.Lock()
	budget := s.diskBudgetBytes
	s.mu.Unlock()
	if budget <= 0 {
		return
	}

	s.jettisonMu.Lock()
	defer s.jettisonMu.Unlock()

	s.RefreshDiskUsedIfStale()
	if s.DiskUsed() <= budget {
		return
	}
	s.runJettison(s.DiskUsed() - budget)
}

// runJettison evicts caches until `need` bytes have been
// removed, or until every candidate has been tried. The
// caller must hold s.jettisonMu.
func (s *Storage) runJettison(need int64) {
	s.mu.Lock()
	caches := make([]*Cache, 0, len(s.caches))
	for _, c := range s.caches {
		caches = append(caches, c)
	}
	s.mu.Unlock()

	sort.Slice(caches, func(i, j int) bool {
		ai, aj := caches[i].MaxPieceAccess(), caches[j].MaxPieceAccess()
		if ai != aj {
			return ai < aj
		}
		return caches[i].hash.HexString() < caches[j].hash.HexString()
	})

	for _, c := range caches {
		if need <= 0 {
			return
		}
		if c.Readers() > 0 {
			// Active stream — never evict.
			continue
		}
		size := s.removeCacheFiles(c)
		if size == 0 {
			continue
		}
		need -= size
		log.TLogln("[LRU] jettison hash=", c.hash.HexString(),
			" bytes=", size, " reason=over_budget")
	}
}

// removeCacheFiles removes every piece file associated with
// the cache and debits the storage counter. The cache object
// itself stays in s.caches so the torrent remains alive and
// tracker-visible — only the on-disk payload is dropped.
func (s *Storage) removeCacheFiles(c *Cache) int64 {
	type pending struct {
		path string
		size int64
	}
	var pendingList []pending

	c.muReaders.Lock()
	for _, p := range c.pieces {
		if p.dPiece == nil {
			continue
		}
		path := p.dPiece.name
		if path == "" {
			continue
		}
		if info, err := os.Stat(path); err == nil {
			pendingList = append(pendingList, pending{path, info.Size()})
		}
	}
	c.muReaders.Unlock()

	var total int64
	for _, p := range pendingList {
		if err := os.Remove(p.path); err == nil {
			total += p.size
			s.SubDiskUsed(p.size)
		}
	}
	return total
}

func (s *Storage) OpenTorrent(info *metainfo.Info, infoHash metainfo.Hash) (ts.TorrentImpl, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ch, ok := s.caches[infoHash]; ok {
		return ch, nil
	}
	ch := NewCache(s.capacity, s)
	ch.Init(info, infoHash)
	s.caches[infoHash] = ch
	return ch, nil
}

// CloseHash removes the cache from s.caches under s.mu and
// closes it outside the lock so the filesystem I/O in Close
// cannot deadlock with the storage mutex.
func (s *Storage) CloseHash(hash metainfo.Hash) {
	if s.caches == nil {
		return
	}
	s.mu.Lock()
	ch, ok := s.caches[hash]
	if ok {
		delete(s.caches, hash)
	}
	s.mu.Unlock()
	if ok {
		ch.Close()
	}
}

func (s *Storage) Close() error {
	s.mu.Lock()
	snapshot := make([]*Cache, 0, len(s.caches))
	for _, ch := range s.caches {
		snapshot = append(snapshot, ch)
	}
	s.caches = make(map[metainfo.Hash]*Cache)
	s.mu.Unlock()
	for _, ch := range snapshot {
		ch.Close()
	}
	return nil
}

func (s *Storage) GetCache(hash metainfo.Hash) *Cache {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cache, ok := s.caches[hash]; ok {
		return cache
	}
	return nil
}

