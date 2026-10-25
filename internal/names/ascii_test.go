package names

import "testing"

func TestAscii(t *testing.T) {
	for in, want := range map[string]string{
		"Démonstration":                   "demonstration",
		"Ve\u0301rite\u0301 cache\u0301e": "verite cachee",
		"Æon Øst ß":                       "æon øst ß",
		"İstanbul":                        "istanbul",
		"Россия":                          "россия",
	} {
		if got := Ascii(in); got != want {
			t.Errorf("Ascii(%q) = %q, attendu %q", in, got, want)
		}
	}
}
