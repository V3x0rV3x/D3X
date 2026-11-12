//go:build windows

package desktop

import (
	"os/exec"
	"syscall"
	"unsafe"

	"fyne.io/systray"
)

// HasTray : l'icône de la zone de notification existe sous Windows.
const HasTray = true

// Tray affiche l'icône de D3X (menu « Ouvrir D3X », « Ouvrir dans le
// navigateur », « Quitter » ; clic sur l'icône = ouvrir) et bloque jusqu'à
// Quit. ready est appelé une fois l'icône posée. onOpen est appelé sur le fil
// de l'icône, à chaque clic : il rend la main tout de suite.
func Tray(ready, onOpen, onBrowser, onQuit func()) {
	systray.Run(func() {
		systray.SetIcon(Icon)
		systray.SetTooltip("D3X")
		open := systray.AddMenuItem("Ouvrir D3X", "Ouvrir la fenêtre de D3X")
		browser := systray.AddMenuItem("Ouvrir dans le navigateur", "Ouvrir la page de D3X dans ton navigateur habituel, avec une session valide")
		systray.AddSeparator()
		quit := systray.AddMenuItem("Quitter", "Arrêter D3X")
		systray.SetOnTapped(onOpen)
		go func() {
			for {
				select {
				case <-open.ClickedCh:
					onOpen()
				case <-browser.ClickedCh:
					onBrowser()
				case <-quit.ClickedCh:
					onQuit()
					return
				}
			}
		}()
		ready()
	}, func() {})
}

// QuitTray retire l'icône et rend la main à Tray.
func QuitTray() { systray.Quit() }

// MessageBox montre un message (il n'y a pas de console sous Windows).
func MessageBox(title, text string) {
	u := syscall.NewLazyDLL("user32.dll").NewProc("MessageBoxW")
	t, _ := syscall.UTF16PtrFromString(title)
	m, _ := syscall.UTF16PtrFromString(text)
	const mbIconWarning = 0x30
	u.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), mbIconWarning)
}

// hideConsole : un programme lancé depuis D3X n'ouvre pas de console.
func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}

// noConsole : comme hideConsole, sans demander à Windows de cacher la première
// fenêtre du programme. Pour la fenêtre de D3X : avec Chrome, le navigateur
// lancé à vide s'arrête, le lancement « --app » devient le navigateur et sa
// fenêtre, créée cachée, ne s'affichait jamais (22/10/26).
func noConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
