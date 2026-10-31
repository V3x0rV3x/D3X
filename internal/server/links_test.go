package server

import (
	"strings"
	"testing"

	"github.com/V3x0rV3x/D3X/internal/config"
)

// Réglage « Mes publications aussi » : enregistré et renvoyé à la page.
func TestLinksPublishSetting(t *testing.T) {
	s := testServer(t, Options{})
	s.mu.Lock()
	s.cfg.Clients = []config.ClientConfig{{ID: "c1", URL: "http://127.0.0.1:1", LinksDir: "/liens"}}
	s.mu.Unlock()
	in := map[string]interface{}{"links": map[string]interface{}{"on": true, "publish": true}}
	if code, _, raw := call(t, s, "POST", "/ui/settings", in); code != 200 {
		t.Fatalf("réglages : %d %s", code, raw)
	}
	if _, _, raw := call(t, s, "GET", "/ui/state", nil); !strings.Contains(raw, `"publish":true`) {
		t.Fatalf("état sans le réglage : %s", raw)
	}
	saved, err := config.Load()
	if err != nil || !saved.Links.On || !saved.Links.Publish {
		t.Fatalf("enregistré : %+v %v", saved.Links, err)
	}
}
