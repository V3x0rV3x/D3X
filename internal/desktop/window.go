// Package desktop donne à D3X sa fenêtre d'application : la page dans une
// fenêtre sans barre d'adresse (Edge ou Chrome en mode application, profil à
// part), l'icône de la zone de notification sous Windows et les messages
// d'erreur quand il n'y a pas de console.
package desktop

import (
	_ "embed"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Icon est l'icône de D3X (ICO multi-tailles).
//
//go:embed icon.ico
var Icon []byte

// appBrowser trouve un navigateur capable d'ouvrir une fenêtre d'application.
func appBrowser() string {
	var cands []string
	switch runtime.GOOS {
	case "windows":
		for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LocalAppData"} {
			if base := os.Getenv(env); base != "" {
				cands = append(cands,
					filepath.Join(base, `Microsoft\Edge\Application\msedge.exe`),
					filepath.Join(base, `Google\Chrome\Application\chrome.exe`))
			}
		}
	case "darwin":
		cands = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		}
	default:
		for _, n := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "microsoft-edge"} {
			if p, err := exec.LookPath(n); err == nil {
				cands = append(cands, p)
			}
		}
	}
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

// Window est une fenêtre d'application ouverte.
type Window struct {
	cmd *exec.Cmd
	// identity est fermé une fois l'identité de D3X posée sur la fenêtre
	// pour la barre des tâches (ou abandonnée).
	identity chan struct{}
}

// OpenWindow ouvre la page dans une fenêtre d'application, avec un profil à
// part (profileDir) : elle a sa propre icône dans la barre des tâches, et sa
// fermeture se voit (Wait). Sous Windows, elle y porte l'identité de D3X et
// non celle du navigateur : épinglée, elle relance D3X. nil si aucun
// navigateur ne s'y prête.
func OpenWindow(url, profileDir string) *Window {
	b := appBrowser()
	if b == "" {
		return nil
	}
	_ = os.MkdirAll(profileDir, 0o700)
	warmUp(b, profileDir)
	cmd := exec.Command(b, append([]string{"--app=" + url, "--window-size=1280,880"}, profileArgs(profileDir)...)...)
	noConsole(cmd)
	if err := cmd.Start(); err != nil {
		return nil
	}
	w := &Window{cmd: cmd, identity: make(chan struct{})}
	go func() {
		defer close(w.identity)
		taskbarIdentity(profileDir)
	}()
	return w
}

// WaitIdentity attend que la fenêtre porte l'identité de D3X dans la barre
// des tâches (15 s au plus si elle n'apparaît pas). Utile à un D3X qui
// s'arrête juste après avoir ouvert la fenêtre. Hors Windows, rend la main
// aussitôt.
func (w *Window) WaitIdentity() { <-w.identity }

// profileArgs : le profil à part de D3X et ses réglages, communs au lancement
// sans fenêtre et à l'ouverture de la fenêtre.
func profileArgs(profileDir string) []string {
	return []string{
		"--user-data-dir=" + profileDir,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-features=Translate",
	}
}

// Wait attend la fermeture de la fenêtre.
func (w *Window) Wait() { _ = w.cmd.Wait() }

// OpenDefault (open_windows.go, open_other.go) ouvre la page dans le
// navigateur par défaut.
