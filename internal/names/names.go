// Package names lit un nom de release (titre, année, série ou film) et
// compare des titres, accents et ponctuation mis de côté.
package names

import (
	"regexp"
	"strings"
	"unicode"
)

// Release : ce qu'un nom de release dit de l'œuvre.
type Release struct {
	Title string // titre lisible (« Le Comte de Monte Cristo »)
	Year  string // année, si le nom la porte
	TV    bool   // série (S01, S01E02, Saison…)
}

var (
	extRe     = regexp.MustCompile(`(?i)\.(mkv|mp4|avi|m2ts|ts|iso|mov|wmv|webm|m4v|flac|mp3|m4a|epub|pdf|cbz|cbr|zip|rar|7z)$`)
	yearRe    = regexp.MustCompile(`^(19[0-9]{2}|20[0-9]{2})$`)
	tvRe      = regexp.MustCompile(`(?i)^(s[0-9]{1,2}(e[0-9]{1,3})*|saison|season|integrale|intégrale|complete)$`)
	stopToken = regexp.MustCompile(`(?i)^(s[0-9]{1,2}(e[0-9]{1,3})*|saison|season|[0-9]{3,4}p|4k|uhd|hdr|hdr10|dv|web|webrip|web-?dl|bluray|blu-ray|bdrip|brrip|hdtv|dvdrip|remux|x26[45]|h26[45]|hevc|avc|multi|multi2|vff|vfq|vfi|vf2|vf|vostfr|vost|french|truefrench|subfrench|english|vo|ac3|eac3|dts|aac|flac|ddp|atmos|10bit|proper|repack|extended|unrated|imax|integrale|intégrale|complete)$`)
	bracketRe = regexp.MustCompile(`[\[\(\{][^\]\)\}]*[\]\)\}]`)
)

// Parse lit un nom de release.
func Parse(name string) Release {
	n := extRe.ReplaceAllString(strings.TrimSpace(name), "")
	// L'année entre parenthèses est gardée, le reste entre crochets ne dit
	// rien du titre.
	var year string
	if m := regexp.MustCompile(`[\(\[]((?:19|20)[0-9]{2})[\)\]]`).FindStringSubmatch(n); m != nil {
		year = m[1]
	}
	n = bracketRe.ReplaceAllString(n, " ")
	fields := strings.FieldsFunc(n, func(r rune) bool { return r == '.' || r == '_' || r == ' ' })
	var title []string
	r := Release{}
	for i, f := range fields {
		f = strings.Trim(f, "-")
		if f == "" {
			continue
		}
		if tvRe.MatchString(f) {
			r.TV = true
		}
		// Une année après au moins un mot de titre ferme le titre (« 2012 »
		// seul reste un titre).
		if yearRe.MatchString(f) && len(title) > 0 {
			if year == "" {
				year = f
			}
			// Une série peut porter son année avant la saison.
			for _, g := range fields[i+1:] {
				if tvRe.MatchString(g) {
					r.TV = true
				}
			}
			break
		}
		// « WEB-GRP », « x264-GRP » : la technique avant le tiret ferme aussi.
		head := f
		if i := strings.IndexByte(f, '-'); i > 0 {
			head = f[:i]
		}
		if (stopToken.MatchString(f) || stopToken.MatchString(head)) && len(title) > 0 {
			for _, g := range fields[i:] {
				if tvRe.MatchString(g) {
					r.TV = true
				}
			}
			break
		}
		title = append(title, f)
	}
	r.Title = strings.TrimSpace(strings.Join(title, " "))
	// « Titre - Sous-titre » : le tiret seul n'est qu'un séparateur.
	r.Title = strings.TrimSpace(strings.ReplaceAll(r.Title, " - ", " "))
	r.Year = year
	return r
}

var fold = map[rune]rune{
	'à': 'a', 'á': 'a', 'â': 'a', 'ã': 'a', 'ä': 'a', 'å': 'a', 'æ': 'a', 'ç': 'c',
	'è': 'e', 'é': 'e', 'ê': 'e', 'ë': 'e', 'ì': 'i', 'í': 'i', 'î': 'i', 'ï': 'i',
	'ñ': 'n', 'ò': 'o', 'ó': 'o', 'ô': 'o', 'õ': 'o', 'ö': 'o', 'ø': 'o', 'œ': 'o',
	'ù': 'u', 'ú': 'u', 'û': 'u', 'ü': 'u', 'ý': 'y', 'ÿ': 'y', 'ß': 's',
}

