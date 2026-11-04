package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/V3x0rV3x/D3X/internal/config"
)

// Une connexion à V3X ratée sur une panne passagère est retentée seule
// (MasterMan, 22/10/26) ; une clé refusée ne l'est pas.
func TestV3XRetry(t *testing.T) {
	old := v3xRetryEvery.Load()
	v3xRetryEvery.Store(int64(30 * time.Millisecond))
	defer v3xRetryEvery.Store(old)

	for _, tc := range []struct {
		name      string
		firstCode int
		wantOK    bool
	}{
		{"site en panne puis revenu", http.StatusServiceUnavailable, true},
		{"clé refusée", http.StatusUnauthorized, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/me" {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if calls.Add(1) == 1 || tc.firstCode == http.StatusUnauthorized {
					w.WriteHeader(tc.firstCode)
					io.WriteString(w, `{"error":"x"}`)
					return
				}
				io.WriteString(w, `{"username":"test","ratio":2.5,"hitAndRun":0}`)
			}))
			defer ts.Close()

			tmp := t.TempDir()
			t.Setenv("AppData", tmp)
			t.Setenv("XDG_CONFIG_HOME", tmp)
			t.Setenv("HOME", tmp)
			cfg := config.Defaults()
			cfg.V3XBase, cfg.V3XKey = ts.URL, "cle"
			s := NewWith("dev", cfg, "jeton-de-test-0123456789abcdef", 8733, Options{})

			ok := func() bool {
				s.mu.Lock()
				defer s.mu.Unlock()
				return s.me != nil && s.v3xErr == ""
			}
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) && !ok() {
				time.Sleep(20 * time.Millisecond)
			}
			if ok() != tc.wantOK {
				s.mu.Lock()
				t.Fatalf("connecté : %v (erreur %q, %d appels), attendu %v", s.me != nil, s.v3xErr, calls.Load(), tc.wantOK)
			}
			if !tc.wantOK {
				// Laisse passer plusieurs tours : la clé refusée n'est pas redemandée.
				time.Sleep(200 * time.Millisecond)
				if n := calls.Load(); n != 1 {
					t.Fatalf("clé refusée redemandée %d fois", n)
				}
			}
		})
	}
}

// Client en erreur (Encrypted_Shards, 04/11/26) : retenté seul une fois par
// minute, trois fois au plus, puis plus rien jusqu'au prochain geste du
// membre ; une publication lancée sans aucun client joint le retente d'abord ;
// un client qui refuse son mot de passe n'est jamais retenté seul.
func TestClientRetry(t *testing.T) {
	oldEvery, oldGap := clientRetryEvery.Load(), retryGap.Load()
	clientRetryEvery.Store(int64(20 * time.Millisecond))
	retryGap.Store(0)
	defer func() { clientRetryEvery.Store(oldEvery); retryGap.Store(oldGap) }()

	// Un faux qBittorrent derrière un proxy : 502 tant qu'il est arrêté.
	var up atomic.Bool
	var logins atomic.Int32
	qb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			logins.Add(1)
		}
		if !up.Load() {
			w.WriteHeader(http.StatusBadGateway)
			io.WriteString(w, "Bad Gateway")
			return
		}
		r.ParseForm()
		switch r.URL.Path {
		case "/api/v2/auth/login":
			if r.Form.Get("password") != "pw" {
				io.WriteString(w, "Fails.")
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "x", Path: "/"})
			io.WriteString(w, "Ok.")
		case "/api/v2/app/version":
			if c, err := r.Cookie("SID"); err != nil || c.Value != "x" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			io.WriteString(w, "v5.1.0")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer qb.Close()
	vs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/me" {
			io.WriteString(w, `{"username":"membre","ratio":1,"hitAndRun":0}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer vs.Close()

	tmp := t.TempDir()
	t.Setenv("AppData", tmp)
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)
	newServer := func(pass string) *Server {
		cfg := config.Defaults()
		cfg.V3XBase, cfg.V3XKey = vs.URL, "cle"
		cfg.Clients = []config.ClientConfig{{ID: "qb", URL: qb.URL, User: "admin", Pass: pass}}
		s := NewWith("test", cfg, "jeton-de-test-0123456789abcdef", 8733, Options{})
		// La connexion du lancement (en tâche de fond), une seule.
		for i := 0; i < 300; i++ {
			s.mu.Lock()
			done := s.conns["qb"] != nil
			s.mu.Unlock()
			if done {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		return s
	}
	joined := func(s *Server) bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		k := s.conns["qb"]
		return k != nil && k.c != nil
	}
	waitLogins := func(n int32) {
		for i := 0; i < 300 && logins.Load() < n; i++ {
			time.Sleep(5 * time.Millisecond)
		}
	}

	// Arrêté : la connexion du lancement, puis trois essais seuls, pas un de plus.
	s := newServer("pw")
	gen := s.connGen.Load()
	waitLogins(4)
	time.Sleep(150 * time.Millisecond)
	if n := logins.Load(); n != 4 {
		t.Fatalf("%d connexions, attendu 1 + 3 essais automatiques", n)
	}
	if joined(s) || s.connGen.Load() == gen {
		t.Fatalf("état des clients : joint %v, génération %d (avant %d)", joined(s), s.connGen.Load(), gen)
	}

	// Revenu : une publication sans aucun client joint le retente d'abord.
	up.Store(true)
	env, err := s.envRetry()
	if err != nil || len(env.Targets) != 1 || !joined(s) {
		t.Fatalf("publication : %v, %+v", err, env)
	}

	// Après les trois essais, plus rien seul, même le client revenu ; un geste
	// du membre (analyse, publication, lot) le rejoint aussitôt.
	logins.Store(0)
	up.Store(false)
	s2 := newServer("pw")
	waitLogins(4)
	time.Sleep(100 * time.Millisecond)
	up.Store(true)
	time.Sleep(100 * time.Millisecond)
	if joined(s2) {
		t.Fatal("rejoint sans geste du membre après trois essais")
	}
	s2.retryClients(context.Background()) // geste du membre : essai immédiat
	if !joined(s2) {
		t.Fatal("le geste du membre ne rejoint pas le client revenu")
	}

	// Mot de passe refusé : jamais retenté seul (qBittorrent bloquerait l'adresse).
	logins.Store(0)
	s3 := newServer("faux")
	time.Sleep(150 * time.Millisecond)
	if n := logins.Load(); n != 1 {
		t.Fatalf("mot de passe refusé redemandé : %d connexions", n)
	}
	s3.mu.Lock()
	k := s3.conns["qb"]
	s3.mu.Unlock()
	if k == nil || !k.auth {
		t.Fatalf("refus non reconnu : %+v", k)
	}
}
