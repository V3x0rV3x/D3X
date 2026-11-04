package server

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/V3x0rV3x/D3X/internal/config"
	"github.com/V3x0rV3x/D3X/internal/pathmap"
	"github.com/V3x0rV3x/D3X/internal/qbit"
)

// La liste du Lot dit les étiquettes des torrents qui couvrent chaque release
// et celles écartées par le réglage « Étiquettes à ne jamais publier » ; le
// réglage se lit et s'écrit par /ui/settings et /ui/state (foux, 04/11/26).
func TestLotSkipTags(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("AppData", tmp)
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)
	parent := filepath.Join(tmp, "Releases")
	for _, n := range []string{"Film.NoHL.2020", "Film.Libre.2021", "Serie.S01"} {
		if err := os.MkdirAll(filepath.Join(parent, n), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(parent, n, n+".mkv"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Le client voit /data là où D3X voit parent.
	qs, _ := fakeQbitServer(t, "demo", []qbit.Torrent{
		{Hash: "a", Name: "Film.NoHL.2020", ContentPath: "/data/Film.NoHL.2020", Progress: 1, Tags: "noHL, V3X"},
		{Hash: "b", Name: "Film.NoHL.2020", ContentPath: "/data/Film.NoHL.2020", Progress: 1, Tags: "cross-seed"}, // mêmes fichiers sans l'étiquette : n'y change rien
		{Hash: "c", Name: "Film.Libre.2021", ContentPath: "/data/Film.Libre.2021", Progress: 1, Tags: "V3X"},
		{Hash: "d", Name: "Serie.S01E01.mkv", ContentPath: "/data/Serie.S01/Serie.S01E01.mkv", Progress: 1, Tags: "LASTSEED"},
	})
	cfg := config.Defaults()
	cfg.Clients = []config.ClientConfig{{ID: "c1", URL: qs.URL, User: "admin", Pass: "demo", PathMap: []pathmap.Rule{{From: parent, To: "/data"}}}}
	// Un second client injoignable : signalé, sans cacher les étiquettes du premier.
	cfg.Clients = append(cfg.Clients, config.ClientConfig{ID: "c2", Name: "Seedbox", URL: "http://127.0.0.1:1", User: "x", Pass: "y"})
	s := NewWith("test", cfg, "jeton-de-test-0123456789abcdef", 8733, Options{})
	s.connect()

	// Le réglage, saisi « a, b », rendu tel quel par l'état.
	if code, st, body := call(t, s, "POST", "/ui/settings", map[string]interface{}{"skipTags": " nohl, ,LastSeed "}); code != http.StatusOK || fmt.Sprint(st["skipTags"]) != "[nohl LastSeed]" {
		t.Fatalf("réglage : %d %s", code, body)
	}
	if _, st, _ := call(t, s, "GET", "/ui/state", nil); fmt.Sprint(st["skipTags"]) != "[nohl LastSeed]" {
		t.Fatalf("état : %v", st["skipTags"])
	}

	code, out, body := call(t, s, "GET", "/ui/batch/list?source=local&path="+url.QueryEscape(parent), nil)
	if code != http.StatusOK || out["tagged"] != true || fmt.Sprint(out["skipTags"]) != "[nohl LastSeed]" {
		t.Fatalf("%d %s", code, body)
	}
	if warn, _ := out["tagsWarn"].(string); warn == "" || !strings.Contains(warn, "Étiquettes non lues") || !strings.Contains(warn, "Seedbox") {
		t.Fatalf("client injoignable non signalé : %q", out["tagsWarn"])
	}
	got := map[string]map[string]interface{}{}
	for _, x := range out["entries"].([]interface{}) {
		e := x.(map[string]interface{})
		got[filepath.Base(e["path"].(string))] = e
	}
	if e := got["Film.NoHL.2020"]; e["skipped"] != true || fmt.Sprint(e["tags"]) != "[cross-seed noHL V3X]" {
		t.Errorf("noHL : %v", e)
	}
	if e := got["Film.Libre.2021"]; e["skipped"] != nil || fmt.Sprint(e["tags"]) != "[V3X]" {
		t.Errorf("libre : %v", e)
	}
	if e := got["Serie.S01"]; e["skipped"] != true || fmt.Sprint(e["tags"]) != "[LASTSEED]" {
		t.Errorf("série (un fichier du dossier est un torrent) : %v", e)
	}

	// Les releases choisies dans « Pas sur V3X » passent par /ui/batch/entries : pareil.
	code, out, body = call(t, s, "POST", "/ui/batch/entries", map[string]interface{}{"paths": []string{filepath.Join(parent, "Film.NoHL.2020")}, "source": "local"})
	if code != http.StatusOK || out["tagged"] != true {
		t.Fatalf("%d %s", code, body)
	}
	if e := out["entries"].([]interface{})[0].(map[string]interface{}); e["skipped"] != true {
		t.Errorf("entries : %v", e)
	}

	// Sans règle de correspondance : devinée d'après les torrents du dossier (comme Publier).
	cfg3 := config.Defaults()
	cfg3.Clients = []config.ClientConfig{{ID: "c1", URL: qs.URL, User: "admin", Pass: "demo"}}
	cfg3.SkipTags = []string{"noHL"}
	s3 := NewWith("test", cfg3, "jeton-de-test-0123456789abcdef", 8733, Options{})
	s3.connect()
	_, out, _ = call(t, s3, "GET", "/ui/batch/list?source=local&path="+url.QueryEscape(parent), nil)
	skipped := 0
	for _, x := range out["entries"].([]interface{}) {
		if x.(map[string]interface{})["skipped"] == true {
			skipped++
		}
	}
	if skipped != 1 {
		t.Errorf("règle devinée : %d écartée(s), attendu 1 : %v", skipped, out["entries"])
	}

	// Sans client réglé : pas de colonne à montrer, rien d'écarté.
	cfg2 := config.Defaults()
	s2 := NewWith("test", cfg2, "jeton-de-test-0123456789abcdef", 8733, Options{})
	if _, out, _ := call(t, s2, "GET", "/ui/batch/list?source=local&path="+url.QueryEscape(parent), nil); out["tagged"] != false || out["entries"].([]interface{})[0].(map[string]interface{})["tags"] != nil {
		t.Errorf("sans client : %v", out)
	}
}
