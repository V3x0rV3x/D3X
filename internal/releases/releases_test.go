package releases

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/V3x0rV3x/D3X/internal/maketorrent"
)

// Dossier rangé par une médiathèque (Hypsis, 27/10/26) : le nom proposé est
// celui de sa vidéo quand le dossier n'a pas l'air d'un nom de release.
func TestNameFromVideo(t *testing.T) {
	plan := func(name string, single bool, files ...maketorrent.Entry) *maketorrent.Plan {
		p := &maketorrent.Plan{Name: name, Single: single, Files: files}
		for _, f := range files {
			p.Total += f.Size
		}
		return p
	}
	movie := maketorrent.Entry{Rel: "1917.2019.MULTi.1080p.BluRay.x264-GROUPE.mkv", Size: 8 << 30}
	srt := maketorrent.Entry{Rel: "1917.2019.MULTi.1080p.BluRay.x264-GROUPE.fr.srt", Size: 80 << 10}
	for _, c := range []struct {
		name string
		p    *maketorrent.Plan
		main int
		want string
	}{
		{"dossier de médiathèque", plan("1917 (2019)", false, srt, movie), 1, "1917.2019.MULTi.1080p.BluRay.x264-GROUPE"},
		{"dossier de release", plan("1917.2019.MULTi.1080p.BluRay.x264-GROUPE", false, movie), 0, ""},
		{"fichier seul", plan(movie.Rel, true, movie), 0, ""},
		{"vidéo sans nom de release", plan("1917 (2019)", false, maketorrent.Entry{Rel: "1917 (2019).mkv", Size: 8 << 30}), 0, ""},
		{"saison", plan("Série (2020)", false,
			maketorrent.Entry{Rel: "Saison 1/Serie.S01E01.1080p.WEB.H264-GRP.mkv", Size: 2 << 30},
			maketorrent.Entry{Rel: "Saison 1/Serie.S01E02.1080p.WEB.H264-GRP.mkv", Size: 1 << 30},
			maketorrent.Entry{Rel: "Saison 1/Serie.S01E03.1080p.WEB.H264-GRP.mkv", Size: 1 << 30}), 0, ""},
		{"plan absent", nil, 0, ""},
	} {
		if got := NameFromVideo(c.p, c.main); got != c.want {
			t.Errorf("%s : %q, attendu %q", c.name, got, c.want)
		}
	}
}

// Lot lancé sur un disque rangé par une médiathèque (Ekhstaz, 04/11/26) : le
// nom de la vidéo quand le dossier n'en est pas un, et une raison quand ni
// l'un ni l'autre ne sont des noms de release. Ni un album, ni un livre, ni
// une collection ne sont concernés.
func TestEntryReleaseName(t *testing.T) {
	dir := t.TempDir()
	mk := func(rel string, size int) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("21 Grams (2003) [ImdbID-tt0315733]/21 Grams (2003).mkv", 200000)
	mk("3 from Hell (2019) {tmdb-489064}/3 from Hell (2019) {tmdb-489064}.mkv", 200000)
	mk("1917 (2019)/1917.2019.MULTi.1080p.BluRay.x264-GROUPE.mkv", 200000)
	mk("1917 (2019)/1917.2019.MULTi.1080p.BluRay.x264-GROUPE.fr.srt", 1000)
	mk("Film.2020.MULTi.1080p.WEB.H264-GRP/Film.2020.MULTi.1080p.WEB.H264-GRP.mkv", 200000)
	mk("300 (2007).mkv", 200000)
	mk("Film.2021.FRENCH.720p.WEB.H264-GRP.mkv", 200000)
	mk("Artiste - Album (2020) [FLAC]/01.flac", 40000)
	mk("Mon livre (2019)/livre.epub", 4000)
	mk("Films/Film (2001)/Film (2001).mkv", 200000)
	list, err := List(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][2]string{
		"21 Grams (2003) [ImdbID-tt0315733]":     {"", BadNameDir},
		"3 from Hell (2019) {tmdb-489064}":       {"", BadNameDir},
		"1917 (2019)":                            {"1917.2019.MULTi.1080p.BluRay.x264-GROUPE", ""},
		"Film.2020.MULTi.1080p.WEB.H264-GRP":     {"", ""},
		"300 (2007).mkv":                         {"", BadNameFile},
		"Film.2021.FRENCH.720p.WEB.H264-GRP.mkv": {"", ""},
		"Artiste - Album (2020) [FLAC]":          {"", ""},
		"Mon livre (2019)":                       {"", ""},
		"Films":                                  {"", ""},
	}
	for _, e := range list {
		w, ok := want[e.Name]
		if !ok {
			t.Errorf("release inattendue : %s", e.Name)
			continue
		}
		if e.ReleaseName != w[0] || e.BadName != w[1] {
			t.Errorf("%s : nom %q, raison %q ; attendu %q, %q", e.Name, e.ReleaseName, e.BadName, w[0], w[1])
		}
		delete(want, e.Name)
	}
	if len(want) > 0 {
		t.Errorf("releases absentes de la liste : %v", want)
	}
}

// Vidéo ou album d'après le poids (Indrop, 28/10/26) : une discographie avec
// un clip reste un album, un film avec sa bande originale reste un film.
func TestEntryKind(t *testing.T) {
	dir := t.TempDir()
	mk := func(rel string, size int) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("Discographie/01.flac", 40000)
	mk("Discographie/02.flac", 50000)
	mk("Discographie/Clip.mp4", 20000)
	mk("Film.2020.1080p/Film.2020.1080p.mkv", 200000)
	mk("Film.2020.1080p/BO.flac", 30000)
	mk("Album/01.mp3", 10000)
	list, err := List(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][2]bool{"Discographie": {false, true}, "Film.2020.1080p": {true, false}, "Album": {false, true}}
	for _, e := range list {
		w, ok := want[e.Name]
		if !ok {
			continue
		}
		if e.Video != w[0] || e.Audio != w[1] {
			t.Errorf("%s : vidéo %v album %v, attendu %v %v", e.Name, e.Video, e.Audio, w[0], w[1])
		}
		delete(want, e.Name)
	}
	if len(want) > 0 {
		t.Errorf("releases absentes de la liste : %v", want)
	}
}
