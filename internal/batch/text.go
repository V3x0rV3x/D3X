package batch

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/V3x0rV3x/D3X/internal/names"
	"github.com/V3x0rV3x/D3X/internal/v3x"
)

// Text : un champ du site qui arrive en chaîne ou en nombre (année, id,
// saison), gardé tel quel en texte ; null ou absent : vide.
type Text string

func (t *Text) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	switch {
	case s == "null" || s == "":
		*t = ""
	case s[0] == '"':
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*t = Text(v)
	default:
		*t = Text(s)
	}
	return nil
}

func (t Text) String() string { return string(t) }

// Work : une œuvre TMDB (résultat de recherche du site, ou série mémorisée).
type Work struct {
	Type          string  `json:"type"` // movie, tv
	ID            Text    `json:"id"`
	Title         string  `json:"title"`
	OriginalTitle string  `json:"originalTitle,omitempty"`
	Year          Text    `json:"year,omitempty"`
	PosterURL     *string `json:"posterUrl"`
}

// File : un fichier d'une publication (chemin dans la release, taille).
type File struct {
	Rel  string
	Size int64
}

// ---------- œuvre certaine (batch.js) ----------

func yearNum(s Text) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(string(s)), 64)
	return v, err == nil
}

// certainWork : même titre (ou titre original) à 60 % au moins, même année
// à un an près, et le bon type (série si le nom porte une saison). Un seul
// candidat, sinon rien.
func certainWork(results []Work, title string, year Text, isSeries bool) *Work {
	var ok []Work
	y, hasYear := yearNum(year)
	for _, r := range results {
		if (r.Type == "tv") != isSeries {
			continue
		}
		if ry, has := yearNum(r.Year); hasYear && has && math.Abs(ry-y) > 1 {
			continue
		}
		if math.Max(names.Similarity(title, r.Title), names.Similarity(title, r.OriginalTitle)) < 0.6 {
			continue
		}
		ok = append(ok, r)
	}
	if len(ok) != 1 {
		return nil
	}
	w := ok[0]
	return &w
}

// ---------- nom d'un album (music.js) ----------

// nameWithToken : la qualité exigée par V3X dans le nom (« FLAC 24bit-96kHz »).
// Le format déjà écrit est complété, sinon la qualité est ajoutée à la fin,
// dans le style du nom (espaces, points ou tirets).
func nameWithToken(name, token string) string {
	if token == "" {
		return name
	}
	codec := strings.SplitN(token, " ", 2)[0]
	spaced := strings.ContainsAny(name, " \t")
	sep := "-"
	if spaced {
		sep = " "
	} else if strings.Contains(name, ".") {
		sep = "."
	}
	styled := token
	if !spaced {
		styled = strings.ReplaceAll(token, " ", sep)
	}
	re := regexp.MustCompile(`(?i)(^|[^A-Za-z0-9])` + regexp.QuoteMeta(codec) + `([^A-Za-z0-9]|$)`)
	if loc := re.FindStringSubmatchIndex(name); loc != nil {
		// Le codec et ce qui le précède sont remplacés ; le reste est gardé.
		return name[:loc[2]] + name[loc[2]:loc[3]] + styled + name[loc[3]+len(codec):]
	}
	if spaced {
		return name + " [" + token + "]"
	}
	return name + sep + styled
}

func mbURL(id string) string { return "https://musicbrainz.org/release/" + id }

// ---------- NFO (publish.js) ----------

// cp437 : les 128 caractères hauts de la page de code 437 (NFO à dessins ASCII).
const cp437 = "ÇüéâäàåçêëèïîìÄÅÉæÆôöòûùÿÖÜ¢£¥₧ƒáíóúñÑªº¿⌐¬½¼¡«»░▒▓│┤╡╢╖╕╣║╗╝╜╛┐└┴┬├─┼╞╟╚╔╩╦╠═╬╧╨╤╥╙╘╒╓╫╪┘┌█▄▌▐▀αßΓπΣσµτΦΘΩδ∞φε∩≡±≥≤⌠⌡÷≈°∙·√ⁿ²■ "

