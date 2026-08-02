package torrstor

import (
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"server/log"
	"server/settings"
	"server/torr/storage"

	"github.com/anacrolix/torrent/metainfo"
	ts "github.com/anacrolix/torrent/storage"
)

type Storage struct {
	storage.Storage

	caches   map[metainfo.Hash]*Cache
	capacity int64
	mu       sync.Mutex

	// archivePath is the on-disk root for the long-term
	// archive of rare-seed torrents. Empty disables the
	// archive subsystem entirely. Read by the cache when
	// deciding where a pinned piece's payload lives.
	archivePath string

	// archiveBudgetBytes caps archive size. 0 disables the
	// LRU evictor. Read/written under s.mu.
	archiveBudgetBytes int64

	// archiveUsedBytes is the running total of on-disk
	// archive bytes. Maintained by AddArchiveUsed /
	// SubArchiveUsed (under archiveUsedMu) and reconciled by
	// RefreshArchiveUsedIfStale.
	archiveUsedBytes int64
	archiveUsedMu    sync.RWMutex

	// lastArchiveVerify is the wall-clock time of the last
	// stat-walk that reconciled archiveUsedBytes.
	lastArchiveVerify time.Time

	// jettisonMu serialises eviction passes.
	jettisonMu sync.Mutex
}

func NewStorage(capacity int64) *Storage {
	stor := new(Storage)
	stor.capacity = capacity
	stor.caches = make(map[metainfo.Hash]*Cache)
	return stor
}

// SetArchivePath configures the on-disk root used by pinned
// torrents. Pass "" to disable the archive.
func (s *Storage) SetArchivePath(p string) {
	s.mu.Lock()
	s.archivePath = p
	s.mu.Unlock()
}

// ArchivePath returns the current archive root.
func (s *Storage) ArchivePath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.archivePath
}

// SetArchiveBudget updates the archive quota. 0 disables
// eviction. Existing over-budget usage is reclaimed
// gradually by the eviction ticker.
func (s *Storage) SetArchiveBudget(budget int64) {
	s.mu.Lock()
	s.archiveBudgetBytes = budget
	s.mu.Unlock()
}

// ArchiveBudget returns the currently configured quota.
func (s *Storage) ArchiveBudget() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.archiveBudgetBytes
}

// ArchiveUsed returns the current archive size in bytes.
func (s *Storage) ArchiveUsed() int64 {
	s.archiveUsedMu.RLock()
	defer s.archiveUsedMu.RUnlock()
	return s.archiveUsedBytes
}

// AddArchiveUsed / SubArchiveUsed are the counter hooks for
// the archive subsystem. Sub is clamped at zero; drift is
// reconciled by RefreshArchiveUsedIfStale.
func (s *Storage) AddArchiveUsed(n int64) {
	if n <= 0 {
		return
	}
	s.archiveUsedMu.Lock()
	s.archiveUsedBytes += n
	s.archiveUsedMu.Unlock()
}

func (s *Storage) SubArchiveUsed(n int64) {
	if n <= 0 {
		return
	}
	s.archiveUsedMu.Lock()
	if n > s.archiveUsedBytes {
		n = s.archiveUsedBytes
	}
	s.archiveUsedBytes -= n
	s.archiveUsedMu.Unlock()
}

// OnArchiveBytes sums the on-disk size of every piece in the
// cache that has an ArchivePiece. Used by
// RefreshArchiveUsedIfStale.
func (c *Cache) OnArchiveBytes() int64 {
	c.muReaders.Lock()
	defer c.muReaders.Unlock()
	var total int64
	for _, p := range c.pieces {
		if p.aPiece == nil {
			continue
		}
		if info, err := os.Stat(p.aPiece.name); err == nil {
			total += info.Size()
		}
	}
	return total
}

// MaxPieceAccess returns the most-recent Accessed timestamp of
// any piece in the cache, used as the LRU freshness signal
// for archive eviction.
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

