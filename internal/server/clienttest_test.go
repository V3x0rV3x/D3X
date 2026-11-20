package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/V3x0rV3x/D3X/internal/config"
	"github.com/V3x0rV3x/D3X/internal/crossseed"
)

// « Tester la connexion » de Transmission (neogamaes, 19/11/26 : D3X en
// Docker sur un Synology, Transmission dans un autre conteneur) : le test
// finit toujours vite, et dit en clair ce qui ne va pas.
func TestClientTestTransmission(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("AppData", tmp)
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)
	old := clientTestWait.Load()
	clientTestWait.Store(int64(300 * time.Millisecond))
	t.Cleanup(func() { clientTestWait.Store(old) })

	// Un Transmission ordinaire : RPC sous /transmission/rpc, poignée de main 409.
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/transmission/rpc" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-Transmission-Session-Id") != "sid" {
			w.Header().Set("X-Transmission-Session-Id", "sid")
			w.WriteHeader(http.StatusConflict)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"result": "success", "arguments": map[string]string{"version": "4.0.6"}})
	}))
	t.Cleanup(ok.Close)

	// Une adresse qui ne répond jamais (paquets jetés par un pare-feu).
	release := make(chan struct{})
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(hang.Close)
	t.Cleanup(func() { close(release) })

	// Liste blanche d'adresses IP : la réponse exacte de Transmission.
	white := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, "<p>Unauthorized IP Address.</p><p>Either disable the IP address whitelist or add your address to it.</p><p>If you're editing settings.json, see the 'rpc-whitelist' and 'rpc-whitelist-enabled' entries.</p>")
	}))
	t.Cleanup(white.Close)

	s := NewWith("test", config.Defaults(), "jeton-de-test-0123456789abcdef", 8733, Options{})
	test := func(url string) (bool, string, time.Duration) {
		t.Helper()
		start := time.Now()
		code, out, raw := call(t, s, "POST", "/ui/client/test", map[string]interface{}{"kind": "transmission", "url": url})
		if code != http.StatusOK {
			t.Fatalf("%s : %d %s", url, code, raw)
		}
		e, _ := out["error"].(string)
		return out["ok"] == true, e, time.Since(start)
	}

	// L'adresse de l'interface web, copiée depuis le navigateur.
	for _, u := range []string{ok.URL, ok.URL + "/transmission/web/", ok.URL + "/transmission/web/index.html#accueil", ok.URL + "/transmission"} {
		if good, e, _ := test(u); !good {
			t.Errorf("%s : %s", u, e)
		}
	}
	if good, e, took := test(hang.URL); good || e != crossseed.NoAnswer || took > 5*time.Second {
		t.Fatalf("adresse muette : ok=%v %q en %v", good, e, took)
	}
	if good, e, _ := test(white.URL); good || !strings.Contains(e, "rpc-whitelist") {
		t.Fatalf("liste blanche : ok=%v %q", good, e)
	}
}
