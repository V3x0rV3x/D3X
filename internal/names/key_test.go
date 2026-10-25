package names

import "testing"

func TestKey(t *testing.T) {
	for in, want := range map[string]string{
		"Power":                      "power",
		"L'Été meurtrier":            "letemeurtrier",
		"  Démonstration, la série ": "demonstrationlaserie",
		"The A-Team (2010)":          "theateam2010",
		"":                           "",
	} {
		if got := Key(in); got != want {
			t.Errorf("Key(%q) = %q, attendu %q", in, got, want)
		}
	}
}
