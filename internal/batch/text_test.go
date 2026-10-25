package batch

import (
	"testing"

	"github.com/V3x0rV3x/D3X/internal/v3x"
)

func TestCertainWork(t *testing.T) {
	movie := Work{Type: "movie", ID: "1", Title: "Démonstration", OriginalTitle: "Demonstration", Year: "2024"}
	tv := Work{Type: "tv", ID: "2", Title: "Démonstration, la série", Year: "2023"}
	if w := certainWork([]Work{movie, tv}, "Demonstration", "2024", false); w == nil || w.ID != "1" {
		t.Fatalf("film : %+v", w)
	}
	if w := certainWork([]Work{movie, tv}, "Demonstration", "2024", true); w != nil {
		t.Fatalf("série attendue, pas de film : %+v", w)
	}
	if w := certainWork([]Work{movie}, "Demonstration", "2020", false); w != nil {
		t.Fatalf("année trop éloignée : %+v", w)
	}
	if w := certainWork([]Work{movie}, "Autre Chose", "2024", false); w != nil {
		t.Fatalf("titre différent : %+v", w)
	}
	// Deux candidats plausibles : incertain.
	if w := certainWork([]Work{movie, {Type: "movie", ID: "3", Title: "Demonstration", Year: "2025"}}, "Demonstration", "2024", false); w != nil {
		t.Fatalf("deux candidats : %+v", w)
	}
}

func TestNameWithToken(t *testing.T) {
	for _, c := range []struct{ name, token, want string }{
		{"Artiste - Album [FLAC]", "FLAC 24bit-96kHz", "Artiste - Album [FLAC 24bit-96kHz]"},
		{"Artiste - Album", "FLAC 24bit-96kHz", "Artiste - Album [FLAC 24bit-96kHz]"},
		{"Artiste.Album.2020", "MP3 320", "Artiste.Album.2020.MP3.320"},
		{"Artiste-Album-2020-FLAC", "FLAC 16bit-44.1kHz", "Artiste-Album-2020-FLAC-16bit-44.1kHz"},
		{"Artiste - Album", "", "Artiste - Album"},
	} {
		if got := nameWithToken(c.name, c.token); got != c.want {
			t.Errorf("nameWithToken(%q, %q) = %q, attendu %q", c.name, c.token, got, c.want)
		}
	}
}

func TestNfo(t *testing.T) {
	if got := decodeNfo([]byte("plain \xc3\xa9t\xc3\xa9")); got != "plain été" {
		t.Errorf("utf-8 : %q", got)
	}
	if got := decodeNfo([]byte{0xC9, 0xCD, 0xBB, 'a'}); got != "╔═╗a" {
		t.Errorf("cp437 : %q", got)
	}
	if !hasMediaInfo("General\nComplete name : x") || hasMediaInfo("rien") {
		t.Error("hasMediaInfo")
	}
	if got := nfoWithMediaInfo("NFO", "General\n..."); got != "NFO\n\nGeneral\n..." {
		t.Errorf("ajout : %q", got)
	}
	if got := nfoWithMediaInfo("General\nComplete name : x", "autre"); got != "General\nComplete name : x" {
		t.Errorf("déjà présent : %q", got)
	}
	if got := nfoWithMediaInfo("", "mi"); got != "" {
		t.Errorf("vide : %q", got)
	}
}

func TestStripExt(t *testing.T) {
	if got := stripExt("Film.2020.mkv", true); got != "Film.2020" {
		t.Errorf("%q", got)
	}
	if got := stripExt("Dossier.mkv", false); got != "Dossier.mkv" {
		t.Errorf("%q", got)
	}
}

func TestTexts(t *testing.T) {
	if got := dupText(&v3x.Match{Kind: "exact", Name: "X"}); got != "déjà sur V3X : X · « Revoir » pour la seeder avec ces fichiers" {
		t.Errorf("%q", got)
	}
	if got := dupText(&v3x.Match{Kind: "renamed", Name: "X"}); got != "déjà sur V3X : X · la Médiathèque la met en seed avec ces fichiers" {
		t.Errorf("%q", got)
	}
	if got := goneText(&v3x.Gone{Status: "trumped", Own: true, Reason: "mieux"}); got != "une fiche V3X de ce contenu a été remplacée par une meilleure version (ta fiche, motif : mieux)" {
		t.Errorf("%q", got)
	}
	if got := pubErr("validation", `{"field":"title","detail":"vide"}`); got != "un champ de la fiche est refusé : title (vide)" {
		t.Errorf("%q", got)
	}
	if got := pubErr("duplicate", `{"existing":{"id":"abc"}}`); got != "cette release est déjà sur V3X (même .torrent). Fiche existante : https://v3x.club/torrents/abc" {
		t.Errorf("%q", got)
	}
	if got := pubErr("upscale_detected", ""); got != "les releases agrandies par IA ne sont pas acceptées telles quelles" {
		t.Errorf("%q", got)
	}
	if got := pubErr("inconnu", "pas du json"); got != "inconnu" {
		t.Errorf("%q", got)
	}
	if !blocking["mediainfo_missing"] || blocking["tmdb_missing"] {
		t.Error("blocking")
	}
}

