package torrstor

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"server/log"
)

// ArchivePiece is the long-term on-disk storage for a piece
// that the rare-seed policy has decided to keep. It lives
// under BTSets.ArchivePath (a separate, user-configured
// directory) and is independent of the streaming disk cache
// (BTSets.TorrentsSavePath) and the in-RAM cache.
type ArchivePiece struct {
	piece *Piece
	name  string

	mu sync.RWMutex
}

// NewArchivePiece returns an ArchivePiece rooted at the
// configured archive path. If the piece file already exists,
// the on-disk size is accounted for immediately so the
// archive's quota counter is correct from the first
// eviction pass.
func NewArchivePiece(p *Piece) *ArchivePiece {
	root := ""
	if p != nil && p.cache != nil && p.cache.storage != nil {
		root = p.cache.storage.ArchivePath()
	}
	if root == "" {
		return nil
	}
	name := filepath.Join(root, p.cache.hash.HexString(), strconv.Itoa(p.Id))
	if ff, err := os.Stat(name); err == nil {
		p.Size = ff.Size()
		p.Complete = ff.Size() == p.cache.pieceLength
		p.Accessed = ff.ModTime().Unix()
		if p.cache.storage != nil {
			p.cache.storage.AddArchiveUsed(ff.Size())
		}
	}
	return &ArchivePiece{piece: p, name: name}
}

func (p *ArchivePiece) WriteAt(b []byte, off int64) (n int, err error) {
	if p == nil {
		return 0, io.EOF
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(p.name), 0o755); err != nil {
		log.TLogln("archive mkdir:", err)
		return 0, err
	}
	ff, err := os.OpenFile(p.name, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		log.TLogln("archive open:", err)
		return 0, err
	}
	defer ff.Close()
	n, err = ff.WriteAt(b, off)
	if n > 0 && p.piece != nil && p.piece.cache != nil && p.piece.cache.storage != nil {
		p.piece.cache.storage.AddArchiveUsed(int64(n))
	}
	if p.piece != nil {
		p.piece.Accessed = time.Now().Unix()
	}
	return
}

func (p *ArchivePiece) ReadAt(b []byte, off int64) (n int, err error) {
	if p == nil {
		return 0, io.EOF
	}
	p.mu.RLock()
	defer p.mu.RUnlock()

	ff, err := os.OpenFile(p.name, os.O_RDONLY, 0o644)
	if os.IsNotExist(err) {
		return 0, io.EOF
	}
	if err != nil {
		return 0, err
	}
	defer ff.Close()
	n, err = ff.ReadAt(b, off)
	if p.piece != nil {
		p.piece.Accessed = time.Now().Unix()
	}
	return n, err
}

func (p *ArchivePiece) Release() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	debit := int64(0)
	if p.piece != nil {
		debit = p.piece.Size
	}
	if info, err := os.Stat(p.name); err == nil {
		debit = info.Size()
	}
	if p.piece != nil && p.piece.cache != nil && p.piece.cache.storage != nil && debit > 0 {
		p.piece.cache.storage.SubArchiveUsed(debit)
	}
	os.Remove(p.name)
}
