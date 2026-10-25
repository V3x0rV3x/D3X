package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/V3x0rV3x/D3X/internal/config"
)

// Fenêtre ratée (Luci, 21/10/26) : une page chargée mais pas à l'écran est
// signalée, puis priée de se fermer dès qu'une autre page s'affiche.
func TestPingShown(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("AppData", tmp)
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)
	cfg := config.Defaults()
	cfg.CheckUpdates = false
	s := NewWith("dev", cfg, "jeton-de-test-0123456789abcdef", 8733, Options{})

	if code, _, _ := call(t, s, http.MethodGet, "/ui/ping?id=premiere&shown=0", nil); code != http.StatusNoContent {
		t.Fatalf("première page : %d", code)
	}
	if !s.PageHidden() || s.PageShown() {
		t.Fatal("une page chargée mais pas affichée : PageHidden attendu")
	}
	if code, _, _ := call(t, s, http.MethodGet, "/ui/ping?id=seconde&shown=1", nil); code != http.StatusNoContent {
		t.Fatalf("seconde page : %d", code)
	}
	if s.PageHidden() || !s.PageShown() {
		t.Fatal("une page affichée : PageShown attendu")
	}
	code, out, body := call(t, s, http.MethodGet, "/ui/ping?id=premiere&shown=0", nil)
	if code != http.StatusOK || out["close"] != true {
		t.Fatalf("la page jamais affichée doit se fermer : %d %s", code, body)
	}
	// Une page qui s'est affichée une fois (puis réduite) ne reçoit jamais
	// l'ordre de se fermer.
	if code, _, _ := call(t, s, http.MethodGet, "/ui/ping?id=seconde&shown=0", nil); code != http.StatusNoContent {
		t.Fatalf("page réduite : %d", code)
	}
}

// Sortie de veille (karat, 24/10/26) : une page restée muette pendant la
// veille n'est pas partie ; une page qui a dit au revoir, si.
func TestResumedGivesPageTime(t *testing.T) {
	s := &Server{}
	s.Resumed() // page jamais venue : rien à faire
	if s.PageSeen() {
		t.Fatal("Resumed ne doit pas inventer une page")
	}
	old := time.Now().Add(-10 * time.Minute).UnixNano()
	s.lastSeen.Store(old)
	if !s.PageGone() {
		t.Fatal("10 min de silence : page partie attendue")
	}
	s.Resumed()
	if s.PageGone() {
		t.Fatal("au réveil, la page doit avoir le temps de revenir")
	}

	s.lastSeen.Store(old)
	s.byeAt.Store(old + int64(time.Second))
	s.Resumed()
	if !s.PageGone() {
		t.Fatal("une page fermée avant la veille reste partie")
	}
}

// Le lot tourne dans D3X (plus depuis la page) : le paramètre lot du ping
// ne retient plus D3X, la page partie l'est au bout de 3 min de silence ou
// 8 s après son au revoir, et aucun lot ne tourne ici.
func TestPageGoneWithoutLot(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("AppData", tmp)
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)
	cfg := config.Defaults()
	cfg.CheckUpdates = false
	s := NewWith("dev", cfg, "jeton-de-test-0123456789abcdef", 8733, Options{})
	quiet := func(d time.Duration) { s.lastSeen.Store(time.Now().Add(-d).UnixNano()) }

	if code, _, _ := call(t, s, http.MethodGet, "/ui/ping?id=lot&shown=1&lot=1", nil); code != http.StatusNoContent {
		t.Fatalf("ping : %d", code)
	}
	if s.LotAdvancing() || s.Busy() {
		t.Fatal("aucun lot démarré : ni LotAdvancing ni Busy")
	}
	quiet(time.Minute)
	if s.PageGone() {
		t.Fatal("une minute de silence : la page est encore là")
	}
	quiet(10 * time.Minute)
	if !s.PageGone() {
		t.Fatal("10 min de silence : page partie attendue, même avec lot=1")
	}

	call(t, s, http.MethodGet, "/ui/ping?id=lot&shown=1", nil)
	if code, _, _ := call(t, s, http.MethodPost, "/ui/bye", nil); code != http.StatusNoContent {
		t.Fatalf("au revoir : %d", code)
	}
	t0 := time.Now().Add(-20 * time.Second).UnixNano()
	s.lastSeen.Store(t0)
	s.byeAt.Store(t0 + int64(time.Second))
	if !s.PageGone() || !strings.Contains(s.GoneWhy(), "au revoir") {
		t.Fatalf("page fermée : partie attendue (%q)", s.GoneWhy())
	}
}
