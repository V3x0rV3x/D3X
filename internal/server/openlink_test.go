package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/V3x0rV3x/D3X/internal/config"
)

func TestOpenLink(t *testing.T) {
	var opened []string
	s := New("test", &config.Config{}, "jeton", 0)
	post := func(srv *Server, body string) int {
		r := httptest.NewRequest(http.MethodPost, "/ui/open", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.openLink(w, r)
		return w.Code
	}
	if c := post(s, `{"url":"https://v3x.club/"}`); c != http.StatusNotFound {
		t.Fatalf("sans ouvreur : %d, attendu 404", c)
	}
	s.SetOpener(func(u string) error { opened = append(opened, u); return nil })
	if c := post(s, `{"url":"https://v3x.club/torrents/1"}`); c != http.StatusOK {
		t.Fatalf("lien https : %d", c)
	}
	for _, bad := range []string{`{"url":"file:///C:/Windows"}`, `{"url":"javascript:alert(1)"}`, `{"url":"https://"}`, `{"url":"calc.exe"}`} {
		if c := post(s, bad); c != http.StatusBadRequest {
			t.Fatalf("%s : %d, attendu 400", bad, c)
		}
	}
	if len(opened) != 1 || opened[0] != "https://v3x.club/torrents/1" {
		t.Fatalf("liens ouverts : %v", opened)
	}
	remote := NewWith("test", &config.Config{}, "jeton", 0, Options{Remote: true})
	remote.SetOpener(func(u string) error { opened = append(opened, u); return nil })
	if c := post(remote, `{"url":"https://v3x.club/"}`); c != http.StatusNotFound {
		t.Fatalf("mode serveur : %d, attendu 404", c)
	}
}
