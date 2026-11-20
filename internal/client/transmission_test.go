package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTransmissionRPCPath(t *testing.T) {
	for in, want := range map[string]string{
		"":                             "/transmission/rpc",
		"/":                            "/transmission/rpc",
		"/transmission":                "/transmission/rpc",
		"/transmission/":               "/transmission/rpc",
		"/transmission/web":            "/transmission/rpc",
		"/transmission/web/":           "/transmission/rpc",
		"/transmission/web/index.html": "/transmission/rpc",
		"/transmission/rpc":            "/transmission/rpc",
		"/transmission/rpc/":           "/transmission/rpc",
		"/seedbox/transmission/web/":   "/seedbox/transmission/rpc",
		"/tr/rpc":                      "/tr/rpc",
		"/seedbox":                     "/seedbox/transmission/rpc",
		"/transmission-proxy":          "/transmission-proxy/transmission/rpc",
	} {
		if got := trRPCPath(in); got != want {
			t.Errorf("trRPCPath(%q) = %q, attendu %q", in, got, want)
		}
	}
}

// Les refus de Transmission dits en clair (neogamaes, 19/11/26), avec les
// réponses exactes de Transmission.
func TestTransmissionRefusals(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		want   string
	}{
		{403, "<p>Unauthorized IP Address.</p><p>Either disable the IP address whitelist or add your address to it.</p>", "rpc-whitelist"},
		{403, "<p>Too many unsuccessful login attempts. Please restart transmission-daemon.</p>", "mot de passe"},
		{421, "<p>Transmission received your request, but the hostname was unrecognized.</p>", "rpc-host-whitelist"},
		{404, "", "9091"},
		{502, "", "HTTP 502"},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			io.WriteString(w, c.body)
		}))
		cl, err := New(Options{Kind: "transmission", URL: srv.URL})
		if err != nil {
			t.Fatal(err)
		}
		_, err = cl.Version(context.Background())
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("HTTP %d : %v, attendu « %s »", c.status, err, c.want)
		}
	}
}
