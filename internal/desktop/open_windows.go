//go:build windows

package desktop

import "golang.org/x/sys/windows"

// OpenDefault ouvre la page dans le navigateur par défaut, par l'appel
// ShellExecute de Windows, comme un double-clic sur un lien.
//
// Jusqu'à la 1.1.57, D3X lançait « rundll32 url.dll,FileProtocolHandler
// <adresse> ». Ce détour par un programme de Windows est une technique connue
// des logiciels malveillants pour se cacher derrière un binaire de confiance :
// Windows Defender classait D3X « Behavior:Win32/DefenseEvasion.A!ml » au clic
// sur « Ouvrir dans le navigateur » et le mettait en quarantaine
// (Encrypted_Shards et MasterMan, 13 et 15/11/26).
func OpenDefault(url string) error {
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(url)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, target, nil, nil, windows.SW_SHOWNORMAL)
}
