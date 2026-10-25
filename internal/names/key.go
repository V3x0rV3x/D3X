package names

import (
	"strings"
	"unicode"
)

// Key : clé d'un titre pour le retrouver d'une saison à l'autre (série
// choisie une fois) : minuscules, sans accents, lettres et chiffres seuls.
// Même règle que seriesKeyOf dans la page.
func Key(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if f, ok := fold[r]; ok {
			r = f
		}
		if r >= 'a' && r <= 'z' || unicode.IsDigit(r) && r < 128 {
			b.WriteRune(r)
		}
	}
	return b.String()
}
