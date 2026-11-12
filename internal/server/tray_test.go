package server

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/V3x0rV3x/D3X/internal/config"
	"github.com/V3x0rV3x/D3X/internal/desktop"
)

// Zone de notification (Encrypted_Shards, 12/11/26) : « rester » et
// « démarrer réduit » se règlent dans les Réglages, absents ils ne changent
// pas, et « rester » garde D3X lancé fenêtre fermée, surveillance ou pas.
func TestTraySettings(t *testing.T) {
	s := testServer(t, Options{})
	if s.StayInTray() {
		t.Fatal("par défaut, D3X ne reste pas dans la zone de notification")
	}
	tray := func(st map[string]interface{}) map[string]interface{} {
		tr, _ := st["tray"].(map[string]interface{})
		return tr
	}
	code, st, body := call(t, s, http.MethodPost, "/ui/settings", map[string]interface{}{"tray": map[string]bool{"stay": true, "start": true}})
	if code != http.StatusOK {
		t.Fatalf("réglages : %d %s", code, body)
	}
	if tr := tray(st); tr["stay"] != true || tr["start"] != true || tr["on"] != desktop.HasTray {
		t.Fatalf("état : %v", tr)
	}
	if !s.StayInTray() {
		t.Fatal("« rester » coché : D3X doit rester dans la zone de notification")
	}
	if cfg, err := config.Load(); err != nil || !cfg.TrayStay || !cfg.TrayStart {
		t.Fatalf("réglages enregistrés : %+v %v", cfg, err)
	}
	// Sans « tray » dans la demande : rien ne change.
	if code, st, body = call(t, s, http.MethodPost, "/ui/settings", map[string]interface{}{"checkUpdates": false}); code != http.StatusOK || tray(st)["stay"] != true || tray(st)["start"] != true {
		t.Fatalf("réglages sans tray : %d %s", code, body)
	}
	if code, st, body = call(t, s, http.MethodPost, "/ui/settings", map[string]interface{}{"tray": map[string]bool{"stay": false, "start": false}}); code != http.StatusOK || tray(st)["stay"] != false || tray(st)["start"] != false {
		t.Fatalf("réglages décochés : %d %s", code, body)
	}
	if s.StayInTray() {
		t.Fatal("« rester » décoché, sans surveillance : D3X ne reste pas")
	}

	// Mode serveur : pas d'icône, pas d'options.
	r := testServer(t, Options{Remote: true})
	if _, st, body := call(t, r, http.MethodGet, "/ui/state", nil); tray(st)["on"] != false {
		t.Fatalf("mode serveur : %s", body)
	}
}

// http://localhost:8733/ ouvert dans un navigateur, sans session : la page
// apprend que le menu de l'icône l'ouvre avec une session valide (Windows),
// et le lien périmé le dit aussi.
func TestSessionTrayHint(t *testing.T) {
	s := testServer(t, Options{})
	w := do(s, http.MethodGet, "localhost:8733", "/ui/state", nil)
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"tray":`+strconv.FormatBool(desktop.HasTray)) {
		t.Fatalf("sans session : %d %s", w.Code, w.Body.String())
	}
	w = do(s, http.MethodGet, "localhost:8733", "/auth?t=ancien-jeton", nil)
	if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "Ouvrir dans le navigateur") != desktop.HasTray {
		t.Fatalf("lien périmé : %d %s", w.Code, w.Body.String())
	}
	if strings.ContainsRune(w.Body.String(), '—') {
		t.Fatalf("tiret cadratin : %s", w.Body.String())
	}
}

// « Ouvrir dans le navigateur » ajoute une page à côté de la fenêtre :
// fermer l'une ne vaut pas départ de la page tant que l'autre se signale.
// La dernière fermée, si. Une page muette depuis longtemps (fermée sans au
// revoir) ne retient rien.
func TestByeOtherPageOpen(t *testing.T) {
	s := testServer(t, Options{})
	call(t, s, http.MethodGet, "/ui/ping?id=fenetre&shown=1", nil)
	call(t, s, http.MethodGet, "/ui/ping?id=onglet&shown=1", nil)
	if code, _, _ := call(t, s, http.MethodPost, "/ui/bye", map[string]string{"id": "onglet"}); code != http.StatusNoContent {
		t.Fatalf("au revoir : %d", code)
	}
	if s.byeAt.Load() != 0 {
		t.Fatal("onglet fermé, fenêtre encore ouverte : la page n'est pas partie")
	}
	call(t, s, http.MethodPost, "/ui/bye", map[string]string{"id": "fenetre"})
	if s.byeAt.Load() == 0 {
		t.Fatal("dernière page fermée : au revoir attendu")
	}

	s = testServer(t, Options{})
	call(t, s, http.MethodGet, "/ui/ping?id=vieille&shown=1", nil)
	call(t, s, http.MethodGet, "/ui/ping?id=fenetre&shown=1", nil)
	s.pagesMu.Lock()
	s.pages["vieille"].at = time.Now().Add(-10 * time.Minute)
	s.pagesMu.Unlock()
	call(t, s, http.MethodPost, "/ui/bye", map[string]string{"id": "fenetre"})
	if s.byeAt.Load() == 0 {
		t.Fatal("une page muette depuis 10 min ne retient pas D3X")
	}
}
