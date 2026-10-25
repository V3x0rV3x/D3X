package names

import "strings"

// Key : clé d'un titre pour le retrouver d'une saison à l'autre (série
// choisie une fois) : minuscules, sans accents, lettres et chiffres seuls.
// Même règle que seriesKeyOf dans la page.
func Key(s string) string {
	var b strings.Builder
	for _, r := range Ascii(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
