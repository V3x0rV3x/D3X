package maketorrent

import (
	"bytes"
	"context"
	"crypto/sha1"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/V3x0rV3x/D3X/internal/bencode"
	"github.com/V3x0rV3x/D3X/internal/torrent"
)

func setEnv(t *testing.T) string {
	tmp := t.TempDir()
	t.Setenv("AppData", tmp)
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)
	return tmp
}

func write(t *testing.T, path string, n int, seed int64) []byte {
	t.Helper()
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestScanHashAndCache(t *testing.T) {
	setEnv(t)
	dir := filepath.Join(t.TempDir(), "Ma.Release.2024.1080p-D3X")
	var all []byte
	// Ordre attendu dans le torrent : par segments de chemin.
	all = append(all, write(t, filepath.Join(dir, "Ma.Release.mkv"), 3_000_123, 1)...)
	all = append(all, write(t, filepath.Join(dir, "Sous-titres", "fr.srt"), 4_321, 2)...)
	all = append(all, write(t, filepath.Join(dir, "Sous-titres", "zz vide.txt"), 0, 3)...)
	write(t, filepath.Join(dir, ".DS_Store"), 100, 4)
	write(t, filepath.Join(dir, "extrafanart", "a.jpg"), 100, 5)
	write(t, filepath.Join(dir, "Ma.Release.trickplay", "x.jpg"), 100, 6)

	p, err := Scan(dir, DefaultExclude)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Files) != 3 || p.Files[0].Rel != "Ma.Release.mkv" || p.Files[1].Rel != "Sous-titres/fr.srt" {
		t.Fatalf("fichiers : %+v", p.Files)
	}
	if len(p.Skipped) != 3 {
		t.Fatalf("écartés : %v", p.Skipped)
	}
	p.PieceLength = 64 << 10 // plusieurs pièces, dont une à cheval sur deux fichiers
	raw, cached, err := p.Hash(context.Background(), "D3X test", nil)
	if err != nil || cached {
		t.Fatalf("Hash : %v (cache %v)", err, cached)
	}
	parsed, err := torrent.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.ContentHash != p.ContentHash || parsed.SizeHash != p.SizeHash {
		t.Fatal("les empreintes calculées avant hachage doivent être celles du .torrent")
	}
	d, _ := bencode.Decode(raw)
	info := d.Root.(bencode.Dict)["info"].(bencode.Dict)
	var want []byte
	for i := 0; i < len(all); i += int(p.PieceLength) {
		end := i + int(p.PieceLength)
		if end > len(all) {
			end = len(all)
		}
		s := sha1.Sum(all[i:end])
		want = append(want, s[:]...)
	}
	if !bytes.Equal(info["pieces"].([]byte), want) {
		t.Fatal("pièces différentes d'un calcul simple, octet par octet")
	}
	if string(info["source"].([]byte)) != "V3X" || info["private"].(int64) != 1 {
		t.Fatal("source V3X et drapeau privé attendus")
	}

	// Deuxième fois : repris du cache, identique.
	raw2, cached, err := p.Hash(context.Background(), "D3X test", nil)
	if err != nil || !cached || !bytes.Equal(raw, raw2) {
		t.Fatalf("cache : %v %v", err, cached)
	}

	// Un fichier modifié (même taille, autre date) : plus de cache.
	write(t, filepath.Join(dir, "Sous-titres", "fr.srt"), 4_321, 99)
	p2, _ := Scan(dir, DefaultExclude)
	p2.PieceLength = p.PieceLength
	if _, cached, _ := p2.Hash(context.Background(), "D3X test", nil); cached {
		t.Fatal("un fichier modifié ne doit pas resservir l'ancien .torrent")
	}
}

func TestSingleFile(t *testing.T) {
	setEnv(t)
	f := filepath.Join(t.TempDir(), "Film.2020.mkv")
	write(t, f, 1_000_000, 7)
	p, err := Scan(f, nil)
	if err != nil || !p.Single || p.Name != "Film.2020.mkv" {
		t.Fatalf("%+v %v", p, err)
	}
	raw, _, err := p.Hash(context.Background(), "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := torrent.Parse(raw)
	if parsed.ContentHash != p.ContentHash || parsed.Total != 1_000_000 {
		t.Fatal("mono-fichier : empreinte ou taille fausse")
	}
}

// Noms à accents décomposés (NFD, comme macOS les rend) : recomposés dans le
// .torrent sur un Mac seulement, gardés octet pour octet ailleurs ; le
// hachage lit toujours le fichier sous son nom du disque.
func TestAccentNames(t *testing.T) {
	setEnv(t)
	nfdDir, nfdFile := "Amélie.2001", "Crédits.mkv"
	dir := filepath.Join(t.TempDir(), nfdDir)
	write(t, filepath.Join(dir, nfdFile), 100_000, 9)
	p, err := Scan(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	wantDir, wantFile := nfdDir, nfdFile
	if runtime.GOOS == "darwin" {
		wantDir, wantFile = "Amélie.2001", "Crédits.mkv"
	}
	if p.Name != wantDir || len(p.Files) != 1 || p.Files[0].Rel != wantFile {
		t.Fatalf("noms : %q %+v", p.Name, p.Files)
	}
	raw, _, err := p.Hash(context.Background(), "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := torrent.Parse(raw)
	if parsed.Name != wantDir || parsed.ContentHash != p.ContentHash {
		t.Fatalf(".torrent : %q", parsed.Name)
	}
}

func TestSymlinkLoop(t *testing.T) {
	setEnv(t)
	dir := filepath.Join(t.TempDir(), "R")
	write(t, filepath.Join(dir, "a.mkv"), 1000, 8)
	if err := os.Symlink(dir, filepath.Join(dir, "boucle")); err != nil {
		t.Skip("liens symboliques indisponibles ici : ", err)
	}
	_ = os.Symlink(filepath.Join(dir, "absent"), filepath.Join(dir, "casse"))
	p, err := Scan(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Files) != 1 {
		t.Fatalf("la boucle ne doit pas être suivie : %+v", p.Files)
	}
}

func TestAutoPieceLength(t *testing.T) {
	for _, c := range []struct{ total, want int64 }{
		{700 << 20, 512 << 10},
		{4 << 30, 4 << 20},
		{60 << 30, 16 << 20},
		{10 << 10, 32 << 10},
	} {
		if got := AutoPieceLength(c.total); got != c.want {
			t.Errorf("AutoPieceLength(%d) = %d, attendu %d", c.total, got, c.want)
		}
	}
}

func TestChangedFile(t *testing.T) {
	setEnv(t)
	dir := filepath.Join(t.TempDir(), "R")
	write(t, filepath.Join(dir, "a.mkv"), 200_000, 9)
	p, _ := Scan(dir, nil)
	write(t, filepath.Join(dir, "a.mkv"), 100_000, 9) // raccourci après la liste
	if _, _, err := p.Hash(context.Background(), "t", nil); err != ErrChanged {
		t.Fatalf("fichier raccourci : %v", err)
	}
}
