package names

import "testing"

func TestKey(t *testing.T) {
	for in, want := range map[string]string{
		"Power":                      "power",
		"L'Été meurtrier":            "letemeurtrier",
		"  Démonstration, la série ": "demonstrationlaserie",
		"The A-Team (2010)":          "theateam2010",
		"":                           "",
		"Šógun":                      "sogun",
		"Shōgun":                     "shogun",
		"Łódź":                       "odz",
		"Æon Flux":                   "onflux",
		"Œdipe":                      "dipe",
		"Straße":                     "strae",
		"Ørnen":                      "rnen",
		"Ștefan":                     "stefan",
		"Việt Nam":                   "vietnam",
	} {
		if got := Key(in); got != want {
			t.Errorf("Key(%q) = %q, attendu %q", in, got, want)
		}
	}
}
