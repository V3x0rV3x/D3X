//go:build windows

package desktop

import (
	"strings"

	"golang.org/x/sys/windows"
)

var (
	pShowWindow          = user32.NewProc("ShowWindow")
	pSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	pIsIconic            = user32.NewProc("IsIconic")
	pGetWindowTextLength = user32.NewProc("GetWindowTextLengthW")
)

// swRestore : ShowWindow rend sa taille et sa place à une fenêtre réduite.
const swRestore = 9

// FocusWindow remet au premier plan la fenêtre d'application de D3X déjà
// ouverte (le navigateur lancé avec son profil, profileDir), réduite ou
// cachée derrière d'autres. Faux s'il n'y en a pas : rien n'est fait.
// Quelques double-clics sur l'icône ouvraient autant de fenêtres, dont
// certaines se fermaient seules (Encrypted_Shards, 12/11/26).
func FocusWindow(profileDir string) (found bool) {
	// Une fenêtre introuvable vaut mieux qu'un arrêt de D3X.
	defer func() {
		if recover() != nil {
			found = false
		}
	}()
	profile := strings.ToLower(profileDir)
	if profile == "" {
		return false
	}
	hs := appWindows(profile, map[uint32]bool{})
	if len(hs) == 0 {
		return false
	}
	// La fenêtre de la page porte son titre ; une fenêtre sans titre du
	// navigateur (bulle, menu) ne sert qu'à défaut.
	h := hs[0]
	for _, x := range hs {
		if n, _, _ := pGetWindowTextLength.Call(uintptr(x)); n > 0 {
			h = x
			break
		}
	}
	focus(h)
	return true
}

func focus(h windows.HWND) {
	if r, _, _ := pIsIconic.Call(uintptr(h)); r != 0 {
		pShowWindow.Call(uintptr(h), swRestore)
	}
	pSetForegroundWindow.Call(uintptr(h))
}
