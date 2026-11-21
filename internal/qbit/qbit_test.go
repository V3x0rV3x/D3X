package qbit

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoginErrors(t *testing.T) {
	ctx := context.Background()

	// Identifiants refusés : « Fails. ».
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			io.WriteString(w, "Fails.")
			return
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	c, _ := New(Options{URL: s.URL, User: "a", Pass: "b"})
	if _, err := c.Version(ctx); !errors.Is(err, ErrAuth) {
		t.Errorf("Fails. : %v", err)
	}
	s.Close()

	// Adresse en liste blanche : la connexion échoue mais l'API répond.
	s = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			io.WriteString(w, "Fails.")
		case "/api/v2/app/version":
			io.WriteString(w, "v4.6.7")
		case "/api/v2/app/webapiVersion":
			io.WriteString(w, "2.9.3")
		}
	}))
	c, _ = New(Options{URL: s.URL})
	if v, err := c.Version(ctx); err != nil || v != "v4.6.7" {
		t.Errorf("liste blanche : %q, %v", v, err)
	}
	if c.v5 {
		t.Error("API 2.9 : pause/resume attendus, pas stop/start")
	}
	s.Close()

	// Autre chose répond (page de connexion d'un panneau).
	s = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "<html>Panel login</html>")
	}))
	c, _ = New(Options{URL: s.URL})
	if _, err := c.Version(ctx); err == nil || !strings.Contains(err.Error(), "ce n'est pas qBittorrent") {
		t.Errorf("autre service : %v", err)
	}
	s.Close()
}

func TestRefererWithoutCredentials(t *testing.T) {
	var referer, auth string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		referer, auth = r.Header.Get("Referer"), r.Header.Get("Authorization")
		io.WriteString(w, "Ok.")
	}))
	defer s.Close()
	u := strings.Replace(s.URL, "http://", "http://boite:motdepasse@", 1)
	c, err := New(Options{URL: u + "/qbittorrent/"})
	if err != nil {
		t.Fatal(err)
	}
	_ = c.login(context.Background())
	if strings.Contains(referer, "motdepasse") {
		t.Errorf("identifiants dans le Referer : %s", referer)
	}
	if !strings.HasPrefix(auth, "Basic ") {
		t.Errorf("authentification HTTP de l'adresse non envoyée : %q", auth)
	}
}

func TestTrackerHosts(t *testing.T) {
	tor := Torrent{MagnetURI: "magnet:?xt=urn:btih:abc&tr=https%3A%2F%2Fannounce.v3x.club%2Fpk%2Fannounce&tr=udp%3A%2F%2Fautre.invalid%3A80"}
	h := tor.TrackerHosts()
	if len(h) != 2 || h[0] != "announce.v3x.club" || h[1] != "autre.invalid" {
		t.Errorf("hôtes : %v", h)
	}
}

// Dossier d'une catégorie en gestion automatique : le sien (complet ou relatif
// au dossier par défaut), sinon le dossier par défaut suivi de son nom.
func TestCategoryPath(t *testing.T) {
	var auto string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			io.WriteString(w, "Ok.")
		case "/api/v2/app/version":
			io.WriteString(w, "v5.1.0")
		case "/api/v2/app/webapiVersion":
			io.WriteString(w, "2.11.2")
		case "/api/v2/torrents/categories":
			io.WriteString(w, `{"films":{"name":"films","savePath":"D:\\Films"},"tv":{"name":"tv","savePath":""},"anime/vf":{"name":"anime/vf","savePath":"Animes"}}`)
		case "/api/v2/app/preferences":
			io.WriteString(w, `{"save_path":"C:\\Telechargements\\"}`)
		case "/api/v2/torrents/setAutoManagement":
			r.ParseForm()
			auto = r.Form.Get("hashes") + "=" + r.Form.Get("enable")
		}
	}))
	defer s.Close()
	c, _ := New(Options{URL: s.URL})
	ctx := context.Background()
	for name, want := range map[string]string{
		"films":    `D:\Films`,
		"tv":       `C:\Telechargements\tv`,
		"anime/vf": `C:\Telechargements\Animes`,
		"absente":  "",
	} {
		if got, err := c.CategoryPath(ctx, name); err != nil || got != want {
			t.Errorf("%s : %q (%v), attendu %q", name, got, err, want)
		}
	}
	if err := c.SetAutoManagement(ctx, true, "abc"); err != nil || auto != "abc=true" {
		t.Errorf("gestion automatique : %q (%v)", auto, err)
	}
	if joinClientPath("/data/torrents/", "anime/vf") != "/data/torrents/anime/vf" {
		t.Error("chemin Linux")
	}
}
