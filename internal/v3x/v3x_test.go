package v3x

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestRetryAfterMargin(t *testing.T) {
	h := http.Header{}
	h.Set("Retry-After", "0")
	if d := retryAfter(h, 0); d < time.Second {
		t.Errorf("Retry-After: 0 doit laisser une seconde de marge, pas %v", d)
	}
	if d := retryAfter(http.Header{}, 2); d != 8*time.Second {
		t.Errorf("sans en-tête, 3e essai : %v, 8 s attendues", d)
	}
}

func TestRetryOnlyInBatch(t *testing.T) {
	var calls int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		io.WriteString(w, `{"username":"x"}`)
	}))
	defer s.Close()
	c := New(s.URL, "k", "t")
	ctx := context.Background()

	// Action cliquée : l'erreur remonte tout de suite.
	if _, err := c.Me(ctx); err == nil {
		t.Fatal("un 429 hors lot doit remonter")
	}
	// En lot : attente puis nouvel essai.
	atomic.StoreInt32(&calls, 0)
	var waited bool
	err := c.doJSON(ctx, &Retry{OnWait: func(d time.Duration) { waited = waited || d > 0 }}, http.MethodGet, "/api/me", nil, nil)
	if err != nil || !waited || atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("lot : err=%v attente=%v appels=%d", err, waited, calls)
	}
}

func TestHugeRetryAfterGivesUp(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer s.Close()
	c := New(s.URL, "k", "t")
	start := time.Now()
	if err := c.doJSON(context.Background(), &Retry{}, http.MethodGet, "/x", nil, nil); err == nil {
		t.Fatal("une heure de pause demandée : abandon attendu")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("aucune attente inutile au-delà du plafond")
	}
}

// Le Retry porté par le contexte (WithRetry, le Lot) vaut pour les méthodes
// qui n'en prennent pas (TMDB, fiche, envoi…) ; sans lui, rien ne change : un
// 429 remonte tout de suite (Encrypted_Shards, 05/11/26).
func TestRetryFromContext(t *testing.T) {
	var calls int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1)%2 == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":"rate_limited"}`)
			return
		}
		if r.URL.Path == "/api/torrents" {
			io.WriteString(w, `{"id":"t1","status":"active"}`)
			return
		}
		io.WriteString(w, `{"results":[]}`)
	}))
	defer s.Close()
	c := New(s.URL, "k", "t")

	// Action cliquée : erreur tout de suite, un seul appel.
	if _, err := c.TMDB(context.Background(), "search", nil); err == nil || atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("hors lot : err=%v appels=%d", err, calls)
	}
	atomic.StoreInt32(&calls, 0)
	if _, err := c.Upload(context.Background(), "x.torrent", []byte("d8:announce0:e"), map[string]string{"a": "b"}); err == nil || atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("envoi hors lot : err=%v appels=%d", err, calls)
	}
	if d := Timeout(context.Background(), 20*time.Second); d != 20*time.Second {
		t.Fatalf("délai hors lot : %v", d)
	}

	// Lot : attente puis nouvel essai, la page prévenue.
	atomic.StoreInt32(&calls, 0)
	var waits []time.Duration
	ctx := WithRetry(context.Background(), &Retry{OnWait: func(d time.Duration) { waits = append(waits, d) }})
	if d := Timeout(ctx, 20*time.Second); d != 20*time.Second+RetryBudget {
		t.Fatalf("délai en lot : %v", d)
	}
	if _, err := c.TMDB(ctx, "search", nil); err != nil || atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("lot : err=%v appels=%d", err, calls)
	}
	if len(waits) != 2 || waits[0] <= 0 || waits[1] != 0 {
		t.Fatalf("la page n'a pas été prévenue de la pause : %v", waits)
	}
	// L'envoi d'une release : un 429 n'a rien publié, la même release repart.
	atomic.StoreInt32(&calls, 0)
	up, err := c.Upload(ctx, "x.torrent", []byte("d8:announce0:e"), map[string]string{"a": "b"})
	if err != nil || up.ID != "t1" || atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("envoi en lot : %+v, err=%v, appels=%d", up, err, calls)
	}
}

// Une pause demandée au-delà du plafond abandonne tout de suite, en lot aussi
// par le contexte.
func TestHugeRetryAfterFromContext(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer s.Close()
	c := New(s.URL, "k", "t")
	start := time.Now()
	_, err := c.Fiche(WithRetry(context.Background(), &Retry{}), FicheRequest{})
	var ae *APIError
	if !errors.As(err, &ae) || ae.Code != "rate_limited" {
		t.Fatalf("abandon attendu : %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("aucune attente inutile au-delà du plafond")
	}
}
