package names

import "testing"

func TestFromDisk(t *testing.T) {
	nfd := "Le Fabuleux Destin d'Amélie Poulain (2001)/Crédits.mkv"
	nfc := "Le Fabuleux Destin d'Amélie Poulain (2001)/Crédits.mkv"
	if got := fromDisk("darwin", nfd); got != nfc {
		t.Errorf("macOS : %q, attendu %q", got, nfc)
	}
	if got := fromDisk("darwin", nfc); got != nfc {
		t.Errorf("macOS, déjà composé : %q", got)
	}
	// Ailleurs, les octets du disque sont gardés tels quels.
	for _, goos := range []string{"linux", "windows", "freebsd"} {
		if got := fromDisk(goos, nfd); got != nfd {
			t.Errorf("%s : %q, attendu inchangé", goos, got)
		}
	}
	if got := fromDisk("darwin", "Sans accent.mkv"); got != "Sans accent.mkv" {
		t.Errorf("sans accent : %q", got)
	}
}