var cp437Runes = []rune(cp437)

// decodeNfo : UTF-8 strict, sinon CP437.
func decodeNfo(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var sb strings.Builder
	for _, c := range b {
		if c < 128 {
			sb.WriteByte(c)
		} else {
			sb.WriteRune(cp437Runes[int(c)-128])
		}
	}
	return sb.String()
}

var (
	reMIHead = regexp.MustCompile(`(?m)^(General|Général)\s*$`)
	reMIName = regexp.MustCompile(`(Complete name|Nom complet)\s*:`)
)

func hasMediaInfo(t string) bool { return reMIHead.MatchString(t) || reMIName.MatchString(t) }

// nfoWithMediaInfo : le .nfo de la release, suivi du MediaInfo s'il n'en a pas.
func nfoWithMediaInfo(nfo, mi string) string {
	if nfo == "" {
		return ""
	}
	if hasMediaInfo(nfo) || mi == "" {
		return nfo
	}
	return nfo + "\n\n" + mi
}

// pickNfo : l'index du .nfo de la release (premier niveau, de préférence au
// nom de la release, 256 Kio au plus), -1 s'il n'y en a pas.
func pickNfo(name string, files []File) int {
	want := strings.ToLower(reExt4.ReplaceAllString(name, ""))
	top := math.MaxInt
	for _, f := range files {
		if isNfo(f) {
			if d := strings.Count(f.Rel, "/"); d < top {
				top = d
			}
		}
	}
	best := -1
	bestNamed := false
	for i, f := range files {
		if !isNfo(f) || strings.Count(f.Rel, "/") != top {
			continue
		}
		named := strings.Contains(strings.ToLower(f.Rel), want)
		if best < 0 || (named && !bestNamed) {
			best, bestNamed = i, named
		}
	}
	return best
}

var reExt4 = regexp.MustCompile(`(?i)\.[a-z0-9]{2,4}$`)

func isNfo(f File) bool {
	return strings.HasSuffix(strings.ToLower(f.Rel), ".nfo") && f.Size > 0 && f.Size <= 262144
}

var reMediaExt = regexp.MustCompile(`(?i)\.(mkv|mp4|avi|m2ts|ts|mov|wmv|webm|mpe?g|m4v|iso|flac|mp3|m4a|ogg|opus|wav|dsf)$`)

// stripExt : nom d'une release faite d'un seul fichier, sans son extension.
func stripExt(name string, single bool) string {
	if !single {
		return name
	}
	return reMediaExt.ReplaceAllString(name, "")
}

// ---------- textes (batch.js, publish.js) ----------

func dupText(d *v3x.Match) string {
	s := "déjà sur V3X : " + d.Name
	if d.Kind == "exact" || d.Kind == "self" {
		return s + " · « Revoir » pour la seeder avec ces fichiers"
	}
	return s + " · la Médiathèque la met en seed avec ces fichiers"
}

func goneText(g *v3x.Gone) string {
	verb := "retirée"
	switch g.Status {
	case "trumped":
		verb = "remplacée par une meilleure version"
	case "archived":
		verb = "archivée"
	}
	s := "une fiche V3X de ce contenu a été " + verb
	if g.Own && g.Reason != "" {
		s += " (ta fiche, motif : " + g.Reason + ")"
	}
	return s
}

