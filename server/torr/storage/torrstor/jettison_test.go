package torrstor

import (
	"os"
	"path/filepath"
	"testing"

	"server/settings"
)

func TestJettisonArchiveIfOver(t *testing.T) {
	dir := t.TempDir()
	prev := settings.BTsets
	settings.BTsets = &settings.BTSets{UseDisk: true}
	t.Cleanup(func() { settings.BTsets = prev })

	s := NewStorage(1 << 20)
	s.SetArchivePath(dir)
	s.SetArchiveBudget(6 << 10)
	s.InitArchiveRoot()

	// Three caches, each writing 4 KB of archive. Total 12 KB
	// > 6 KB budget; the two oldest should be evicted.
	for i := 0; i < 3; i++ {
		c := NewCache(1<<20, s)
		c.hash = metainfoHashForTest(byte(i + 1))
		c.pieceLength = 4 << 10
		c.pieces = map[int]*Piece{}
		piece := &Piece{
			Id: 0, Size: 4 << 10, Complete: true,
			Accessed: int64(100 + i*100), cache: c,
		}
		piece.aPiece = &ArchivePiece{
			piece: piece,
			name:  filepath.Join(dir, c.hash.HexString(), "0"),
		}
		if err := os.MkdirAll(filepath.Dir(piece.aPiece.name), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(piece.aPiece.name, make([]byte, 4<<10), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		s.AddArchiveUsed(4 << 10)
		c.pieces[0] = piece
		s.caches[c.hash] = c
	}

	if got := s.ArchiveUsed(); got != 12<<10 {
		t.Fatalf("seeded ArchiveUsed=%d, want 12K", got)
	}
	s.JettisonArchiveIfOver()
	if got := s.ArchiveUsed(); got > 6<<10 {
		t.Fatalf("after jettison ArchiveUsed=%d, want <=6K", got)
	}
	if got := s.ArchiveUsed(); got == 0 {
		t.Fatalf("eviction should leave one archive intact, got 0")
	}
}

func TestJettisonArchiveSkipsActiveReaders(t *testing.T) {
	dir := t.TempDir()
	prev := settings.BTsets
	settings.BTsets = &settings.BTSets{UseDisk: true}
	t.Cleanup(func() { settings.BTsets = prev })

	s := NewStorage(1 << 20)
	s.SetArchivePath(dir)
	s.SetArchiveBudget(1 << 10)
	s.InitArchiveRoot()

	c := NewCache(1<<20, s)
	c.hash = metainfoHashForTest(0xab)
	c.pieceLength = 4 << 10
	c.pieces = map[int]*Piece{}
	piece := &Piece{Id: 0, Size: 4 << 10, Complete: true, Accessed: 100, cache: c}
	piece.aPiece = &ArchivePiece{
		piece: piece,
		name:  filepath.Join(dir, c.hash.HexString(), "0"),
	}
	if err := os.MkdirAll(filepath.Dir(piece.aPiece.name), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(piece.aPiece.name, make([]byte, 4<<10), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	s.AddArchiveUsed(4 << 10)
	c.pieces[0] = piece
	s.caches[c.hash] = c

	// Inject a nil Reader. nil is a valid map key, and the
	// count is the only thing the eviction pass consults.
	c.muReaders.Lock()
	c.readers = map[*Reader]struct{}{nil: {}}
	c.muReaders.Unlock()
	if got := c.Readers(); got != 1 {
		t.Fatalf("expected Readers()=1, got %d", got)
	}

	s.JettisonArchiveIfOver()
	if got := s.ArchiveUsed(); got != 4<<10 {
		t.Fatalf("active reader archive was evicted (ArchiveUsed=%d, want 4K)", got)
	}
	if _, err := os.Stat(piece.aPiece.name); err != nil {
		t.Fatalf("active reader archive file removed: %v", err)
	}
}

func TestPinnedCacheNotTrimmed(t *testing.T) {
	s := NewStorage(1 << 20)
	c := NewCache(1<<20, s)
	c.hash[0] = 0x42
	c.pieces = make(map[int]*Piece)
	for i := 0; i < 4; i++ {
		c.pieces[i] = &Piece{
			Id: i, Size: 1 << 20, Complete: true,
			Accessed: int64(100 + i), cache: c,
		}
	}
	c.SetPinned(true)
	got := c.getRemPieces()
	if len(got) != 0 {
		t.Fatalf("pinned getRemPieces returned %d pieces, want 0", len(got))
	}
}

func metainfoHashForTest(b byte) metainfoHash {
	var h metainfoHash
	for i := range h {
		h[i] = b
	}
	return h
}

type metainfoHash = [20]byte