// Words : les mots d'un titre, en minuscules, sans accents ni ponctuation
// (« L'Été » → « lete »). Les articles d'une lettre disparaissent.
func Words(s string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 1 || (len(cur) == 1 && unicode.IsDigit(cur[0])) {
			out = append(out, string(cur))
		}
		cur = cur[:0]
	}
	for _, r := range strings.ToLower(s) {
		if f, ok := fold[r]; ok {
			r = f
		}
		switch {
		case r == '\'' || r == '’':
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			cur = append(cur, r)
		default:
			flush()
		}
	}
	flush()
	return out
}

// Similarity : part des mots en commun (indice de Jaccard, de 0 à 1).
func Similarity(a, b string) float64 {
	A, B := map[string]bool{}, map[string]bool{}
	for _, w := range Words(a) {
		A[w] = true
	}
	for _, w := range Words(b) {
		B[w] = true
	}
	if len(A) == 0 || len(B) == 0 {
		return 0
	}
	n := 0
	for w := range A {
		if B[w] {
			n++
		}
	}
	return float64(n) / float64(len(A)+len(B)-n)
}

// TitleOnly retire l'année entre parenthèses d'un titre (« Malfaisante (2026) »).
func TitleOnly(s string) (string, string) {
	m := regexp.MustCompile(`\s*\(((?:19|20)[0-9]{2})\)\s*$`).FindStringSubmatch(s)
	if m == nil {
		return strings.TrimSpace(s), ""
	}
	return strings.TrimSpace(s[:len(s)-len(m[0])]), m[1]
}

var (
	seasonRe  = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])s([0-9]{1,2})(?:e([0-9]{1,3}))?(?:[^a-z0-9]|$)`)
	saisonRe  = regexp.MustCompile(`(?i)(?:saison|season)\s*([0-9]{1,2})`)
	reqTailRe = regexp.MustCompile(`(?i)\s*[-\x{2013}\x{2014}:]\s*(?:s[0-9]{1,2}(?:e[0-9]{1,3})?|saison\s*[0-9]{1,2}|season\s*[0-9]{1,2}|int[ée]grale)\s*$`)
)

// Episode : la saison et l'épisode qu'un nom indique (-1 s'il n'en dit rien).
func Episode(s string) (season, episode int) {
	season, episode = -1, -1
	if m := seasonRe.FindStringSubmatch(s); m != nil {
		season = atoi(m[1])
		if m[2] != "" {
			episode = atoi(m[2])
		}
		return
	}
	if m := saisonRe.FindStringSubmatch(s); m != nil {
		season = atoi(m[1])
	}
	return
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n
}

// RequestTitle : le titre d'une demande du site, sans sa saison ni son année
// (« Elsbeth (2024) - Saison 3 » → « Elsbeth », « 2024 »).
func RequestTitle(s string) (string, string) {
	return TitleOnly(reqTailRe.ReplaceAllString(strings.TrimSpace(s), ""))
}

var (
	// techToken : résolution, source ou codec.
	techToken = regexp.MustCompile(`(?i)^([0-9]{3,4}[pi]|4k|uhd|blu-?ray|bdrip|brrip|bdremux|remux|web|web-?dl|webrip|hdtv|hdrip|hdlight|mhd|dvdrip|dvd[59r]?|tvrip|x26[45]|h26[45]|hevc|avc|av1|xvid|divx|10bit)$`)
	// sceneMark : année, langue ou saison, ce qui précède un tag de groupe.
	sceneMark = regexp.MustCompile(`(?i)^((19|20)[0-9]{2}|multi|multi2|french|truefrench|subfrench|vff|vfq|vfi|vf2|vf|vostfr|english|s[0-9]{1,2}(e[0-9]{1,3})*)$`)
	groupTag  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_]{1,19}$`)
)

// LooksLikeRelease : le nom porte les marques d'un nom de release (Hypsis,
// 27/10/26) : une résolution, une source ou un codec (« 1080p », « BluRay »,
// « x264 »), ou un tag de groupe en fin de nom écrit à la façon des releases,
// avec des points et une année ou une langue (« Film.2019.FRENCH-GROUPE »).
// « 1917 (2019) », un dossier rangé par une médiathèque, n'en a pas, ni
// « Spider-Man (2002) ».
func LooksLikeRelease(name string) bool {
	n := extRe.ReplaceAllString(strings.TrimSpace(name), "")
	fields := strings.FieldsFunc(n, func(r rune) bool { return strings.ContainsRune("._ ()[]{}", r) })
	marked := false
	for _, f := range fields {
		if techToken.MatchString(f) {
			return true
		}
		for _, p := range strings.Split(f, "-") {
			if techToken.MatchString(p) {
				return true
			}
			if sceneMark.MatchString(p) {
				marked = true
			}
		}
	}
	if !marked || strings.Contains(n, " ") || strings.Count(n, ".") < 2 {
		return false
	}
	i := strings.LastIndexByte(n, '-')
	return i > 0 && groupTag.MatchString(n[i+1:])
}
