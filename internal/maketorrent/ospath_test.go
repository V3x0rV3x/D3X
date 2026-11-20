package maketorrent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/V3x0rV3x/D3X/internal/torrent"
)

func TestOSPath(t *testing.T) {
	for _, c := range []struct{ goos, in, want string }{
		{"windows", `D:\Packs\Marvel`, `D:\Packs\Marvel`},
		{"windows", `D:\Packs\Agents of S.H.I.E.L.D.\S01E01.mkv`, `\\?\D:\Packs\Agents of S.H.I.E.L.D.\S01E01.mkv`},
		{"windows", `D:\Packs\Vol 2 `, `\\?\D:\Packs\Vol 2 `},
		{"windows", `D:/Packs/Vol. 1./a.mkv`, `\\?\D:\Packs\Vol. 1.\a.mkv`},
		{"windows", `\\nas\video\Vol. 1.`, `\\?\UNC\nas\video\Vol. 1.`},
		{"windows", `\\?\D:\Vol. 1.`, `\\?\D:\Vol. 1.`},
		{"windows", `D:\Packs\..\Vol. 1.`, `D:\Packs\..\Vol. 1.`}, // pas nettoyé : laissé tel quel
		{"windows", `D:\Packs\.\a.mkv`, `D:\Packs\.\a.mkv`},
		{"linux", "/data/Vol. 1./a.mkv", "/data/Vol. 1./a.mkv"},
		{"darwin", "/Volumes/nas/Vol 2 ", "/Volumes/nas/Vol 2 "},
	} {
		if got := osPath(c.goos, c.in); got != c.want {
			t.Errorf("osPath(%s, %q) = %q, attendu %q", c.goos, c.in, got, c.want)
		}
	}
}

// extended : le chemin pour créer les noms de test sous Windows, qui refuse
// sinon un nom finissant par un point ou une espace.
func extended(p string) string {
	if runtime.GOOS == "windows" {
		return `\\?\` + p
	}
	return p
}

// Un pack dont des dossiers et des fichiers finissent par un point ou une
// espace, comme un NAS sous Linux en crée (Turias, 18/11/26 : 2,3 Tio, 274 Gio
// trouvés) : sous Windows, ils étaient écartés comme « illisibles » et le
// .torrent partait sans eux.
func TestTrailingDotAndSpaceNames(t *testing.T) {
	setEnv(t)
	pack := filepath.Join(t.TempDir(), "Pack.Integrale")
	t.Cleanup(func() { _ = os.RemoveAll(extended(pack)) })
	mkdir := func(p string) {
		t.Helper()
		if err := os.Mkdir(extended(p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	put := func(p string, n int) {
		t.Helper()
		if err := os.WriteFile(extended(p), make([]byte, n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mkdir(pack)
	for _, d := range []string{"Agents of S.H.I.E.L.D.", "Vol 2 ", "Normal"} {
		mkdir(filepath.Join(pack, d))
		put(filepath.Join(pack, d, "a.mkv"), 70_000)
	}
	put(filepath.Join(pack, "Lisez-moi. "), 1_000)

	p, err := Scan(pack, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Skipped) != 0 || len(p.Files) != 4 || p.Total != 3*70_000+1_000 {
		t.Fatalf("%d fichiers, %d octets, écartés %q", len(p.Files), p.Total, p.Skipped)
	}
	raw, _, err := p.Hash(context.Background(), "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := torrent.Parse(raw)
	if err != nil || parsed.Total != p.Total {
		t.Fatalf(".torrent : %v", err)
	}
	want := map[string]bool{
		"Pack.Integrale/Agents of S.H.I.E.L.D./a.mkv": true,
		"Pack.Integrale/Vol 2 /a.mkv":                 true,
		"Pack.Integrale/Normal/a.mkv":                 true,
		"Pack.Integrale/Lisez-moi. ":                  true,
	}
	for _, f := range parsed.Files {
		delete(want, f.Path)
	}
	if len(want) != 0 {
		t.Fatalf("absents du .torrent : %v (fichiers : %+v)", want, parsed.Files)
	}

	// La release choisie est elle-même un dossier qui finit par un point.
	one, err := Scan(filepath.Join(pack, "Agents of S.H.I.E.L.D."), nil)
	if err != nil {
		t.Fatal(err)
	}
	if one.Name != "Agents of S.H.I.E.L.D." || len(one.Files) != 1 {
		t.Fatalf("dossier choisi : %q %+v", one.Name, one.Files)
	}
}
