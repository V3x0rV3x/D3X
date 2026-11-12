//go:build !windows

package desktop

import (
	"fmt"
	"os"
	"os/exec"
)

// HasTray : hors Windows, D3X garde son terminal (pas d'icône de notification).
const HasTray = false

// Tray n'existe pas ici : ready est appelé, puis la fonction rend la main.
func Tray(ready, onOpen, onBrowser, onQuit func()) { ready() }

// QuitTray ne fait rien hors Windows.
func QuitTray() {}

// MessageBox écrit le message dans le terminal.
func MessageBox(title, text string) { fmt.Fprintln(os.Stderr, title+" : "+text) }

func hideConsole(*exec.Cmd) {}

func noConsole(*exec.Cmd) {}

// CloseWindow ne fait rien hors Windows (la fenêtre suit le navigateur).
func CloseWindow(string) int { return 0 }

// FocusWindow ne sait pas trouver la fenêtre hors Windows : faux, une
// fenêtre est ouverte comme avant.
func FocusWindow(string) bool { return false }

// warmUp ne fait rien hors Windows : la fenêtre s'ouvre directement.
func warmUp(string, string) {}

// taskbarIdentity ne fait rien hors Windows : la fenêtre garde l'identité du
// navigateur.
func taskbarIdentity(string) {}
