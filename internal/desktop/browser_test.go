package desktop

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestSplitCommand(t *testing.T) {
	cases := map[string][]string{
		``:                        nil,
		`  `:                      nil,
		`/usr/bin/firefox -P p2p`: {"/usr/bin/firefox", "-P", "p2p"},
		`"C:\Program Files\Mozilla Firefox\firefox.exe" -profile "C:\Users\x\AppData\Roaming\Mozilla\Firefox\Profiles\p2p4ever.p2p"`: {
			`C:\Program Files\Mozilla Firefox\firefox.exe`, "-profile", `C:\Users\x\AppData\Roaming\Mozilla\Firefox\Profiles\p2p4ever.p2p`,
		},
		`a  "b c"d ""`: {"a", "b cd", ""},
	}
	for in, want := range cases {
		if got := SplitCommand(in); !reflect.DeepEqual(got, want) {
			t.Errorf("%q : %q, attendu %q", in, got, want)
		}
	}
}

func TestCheckBrowser(t *testing.T) {
	if err := CheckBrowser(""); err != nil {
		t.Fatalf("vide : %v", err)
	}
	if err := CheckBrowser("firefox -P p2p"); err == nil {
		t.Fatal("chemin relatif accepté")
	}
	dir := t.TempDir()
	name := "nav"
	if runtime.GOOS == "windows" {
		name = "nav.exe"
	}
	exe := filepath.Join(dir, name)
	if err := CheckBrowser(`"` + exe + `" -P p2p`); err == nil {
		t.Fatal("programme absent accepté")
	}
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CheckBrowser(`"` + exe + `" -P p2p`); err != nil {
		t.Fatalf("programme présent refusé : %v", err)
	}
	if err := CheckBrowser(`"` + dir + `"`); err == nil {
		t.Fatal("dossier accepté")
	}
	if runtime.GOOS == "windows" {
		bat := filepath.Join(dir, "nav.bat")
		os.WriteFile(bat, []byte("x"), 0o755)
		if err := CheckBrowser(`"` + bat + `"`); err == nil {
			t.Fatal(".bat accepté")
		}
	}
}