var errorTexts = map[string]string{
	"validation":               "un champ de la fiche est refusé",
	"rights_not_declared":      "il faut déclarer avoir le droit de partager ce contenu",
	"invalid_category":         "choisis une sous-catégorie",
	"invalid_torrent":          "le .torrent est refusé par V3X",
	"file_too_large":           "le .torrent dépasse 32 Mio (trop de fichiers)",
	"archive_only":             "une release qui ne contient que des archives n'est pas acceptée",
	"review_quota":             "tu as déjà trop de releases en attente de validation : attends qu'elles soient revues",
	"tmdb_required":            "une fiche TMDB est obligatoire pour un film ou une série (ou coche « Pas de fiche TMDB » si elle n'existe vraiment pas)",
	"tmdb_link_invalid":        "le lien TMDB n'est pas valide",
	"nfo_required":             "le NFO est obligatoire pour un film ou une série",
	"mediainfo_required":       "le NFO doit contenir un vrai MediaInfo (pistes vidéo et audio mesurées)",
	"resolution_too_low":       "la résolution est sous le minimum de V3X",
	"ddl_source":               "la release porte la marque d'un site de téléchargement direct",
	"free_software":            "les logiciels libres et gratuits ne se publient pas sur V3X",
	"audio_naming":             "le nom d'une release musicale doit suivre le nommage du site",
	"name_missing_disc":        "le nom doit indiquer le disque d'origine",
	"name_missing_gpu":         "le nom doit indiquer l'encodeur matériel",
	"exclusive_group_mismatch": "l'exclusivité est réservée aux releases de ta team",
	"team_reupload_forbidden":  "cette team refuse les réuploads de ses releases",
	"banned_tag":               "le groupe de cette release est refusé sur V3X",
	"team_reupload_ask":        "cette team demande son accord avant un réupload",
	"duplicate":                "cette release est déjà sur V3X (même .torrent)",
	"duplicate_removed":        "cette release a déjà été publiée puis retirée",
	"duplicate_content":        "le même contenu est déjà sur V3X",
	"invalid_key_or_scope":     "ta clé API n'a pas le droit « Upload » (Réglages ▸ Intégrations sur V3X)",
}

// pubErr : le refus de V3X en clair (même texte que la page).
func pubErr(code, body string) string {
	if strings.HasPrefix(code, "upscale") {
		return "les releases agrandies par IA ne sont pas acceptées telles quelles"
	}
	msg := errorTexts[code]
	if msg == "" {
		msg = code
	}
	var b struct {
		Field    string `json:"field"`
		Detail   string `json:"detail"`
		Existing *struct {
			ID string `json:"id"`
		} `json:"existing"`
	}
	if body != "" && json.Unmarshal([]byte(body), &b) == nil {
		if code == "validation" && b.Field != "" {
			msg += " : " + b.Field
			if b.Detail != "" {
				msg += " (" + b.Detail + ")"
			}
		}
		if b.Existing != nil && b.Existing.ID != "" {
			msg += ". Fiche existante : https://v3x.club/torrents/" + b.Existing.ID
		}
	}
	return msg
}

// warnings : les contrôles de la fiche que le lot refuse de passer outre
// (BLOCKING dans la page), avec leur texte.
var warnings = map[string]string{
	"mediainfo_missing":      "Pas de MediaInfo : il est obligatoire pour un film ou une série.",
	"mediainfo_not_measured": "Le MediaInfo ne contient pas de pistes mesurées : V3X le refusera.",
	"resolution_too_low":     "Résolution sous le minimum de V3X : la publication sera refusée.",
	"name_missing_gpu":       "Encodage matériel détecté : le nom doit l'indiquer (NVENC, QSV…).",
	"gpu_name_conflict":      "Le nom et le MediaInfo ne disent pas le même encodeur.",
	"tmdb_not_found":         "Cette fiche TMDB est introuvable.",
	"audio_naming":           "Le nom d'une release musicale doit suivre le nommage du site.",
	"category_unknown":       "Catégorie inconnue sur V3X.",
}

var blocking = map[string]bool{"mediainfo_missing": true, "mediainfo_not_measured": true, "resolution_too_low": true, "name_missing_gpu": true, "gpu_name_conflict": true, "tmdb_not_found": true, "audio_naming": true, "category_unknown": true}

var reSeason = regexp.MustCompile(`(?i)\bS\d{1,2}(E\d{1,3})?\b`)
