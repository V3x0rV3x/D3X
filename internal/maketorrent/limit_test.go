package maketorrent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"
)

// Une vitesse de lecture bornée ne change rien au .torrent, ralentit bien la
// lecture, et un hachage annulé n'attend pas la fin de la limite.
func TestReadLimit(t *testing.T) {
	setEnv(t)
	defer SetReadLimit(0)
	dir := filepath.Join(t.TempDir(), "Limite.2024.1080p-D3X")
	write(t, filepath.Join(dir, "Limite.mkv"), 5_000_000, 7)
	write(t, filepath.Join(dir, "fr.srt"), 1_234, 8)
	p, err := Scan(dir, DefaultExclude)
	if err != nil {
		t.Fatal(err)
	}
	p.PieceLength = 4 << 20 // pièces plus grandes qu'une lecture bornée
	ctx := context.Background()

	SetReadLimit(0)
	free, err := p.hashPieces(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	SetReadLimit(20) // 20 Mo/s : 5 Mo en 0,25 s environ
	start := time.Now()
	slow, err := p.hashPieces(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(free, slow) {
		t.Fatal("pièces différentes avec la limite")
	}
	if el := time.Since(start); el < 180*time.Millisecond {
		t.Fatalf("5 Mo lus en %v à 20 Mo/s", el)
	}

	data := bytes.Repeat([]byte("D3X"), 1_000_000)
	start = time.Now()
	got, err := io.ReadAll(Paced(ctx, bytes.NewReader(data)))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("lecture bornée : %d octets, %v", len(got), err)
	}
	if el := time.Since(start); el < 100*time.Millisecond {
		t.Fatalf("3 Mo lus en %v à 20 Mo/s", el)
	}

	SetReadLimit(1)
	cctx, cancel := context.WithCancel(ctx)
	time.AfterFunc(50*time.Millisecond, cancel)
	start = time.Now()
	if _, err := p.hashPieces(cctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("hachage annulé : %v", err)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("annulation prise en compte au bout de %v", el)
	}
}
