package names

import (
	"strings"
	"unicode"
)

// ascii : les lettres latines minuscules (Latin-1 et Latin étendu A) qui ont
// une décomposition canonique NFD, ramenées à leur lettre de base. Æ, Ø, Œ, ß,
// Ł, Đ… n'en ont pas : la page (normalize("NFD") puis retrait des accents) les
// laisse telles quelles.
var ascii = map[rune]rune{
	'à': 'a',
	'á': 'a',
	'â': 'a',
	'ã': 'a',
	'ä': 'a',
	'å': 'a',
	'ç': 'c',
	'è': 'e',
	'é': 'e',
	'ê': 'e',
	'ë': 'e',
	'ì': 'i',
	'í': 'i',
	'î': 'i',
	'ï': 'i',
	'ñ': 'n',
	'ò': 'o',
	'ó': 'o',
	'ô': 'o',
	'õ': 'o',
	'ö': 'o',
	'ù': 'u',
	'ú': 'u',
	'û': 'u',
	'ü': 'u',
	'ý': 'y',
	'ÿ': 'y',
	'ā': 'a',
	'ă': 'a',
	'ą': 'a',
	'ć': 'c',
	'ĉ': 'c',
	'ċ': 'c',
	'č': 'c',
	'ď': 'd',
	'ē': 'e',
	'ĕ': 'e',
	'ė': 'e',
	'ę': 'e',
	'ě': 'e',
	'ĝ': 'g',
	'ğ': 'g',
	'ġ': 'g',
	'ģ': 'g',
	'ĥ': 'h',
	'ĩ': 'i',
	'ī': 'i',
	'ĭ': 'i',
	'į': 'i',
	'ĵ': 'j',
	'ķ': 'k',
	'ĺ': 'l',
	'ļ': 'l',
	'ľ': 'l',
	'ń': 'n',
	'ņ': 'n',
	'ň': 'n',
	'ō': 'o',
	'ŏ': 'o',
	'ő': 'o',
	'ŕ': 'r',
	'ŗ': 'r',
	'ř': 'r',
	'ś': 's',
	'ŝ': 's',
	'ş': 's',
	'š': 's',
	'ţ': 't',
	'ť': 't',
	'ũ': 'u',
	'ū': 'u',
	'ŭ': 'u',
	'ů': 'u',
	'ű': 'u',
	'ų': 'u',
	'ŵ': 'w',
	'ŷ': 'y',
	'ź': 'z',
	'ż': 'z',
	'ž': 'z',
}

// Ascii : minuscules, sans accents, comme normalize("NFD") suivi du retrait
// des signes combinants dans la page. Les autres caractères sont gardés.
func Ascii(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 0x300 && r <= 0x36F {
			continue // signe combinant (texte déjà décomposé)
		}
		if r == 'İ' {
			r = 'i'
		}
		if f, ok := ascii[unicode.ToLower(r)]; ok {
			r = f
		}
		b.WriteRune(r)
	}
	return b.String()
}
