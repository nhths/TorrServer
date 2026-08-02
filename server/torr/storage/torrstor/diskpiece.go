package torrstor

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"server/log"
	"server/settings"
)

type DiskPiece struct {
	piece *Piece

	name string

	mu sync.RWMutex
}

func NewDiskPiece(p *Piece) *DiskPiece {
	name := filepath.Join(settings.BTsets.TorrentsSavePath, p.cache.hash.HexString(), strconv.Itoa(p.Id))
	ff, err := os.Stat(name)
	if err == nil {
		p.Size = ff.Size()
		p.Complete = ff.Size() == p.cache.pieceLength
		p.Accessed = ff.ModTime().Unix()
		// The piece was on disk before this process started;
		// account for it now so the storage counter is correct
		// from the first tick of the eviction loop. Drift is
		// reconciled by RefreshDiskUsedIfStale.
		if p.cache.storage != nil {
			p.cache.storage.AddDiskUsed(ff.Size())
		}
	}
	return &DiskPiece{piece: p, name: name}
}

func (p *DiskPiece) WriteAt(b []byte, off int64) (n int, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	ff, err := os.OpenFile(p.name, os.O_RDWR|os.O_CREATE, 0o666)
	if err != nil {
		log.TLogln("Error open file:", err)
		return 0, err
	}
	defer ff.Close()
	n, err = ff.WriteAt(b, off)

	// Bump the storage counter by the bytes actually written.
	if n > 0 && p.piece.cache.storage != nil {
		p.piece.cache.storage.AddDiskUsed(int64(n))
	}

	p.piece.Size += int64(n)
	if p.piece.Size > p.piece.cache.pieceLength {
		p.piece.Size = p.piece.cache.pieceLength
	}
	p.piece.Accessed = time.Now().Unix()
	return
}

func (p *DiskPiece) ReadAt(b []byte, off int64) (n int, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	ff, err := os.OpenFile(p.name, os.O_RDONLY, 0o666)
	if os.IsNotExist(err) {
		return 0, io.EOF
	}
	if err != nil {
		log.TLogln("Error open file:", err)
		return 0, err
	}
	defer ff.Close()

	n, err = ff.ReadAt(b, off)

	p.piece.Accessed = time.Now().Unix()
	if int64(len(b))+off >= p.piece.Size {
		go p.piece.cache.cleanPieces()
	}
	return n, err
}

func (p *DiskPiece) Release() {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Stat the file before removing it so the storage counter
	// is debited by the actual on-disk size. Fall back to the
	// last-known Piece.Size if the stat fails.
	debit := p.piece.Size
	if info, err := os.Stat(p.name); err == nil {
		debit = info.Size()
	}
	if p.piece.cache.storage != nil && debit > 0 {
		p.piece.cache.storage.SubDiskUsed(debit)
	}

	p.piece.Size = 0
	p.piece.Complete = false

	os.Remove(p.name)
}
