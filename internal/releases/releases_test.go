package releases

import (
	"os"
	"path/filepath"
	"testing"
)

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
