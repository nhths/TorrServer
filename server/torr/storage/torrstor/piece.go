package torrstor

import (
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/storage"
	"server/settings"
)

type Piece struct {
	storage.PieceImpl `json:"-"`

	Id   int   `json:"-"`
	Size int64 `json:"size"`

	Complete bool  `json:"complete"`
	Accessed int64 `json:"accessed"`

	// mPiece is the in-RAM streaming piece. It is always
	// present, even when the cache is "pinned" to the archive,
	// because the streaming reader still needs a place to put
	// bytes between disk and client.
	mPiece *MemPiece `json:"-"`
	// aPiece is the long-term archive piece. It is non-nil
	// only when the cache has been pinned by the rare-seed
	// policy and the storage has an ArchivePath configured.
	aPiece *ArchivePiece `json:"-"`
	// dPiece is the on-disk piece for the streaming cache.
	// It exists only when settings.BTsets.UseDisk is true.
	dPiece *DiskPiece `json:"-"`

	cache *Cache `json:"-"`
}

func NewPiece(id int, cache *Cache) *Piece {
	p := &Piece{
		Id:    id,
		cache: cache,
	}
	p.mPiece = NewMemPiece(p)
	if settings.BTsets.UseDisk {
		p.dPiece = NewDiskPiece(p)
	}
	return p
}

// ensureArchivePiece lazily creates the archive piece once
// the cache is pinned and an archive path is configured.
// Returns nil if no archive path is configured.
func (p *Piece) ensureArchivePiece() *ArchivePiece {
	if p.cache == nil || p.cache.storage == nil {
		return nil
	}
	if p.aPiece != nil {
		return p.aPiece
	}
	if p.cache.storage.ArchivePath() == "" {
		return nil
	}
	p.aPiece = NewArchivePiece(p)
	return p.aPiece
}

func (p *Piece) WriteAt(b []byte, off int64) (n int, err error) {
	// When the cache is pinned for archival, write to the
	// archive piece in addition to the in-memory piece so the
	// payload survives an LRU eviction of streaming state.
	if p.cache.IsPinned() {
		if ap := p.ensureArchivePiece(); ap != nil {
			if _, err := ap.WriteAt(b, off); err != nil {
				return 0, err
			}
		}
	}
	if p.dPiece != nil {
		return p.dPiece.WriteAt(b, off)
	}
	return p.mPiece.WriteAt(b, off)
}

func (p *Piece) ReadAt(b []byte, off int64) (n int, err error) {
	// Read from the archive first when the cache is pinned
	// and the streaming layer does not have the data yet.
	if p.cache.IsPinned() && p.aPiece != nil && p.Size < off+int64(len(b)) {
		if n, err := p.aPiece.ReadAt(b, off); err == nil {
			return n, nil
		}
	}
	if p.dPiece != nil {
		return p.dPiece.ReadAt(b, off)
	}
	return p.mPiece.ReadAt(b, off)
}

func (p *Piece) MarkComplete() error {
	p.Complete = true
	return nil
}

func (p *Piece) MarkNotComplete() error {
	p.Complete = false
	return nil
}

func (p *Piece) Completion() storage.Completion {
	return storage.Completion{
		Complete: p.Complete,
		Ok:       true,
	}
}

func (p *Piece) Release() {
	if !settings.BTsets.UseDisk {
		p.mPiece.Release()
	} else {
		p.dPiece.Release()
	}
	//if !p.cache.isClosed {
		p.cache.torrent.Piece(p.Id).SetPriority(torrent.PiecePriorityNone)
		p.cache.torrent.Piece(p.Id).UpdateCompletion()
	//}
}