// HasArchive reports whether the cache has any archive
// pieces on disk. Used by the eviction pass to skip caches
// that the archive is not actually consuming.
func (c *Cache) HasArchive() bool {
	c.muReaders.Lock()
	defer c.muReaders.Unlock()
	for _, p := range c.pieces {
		if p.aPiece != nil {
			return true
		}
	}
	return false
}

// RefreshArchiveUsedIfStale runs a stat-walk over the
// archive pieces of every cache, at most once per 10
// minutes. The walk is O(pieces) and safe to call from the
// eviction ticker.
func (s *Storage) RefreshArchiveUsedIfStale() {
	const interval = 10 * time.Minute
	s.archiveUsedMu.RLock()
	fresh := time.Since(s.lastArchiveVerify) < interval
	s.archiveUsedMu.RUnlock()
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
		total += c.OnArchiveBytes()
	}
	s.archiveUsedMu.Lock()
	s.archiveUsedBytes = total
	s.lastArchiveVerify = time.Now()
	s.archiveUsedMu.Unlock()
}

// JettisonArchiveIfOver evicts the oldest pinned (archived)
// torrents until archive usage falls within the configured
// budget, or until no further cache can be evicted. The
// streaming cache for an evicted torrent is preserved — only
// the archive copy is removed.
func (s *Storage) JettisonArchiveIfOver() {
	s.mu.Lock()
	path := s.archivePath
	budget := s.archiveBudgetBytes
	s.mu.Unlock()
	if path == "" || budget <= 0 {
		return
	}

	s.jettisonMu.Lock()
	defer s.jettisonMu.Unlock()

	s.RefreshArchiveUsedIfStale()
	if s.ArchiveUsed() <= budget {
		return
	}
	s.runArchiveJettison(s.ArchiveUsed() - budget)
}

// runArchiveJettison removes archive copies from the
// least-recently-accessed caches until `need` bytes have
// been freed. Caches with active readers are skipped.
func (s *Storage) runArchiveJettison(need int64) {
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
			// Active stream — never evict the archive.
			continue
		}
		if !c.HasArchive() {
			continue
		}
		size := s.removeArchiveFiles(c)
		if size == 0 {
			continue
		}
		need -= size
		log.TLogln("[archive LRU] jettison hash=", c.hash.HexString(),
			" bytes=", size, " reason=over_budget")
	}
}

// removeArchiveFiles deletes every archive piece file for
// the cache and debits the archive counter. The streaming
// cache and the torrent's BT client state are untouched.
func (s *Storage) removeArchiveFiles(c *Cache) int64 {
	type pending struct {
		path string
		size int64
	}
	var pendingList []pending

	c.muReaders.Lock()
	for _, p := range c.pieces {
		if p.aPiece == nil {
			continue
		}
		path := p.aPiece.name
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
			s.SubArchiveUsed(p.size)
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

// InitArchiveRoot ensures ArchivePath exists if a path is
// configured. Called by the BT server after the storage is
// constructed.
func (s *Storage) InitArchiveRoot() {
	if s == nil {
		return
	}
	s.mu.Lock()
	root := s.archivePath
	s.mu.Unlock()
	if root == "" {
		return
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		log.TLogln("archive mkdir:", err)
	}
}

// piecePath returns the per-torrent directory in the
// archive. The directory is created lazily on first write.
func (s *Storage) piecePath(hash metainfo.Hash, idx int) string {
	s.mu.Lock()
	root := s.archivePath
	s.mu.Unlock()
	if root == "" {
		return ""
	}
	dir := filepath.Join(root, hash.HexString())
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, itoa(idx))
}

// itoa is a tiny helper to avoid importing strconv in
// already-heavy files.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// settingsArchiver exposes the live archive settings to the
// piece layer without dragging in a circular import.
func settingsArchiver() (string, int64) {
	if settings.BTsets == nil {
		return "", 0
	}
	return settings.BTsets.ArchivePath, settings.BTsets.ArchiveBudgetBytes
}
