package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/V3x0rV3x/D3X/internal/config"
)

// Le lot tourne dans D3X : démarré par la page, il continue sans elle. Ici
// en simulation, sur les releases de démonstration, avec le MediaInfo rendu
// par « la page » (aucun binaire mediainfo dans le PATH du test).
func TestLotServerSide(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("AppData", tmp)
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)
	t.Setenv("PATH", tmp) // pas de mediainfo
	t.Setenv("D3X_MEDIAINFO", "")

	dp := &demoPub{uploaded: map[string][]byte{}}
	vs := fakeV3XForLot(t, dp)
	qs, _ := fakeQbitServer(t, "demo", nil)
	parent := demoRelease(filepath.Join(tmp, "medias"))

	cfg := config.Defaults()
	cfg.V3XBase, cfg.V3XKey = vs.URL, "demo"
	cfg.Clients = []config.ClientConfig{{ID: "c1", URL: qs.URL, User: "admin", Pass: "demo"}}
	s := NewWith("dev", cfg, "jeton-de-test-0123456789abcdef", 8733, Options{Remote: true, Managed: "docker"})
	for i := 0; i < 200 && s.ready() != nil; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if err := s.ready(); err != nil {
		t.Fatal(err)
	}

	paths := []string{
		filepath.Join(parent, "Demonstration.2024.MULTi.1080p.WEB.H264-DEMO"),
		filepath.Join(parent, "Autre.Fichier.Seul.2023.mkv"),
		filepath.Join(parent, "Logiciel.Demo.v1.0"),
		filepath.Join(parent, "Livre.Demo.2022.epub"),
		filepath.Join(parent, "Collection.Demo"),
	}
	// Un lot ne part que sur une demande de ce lancement de D3X : celle d'avant
	// un redémarrage (renvoyée, ou page d'avant la 1.1.39 sans « boot ») est
	// refusée et rien ne démarre (bertho-zero, ticket n°3, 27/10/26).
	_, st0, _ := call(t, s, "GET", "/ui/state", nil)
	boot, _ := st0["boot"].(string)
	if boot == "" || boot != s.boot {
		t.Fatalf("/ui/state sans le lancement : %v", st0["boot"])
	}
	for _, old := range []string{"", "lancement-precedent"} {
		if code, _, body := call(t, s, "POST", "/ui/batch/start", map[string]interface{}{"path": parent, "source": "local", "paths": paths, "boot": old}); code != http.StatusConflict || s.lot.State() != nil {
			t.Fatalf("lot démarré par une demande d'un autre lancement (%q) : %d %s", old, code, body)
		}
	}
	code, out, body := call(t, s, "POST", "/ui/batch/start", map[string]interface{}{"path": parent, "source": "local", "paths": paths, "boot": boot, "options": map[string]interface{}{"sim": true, "video": true, "cap": 300}})
	if code != http.StatusOK || out["state"] != "running" {
		t.Fatalf("%d %s", code, body)
	}
	if code, _, body := call(t, s, "POST", "/ui/batch/start", map[string]interface{}{"path": parent, "source": "local", "paths": paths[:1], "boot": boot}); code != http.StatusConflict {
		t.Fatalf("second lot accepté : %d %s", code, body)
	}

	served := map[string]bool{}
	paused, statusSaw := false, false
	var st map[string]interface{}
	for i := 0; i < 1200; i++ {
		_, st, _ = call(t, s, "GET", "/ui/batch/state", nil)
		if st["state"] == "done" {
			break
		}
		// La page rend le MediaInfo demandé.
		if w, ok := st["mediainfoWanted"].(map[string]interface{}); ok {
			id, _ := w["jobId"].(string)
			if st["state"] != "paused" {
				t.Fatalf("une demande attend mais le lot n'est pas en pause : %v", st["state"])
			}
			paused = true
			if s.LotAdvancing() {
				t.Fatal("lot en pause sur le MediaInfo : LotAdvancing doit être faux")
			}
			// Le hachage continue pendant la pause (vrai travail) ; une fois
			// fini, le lot en pause n'occupe pas D3X.
			for k := 0; k < 200 && s.pub.Busy(); k++ {
				time.Sleep(25 * time.Millisecond)
			}
			if s.Busy() {
				t.Fatal("lot en pause sur le MediaInfo, hachage fini : Busy doit être faux")
			}
			// Toute page la voit aussi dans /ui/status, à côté de l'analyse.
			if _, sst, _ := call(t, s, "GET", "/ui/status", nil); sst["lot"] != nil {
				lot := sst["lot"].(map[string]interface{})
				if _, ok := sst["counts"]; !ok {
					t.Fatalf("status sans l'état de l'analyse : %v", sst)
				}
				if lot["state"] == "paused" && lot["mediainfoWanted"] != nil {
					statusSaw = true
				}
			}
			if !served[id] {
				served[id] = true
				if code, _, body := call(t, s, "POST", "/ui/pub/"+id+"/mediainfo", map[string]string{"text": "General\nComplete name : rien\nFormat : Matroska\n\nVideo\nWidth : 1920 pixels"}); code != http.StatusOK {
					t.Fatalf("mediainfo refusé : %d %s", code, body)
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st["state"] != "done" {
		t.Fatalf("lot pas fini : %v", st)
	}
	if !paused || !statusSaw {
		t.Errorf("pause jamais vue (état %v, status %v)", paused, statusSaw)
	}
	if len(served) != 2 {
		t.Fatalf("MediaInfo demandé %d fois, attendu 2 (les deux vidéos)", len(served))
	}
	got := map[string][2]string{}
	for _, x := range st["entries"].([]interface{}) {
		e := x.(map[string]interface{})
		d, _ := e["detail"].(string)
		got[filepath.Base(e["path"].(string))] = [2]string{e["state"].(string), d}
	}
	want := map[string]string{
		"Logiciel.Demo.v1.0":                           "review",
		"Livre.Demo.2022.epub":                         "review",
		"Collection.Demo":                              "review",
		"Autre.Fichier.Seul.2023.mkv":                  "review", // pas d'œuvre TMDB certaine
		"Demonstration.2024.MULTi.1080p.WEB.H264-DEMO": "review", // le faux V3X annonce un REPACK
	}
	for n, w := range want {
		if got[n][0] != w {
			t.Errorf("%s : %v, attendu %s", n, got[n], w)
		}
	}
	if d := got["Demonstration.2024.MULTi.1080p.WEB.H264-DEMO"][1]; !strings.Contains(d, "un REPACK de cette release est déjà sur V3X") {
		t.Errorf("REPACK non vu : %q", d)
	}
	if d := got["Autre.Fichier.Seul.2023.mkv"][1]; !strings.Contains(d, "œuvre TMDB incertaine") || !strings.Contains(d, "catégorie à choisir") {
		t.Errorf("raisons : %q", d)
	}
	// Le serveur n'est plus occupé, et le journal garde les décisions.
	if s.Busy() {
		t.Error("Busy après la fin du lot")
	}
	if b, _ := os.ReadFile(filepath.Join(tmp, "D3X", "history.jsonl")); !strings.Contains(string(b), "batch_review") {
		t.Errorf("journal : %s", b)
	}
}

// fakeV3XForLot : /api/me, un /api/d3x/match qui ne trouve rien, et les
// routes de publication du mode démo (contexte, TMDB, fiche, versions).
func fakeV3XForLot(t *testing.T, dp *demoPub) *httptest.Server {
	t.Helper()
	vs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dp.handle(w, r) {
			return
		}
		switch r.URL.Path {
		case "/api/me":
			io.WriteString(w, `{"username":"membre-demo","ratio":3.42,"hitAndRun":0}`)
		case "/api/d3x/match":
			var in struct {
				Items []struct{ Key string }
			}
			json.NewDecoder(r.Body).Decode(&in)
			var out []map[string]interface{}
			for _, it := range in.Items {
				out = append(out, map[string]interface{}{"key": it.Key, "match": nil})
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"results": out})
		case "/api/d3x/requests":
			json.NewEncoder(w).Encode(map[string]interface{}{"requests": []interface{}{}})
		case "/api/d3x/team-policies":
			json.NewEncoder(w).Encode(map[string]interface{}{"teams": []interface{}{}, "bannedTags": []interface{}{}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(vs.Close)
	return vs
}
