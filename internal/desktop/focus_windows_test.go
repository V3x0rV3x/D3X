//go:build windows

package desktop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Sans fenêtre de D3X ouverte, FocusWindow ne trouve rien et ne fait rien :
// la fenêtre est alors ouverte comme avant.
func TestFocusWindowNone(t *testing.T) {
	if FocusWindow(filepath.Join(t.TempDir(), "aucune-fenetre")) {
		t.Fatal("une fenêtre trouvée pour un profil jamais lancé")
	}
	if FocusWindow("") {
		t.Fatal("profil vide : rien à trouver")
	}
}

// Essai réel (D3X_TEST_EDGE=1) : la fenêtre ouverte, puis réduite, revient
// au premier plan, et aucune autre n'est ouverte (Encrypted_Shards,
// 12/11/26 : plusieurs fenêtres pour quelques double-clics sur l'icône).
func TestFocusWindowRealBrowser(t *testing.T) {
	if os.Getenv("D3X_TEST_EDGE") != "1" {
		t.Skip("essai réel : D3X_TEST_EDGE=1")
	}
	dir := filepath.Join(t.TempDir(), "fenetre-essai")
	w := OpenWindow("about:blank", dir)
	if w == nil {
		t.Skip("aucun navigateur")
	}
	defer CloseWindow(dir)
	w.WaitIdentity()
	profile := strings.ToLower(dir)
	hs := appWindows(profile, map[uint32]bool{})
	if len(hs) == 0 {
		t.Fatal("aucune fenêtre trouvée")
	}
	const swMinimize = 6
	pShowWindow.Call(uintptr(hs[0]), swMinimize)
	time.Sleep(500 * time.Millisecond)
	if !FocusWindow(dir) {
		t.Fatal("fenêtre ouverte non trouvée")
	}
	time.Sleep(500 * time.Millisecond)
	if r, _, _ := pIsIconic.Call(uintptr(hs[0])); r != 0 {
		t.Error("la fenêtre est restée réduite")
	}
	if n := len(appWindows(profile, map[uint32]bool{})); n != len(hs) {
		t.Errorf("%d fenêtres après FocusWindow, attendu %d", n, len(hs))
	}
}
