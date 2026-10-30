package pathmap

import (
	"os"
	"path/filepath"
	"testing"

	unorm "golang.org/x/text/unicode/norm"
)

func TestToClient(t *testing.T) {
	rules := []Rule{{From: `\\NAS\data`, To: "/data"}, {From: `\\NAS\data\films`, To: "/mnt/films"}, {From: "D:/Torrents", To: "/downloads"}}
	cases := map[string]string{
		`\\NAS\data\series\X`:    "/data/series/X",
		`\\NAS\data\films\Y.mkv`: "/mnt/films/Y.mkv", // préfixe le plus long
		`\\nas\DATA\a`:           "/data/a",          // côté Windows sans la casse
		`d:\torrents\Z`:          "/downloads/Z",
		`\\NAS\database\a`:       `\\NAS\database\a`, // pas à une frontière de dossier
		`E:\Autre`:               `E:\Autre`,
	}
	for in, want := range cases {
		if got := ToClient(in, rules); got != want {
			t.Errorf("ToClient(%q) = %q, attendu %q", in, got, want)
		}
	}
	if got := FromClient("/data/series/X", rules); got != `\\NAS\data\series\X` {
		t.Errorf("FromClient : %q", got)
	}
	if got := ToClient("/srv/x", []Rule{{From: "/srv", To: `D:\srv`}}); got != `D:\srv\x` {
		t.Errorf("vers Windows : %q", got)
	}
}

// Sur un Mac, la partie du chemin lue sur le disque (accents décomposés) part
// au client recomposée ; le chemin sans règle reste tel quel.
func TestToClientAccentsMac(t *testing.T) {
	old := clientName
	clientName = unorm.NFC.String
	defer func() { clientName = old }()
	nfd := "/Volumes/D/Vide\u0301os/Ame\u0301lie"
	rules := []Rule{{From: "/Volumes/D", To: `D:\`}}
	if got := ToClient(nfd, rules); got != "D:\\Vid\u00e9os\\Am\u00e9lie" {
		t.Errorf("ToClient = %q", got)
	}
	if got := ToClient(nfd, nil); got != nfd {
		t.Errorf("sans règle : %q", got)
	}
}

func TestSuggest(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"Film.A", "Film.B", "Serie.C"} {
		os.MkdirAll(filepath.Join(dir, n), 0o755)
	}
	client := []string{"/data/torrents/Film.A", "/data/torrents/Film.B", "/autre/Serie.C", "/data/torrents/Absent"}
	r, n := Suggest(dir, client)
	if r.To != "/data/torrents" || r.From != dir || n != 2 {
		t.Fatalf("suggestion : %+v (%d)", r, n)
	}
	// Même chemin des deux côtés : pas de règle.
	same := []string{filepath.Join(dir, "Film.A")}
	if r, n := Suggest(dir, same); r.To != "" || n != 1 {
		t.Fatalf("même machine : %+v (%d)", r, n)
	}
}

func TestCovers(t *testing.T) {
	rules := []Rule{{From: `\\NAS\data`, To: "/data"}, {From: "/home/moi", To: ""}}
	for p, want := range map[string]bool{
		`\\NAS\data\films`: true,
		`\\nas\DATA`:       true,
		`\\NAS\database`:   false, // pas à une frontière de dossier
		"/home/moi/x":      false, // règle à moitié remplie
		`D:\Films`:         false,
	} {
		if got := Covers(p, rules); got != want {
			t.Errorf("Covers(%q) = %v, attendu %v", p, got, want)
		}
	}
}
