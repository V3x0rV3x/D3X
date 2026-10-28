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

// 1.1.41 : la règle de réupload vaut pour toute publication. Un lot lancé sur
// un dossier parcouru, avec seulement le detag (la copie taguée est sa
// voisine, hors du lot), le range dans « À vérifier » avant tout hachage ; et
// Publier ouvert à la main sur ce detag le dit et refuse de publier.
func TestPolicyBrowsedLotAndPublier(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("AppData", tmp)
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)
	t.Setenv("PATH", tmp)
	t.Setenv("D3X_MEDIAINFO", "")

	dp := &demoPub{uploaded: map[string][]byte{}}
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
			json.NewEncoder(w).Encode(map[string]interface{}{
				"teams":      []interface{}{map[string]interface{}{"name": "mHDgz", "tags": []string{"mHDgz"}, "policy": "forbidden", "allowed": false}},
				"bannedTags": []interface{}{},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(vs.Close)
	qs, _ := fakeQbitServer(t, "demo", nil)

	dir := filepath.Join(tmp, "medias")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mk := func(name string) string {
		p := filepath.Join(dir, name)
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(150 << 20); err != nil {
			t.Fatal(err)
		}
		f.Close()
		return p
	}
	mk("Le Temps D'un Week-end (1992) - 1080p FR EN x264 ac3 mHDgz.mkv")
	detag := mk("Le Temps D'un Week-end (1992).mkv")

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

	// Le lot, sur le seul detag.
	_, st0, _ := call(t, s, "GET", "/ui/state", nil)
	code, _, body := call(t, s, "POST", "/ui/batch/start", map[string]interface{}{"path": dir, "source": "local", "paths": []string{detag}, "boot": st0["boot"]})
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	var st map[string]interface{}
	for i := 0; i < 400; i++ {
		_, st, _ = call(t, s, "GET", "/ui/batch/state", nil)
		if st["state"] == "done" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	e := st["entries"].([]interface{})[0].(map[string]interface{})
	if e["state"] != "review" || !strings.HasPrefix(e["detail"].(string), "Detag · Réupload interdit · mHDgz : ") {
		t.Fatalf("lot : %v", e)
	}

	// Publier, ouvert à la main sur le même detag.
	code, j, body := call(t, s, "POST", "/ui/pub/start", map[string]string{"path": detag, "source": "local"})
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	id := j["id"].(string)
	defer call(t, s, "POST", "/ui/pub/"+id+"/cancel", nil)
	_, pol, body := call(t, s, "POST", "/ui/pub/"+id+"/policy", map[string]string{"name": "Le Temps (1992)"})
	stop, _ := pol["stop"].(map[string]interface{})
	if stop == nil || stop["label"] != "Detag · Réupload interdit · mHDgz" {
		t.Fatalf("avertissement de Publier : %s", body)
	}
	code, _, body = call(t, s, "POST", "/ui/pub/"+id+"/publish", map[string]interface{}{"fields": map[string]string{"categoryId": "11", "releaseName": "Le Temps (1992)"}})
	if code != http.StatusConflict || !strings.Contains(body, "Detag · Réupload interdit · mHDgz") {
		t.Fatalf("publication : %d %s", code, body)
	}
	if len(dp.uploaded) != 0 {
		t.Errorf("publiée malgré la règle : %v", dp.uploaded)
	}
}