func TestPickNfo(t *testing.T) {
	for _, c := range []struct {
		nom   string
		files []File
		want  int
	}{
		{"nommé, pas en premier", []File{{Rel: "D/autre.nfo", Size: 10}, {Rel: "D/Film.nfo", Size: 100}}, 1},
		{"seul un plus profond", []File{{Rel: "D/a.mkv", Size: 9}, {Rel: "D/Sub/ep.nfo", Size: 50}}, 1},
		{"le moins profond d'abord", []File{{Rel: "D/Sub/Film.nfo", Size: 50}, {Rel: "D/autre.nfo", Size: 10}}, 1},
		{"trop gros", []File{{Rel: "D/Film.nfo", Size: 262145}}, -1},
		{"limite 256 Kio", []File{{Rel: "D/Film.nfo", Size: 262144}}, 0},
		{"taille nulle", []File{{Rel: "D/Film.nfo", Size: 0}}, -1},
		{"aucun nfo", []File{{Rel: "D/a.mkv", Size: 9}}, -1},
		{"liste vide", nil, -1},
	} {
		if got := pickNfo("Film", c.files); got != c.want {
			t.Errorf("%s : %d, attendu %d", c.nom, got, c.want)
		}
	}
}

func TestSimilarity(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want float64
	}{
		{"Toy Story 3", "Toy Story 4", 1}, {"Rocky 2", "Rocky", 1}, {"Россия", "Россия", 0},
		{"L'Été", "Lete", 1}, {"Ve\u0301rite\u0301 cache\u0301e", "Vérité cachée", 1}, {"", "x", 0},
	} {
		if got := similarity(c.a, c.b); got != c.want {
			t.Errorf("similarity(%q, %q) = %v, attendu %v", c.a, c.b, got, c.want)
		}
	}
}

func TestDecodeNfoDetails(t *testing.T) {
	if got := decodeNfo([]byte{0xFF}); got != "\u00a0" {
		t.Errorf("cp437 0xFF : %q", got)
	}
	if got := decodeNfo([]byte("\xEF\xBB\xBFNFO")); got != "NFO" {
		t.Errorf("BOM : %q", got)
	}
	if got := nameWithToken("FLAC-Artiste-Album", "FLAC 16bit-44.1kHz"); got != "FLAC-16bit-44.1kHz-Artiste-Album" {
		t.Errorf("codec au début : %q", got)
	}
	if got := pubErr("validation", `{"field":3,"existing":{"id":42}}`); got != "un champ de la fiche est refusé : 3. Fiche existante : https://v3x.club/torrents/42" {
		t.Errorf("nombres : %q", got)
	}
}

func TestTextUnmarshal(t *testing.T) {
	for in, want := range map[string]Text{`"x"`: "x", `42`: "42", `1.5`: "1.5", `null`: "", `true`: "", `{}`: "", `[]`: ""} {
		var got Text
		if err := got.UnmarshalJSON([]byte(in)); err != nil || got != want {
			t.Errorf("%s : %q (%v), attendu %q", in, got, err, want)
		}
	}
}

func TestBlockingEnAvertissements(t *testing.T) {
	if len(blocking) != len(warnings) {
		t.Fatalf("%d bloquants pour %d avertissements", len(blocking), len(warnings))
	}
	for k := range blocking {
		if _, ok := warnings[k]; !ok {
			t.Errorf("%q bloquant sans texte", k)
		}
	}
}

func TestCertainWorkSansAnnee(t *testing.T) {
	w := []Work{{Type: "movie", ID: "1", Title: "Demonstration"}}
	if r := certainWork(w, "Demonstration", "", false); r == nil || r.ID != "1" {
		t.Errorf("sans année : %+v", r)
	}
	w[0].Year = "2024"
	if r := certainWork(w, "Demonstration", "", false); r == nil {
		t.Errorf("année du titre absente : %+v", r)
	}
}

func TestGoneTextVariantes(t *testing.T) {
	for _, c := range []struct {
		g    v3x.Gone
		want string
	}{
		{v3x.Gone{Status: "deleted"}, "une fiche V3X de ce contenu a été retirée"},
		{v3x.Gone{Status: "archived"}, "une fiche V3X de ce contenu a été archivée"},
		{v3x.Gone{Status: "trumped", Reason: "mieux"}, "une fiche V3X de ce contenu a été remplacée par une meilleure version"},
		{v3x.Gone{Status: "archived", Own: true}, "une fiche V3X de ce contenu a été archivée"},
	} {
		if got := goneText(&c.g); got != c.want {
			t.Errorf("%+v : %q", c.g, got)
		}
	}
}
