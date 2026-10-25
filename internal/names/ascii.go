package names

import "strings"

// ascii : les lettres latines minuscules qui ont une décomposition canonique
// NFD (lettre de base + signes combinants), ramenées à leur lettre de base.
// Plages couvertes : U+00C0–U+017F (Latin-1 et Latin étendu A), U+0180–U+024F
// (Latin étendu B : ș, ț, ǎ…) et U+1E00–U+1EFF (Latin étendu additionnel :
// vietnamien…). Æ, Ø, Œ, ß, Ł, Đ… n'en ont pas : la page (normalize("NFD")
// puis retrait des accents) les laisse telles quelles.
var ascii = map[rune]rune{
	// Latin-1 supplément et Latin étendu A (U+00C0–U+017F)
	'à': 'a', 'á': 'a', 'â': 'a', 'ã': 'a', 'ä': 'a', 'å': 'a',
	'ç': 'c', 'è': 'e', 'é': 'e', 'ê': 'e', 'ë': 'e', 'ì': 'i',
	'í': 'i', 'î': 'i', 'ï': 'i', 'ñ': 'n', 'ò': 'o', 'ó': 'o',
	'ô': 'o', 'õ': 'o', 'ö': 'o', 'ù': 'u', 'ú': 'u', 'û': 'u',
	'ü': 'u', 'ý': 'y', 'ÿ': 'y', 'ā': 'a', 'ă': 'a', 'ą': 'a',
	'ć': 'c', 'ĉ': 'c', 'ċ': 'c', 'č': 'c', 'ď': 'd', 'ē': 'e',
	'ĕ': 'e', 'ė': 'e', 'ę': 'e', 'ě': 'e', 'ĝ': 'g', 'ğ': 'g',
	'ġ': 'g', 'ģ': 'g', 'ĥ': 'h', 'ĩ': 'i', 'ī': 'i', 'ĭ': 'i',
	'į': 'i', 'ĵ': 'j', 'ķ': 'k', 'ĺ': 'l', 'ļ': 'l', 'ľ': 'l',
	'ń': 'n', 'ņ': 'n', 'ň': 'n', 'ō': 'o', 'ŏ': 'o', 'ő': 'o',
	'ŕ': 'r', 'ŗ': 'r', 'ř': 'r', 'ś': 's', 'ŝ': 's', 'ş': 's',
	'š': 's', 'ţ': 't', 'ť': 't', 'ũ': 'u', 'ū': 'u', 'ŭ': 'u',
	'ů': 'u', 'ű': 'u', 'ų': 'u', 'ŵ': 'w', 'ŷ': 'y', 'ź': 'z',
	'ż': 'z', 'ž': 'z',
	// Latin étendu B (U+0180–U+024F)
	'ơ': 'o', 'ư': 'u', 'ǎ': 'a', 'ǐ': 'i', 'ǒ': 'o', 'ǔ': 'u',
	'ǖ': 'u', 'ǘ': 'u', 'ǚ': 'u', 'ǜ': 'u', 'ǟ': 'a', 'ǡ': 'a',
	'ǧ': 'g', 'ǩ': 'k', 'ǫ': 'o', 'ǭ': 'o', 'ǰ': 'j', 'ǵ': 'g',
	'ǹ': 'n', 'ǻ': 'a', 'ȁ': 'a', 'ȃ': 'a', 'ȅ': 'e', 'ȇ': 'e',
	'ȉ': 'i', 'ȋ': 'i', 'ȍ': 'o', 'ȏ': 'o', 'ȑ': 'r', 'ȓ': 'r',
	'ȕ': 'u', 'ȗ': 'u', 'ș': 's', 'ț': 't', 'ȟ': 'h', 'ȧ': 'a',
	'ȩ': 'e', 'ȫ': 'o', 'ȭ': 'o', 'ȯ': 'o', 'ȱ': 'o', 'ȳ': 'y',
	// Latin étendu additionnel (U+1E00–U+1EFF)
	'ḁ': 'a', 'ḃ': 'b', 'ḅ': 'b', 'ḇ': 'b', 'ḉ': 'c', 'ḋ': 'd',
	'ḍ': 'd', 'ḏ': 'd', 'ḑ': 'd', 'ḓ': 'd', 'ḕ': 'e', 'ḗ': 'e',
	'ḙ': 'e', 'ḛ': 'e', 'ḝ': 'e', 'ḟ': 'f', 'ḡ': 'g', 'ḣ': 'h',
	'ḥ': 'h', 'ḧ': 'h', 'ḩ': 'h', 'ḫ': 'h', 'ḭ': 'i', 'ḯ': 'i',
	'ḱ': 'k', 'ḳ': 'k', 'ḵ': 'k', 'ḷ': 'l', 'ḹ': 'l', 'ḻ': 'l',
	'ḽ': 'l', 'ḿ': 'm', 'ṁ': 'm', 'ṃ': 'm', 'ṅ': 'n', 'ṇ': 'n',
	'ṉ': 'n', 'ṋ': 'n', 'ṍ': 'o', 'ṏ': 'o', 'ṑ': 'o', 'ṓ': 'o',
	'ṕ': 'p', 'ṗ': 'p', 'ṙ': 'r', 'ṛ': 'r', 'ṝ': 'r', 'ṟ': 'r',
	'ṡ': 's', 'ṣ': 's', 'ṥ': 's', 'ṧ': 's', 'ṩ': 's', 'ṫ': 't',
	'ṭ': 't', 'ṯ': 't', 'ṱ': 't', 'ṳ': 'u', 'ṵ': 'u', 'ṷ': 'u',
	'ṹ': 'u', 'ṻ': 'u', 'ṽ': 'v', 'ṿ': 'v', 'ẁ': 'w', 'ẃ': 'w',
	'ẅ': 'w', 'ẇ': 'w', 'ẉ': 'w', 'ẋ': 'x', 'ẍ': 'x', 'ẏ': 'y',
	'ẑ': 'z', 'ẓ': 'z', 'ẕ': 'z', 'ẖ': 'h', 'ẗ': 't', 'ẘ': 'w',
	'ẙ': 'y', 'ạ': 'a', 'ả': 'a', 'ấ': 'a', 'ầ': 'a', 'ẩ': 'a',
	'ẫ': 'a', 'ậ': 'a', 'ắ': 'a', 'ằ': 'a', 'ẳ': 'a', 'ẵ': 'a',
	'ặ': 'a', 'ẹ': 'e', 'ẻ': 'e', 'ẽ': 'e', 'ế': 'e', 'ề': 'e',
	'ể': 'e', 'ễ': 'e', 'ệ': 'e', 'ỉ': 'i', 'ị': 'i', 'ọ': 'o',
	'ỏ': 'o', 'ố': 'o', 'ồ': 'o', 'ổ': 'o', 'ỗ': 'o', 'ộ': 'o',
	'ớ': 'o', 'ờ': 'o', 'ở': 'o', 'ỡ': 'o', 'ợ': 'o', 'ụ': 'u',
	'ủ': 'u', 'ứ': 'u', 'ừ': 'u', 'ử': 'u', 'ữ': 'u', 'ự': 'u',
	'ỳ': 'y', 'ỵ': 'y', 'ỷ': 'y', 'ỹ': 'y',
}

// Ascii : minuscules, sans accents, comme normalize("NFD") suivi du retrait
// des signes combinants dans la page. Les autres caractères sont gardés.
func Ascii(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 0x300 && r <= 0x36F {
			continue // signe combinant (texte déjà décomposé)
		}
		if f, ok := ascii[r]; ok {
			r = f
		}
		b.WriteRune(r)
	}
	return b.String()
}
