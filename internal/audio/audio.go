// Package audio lit ce qu'il faut d'un album pour le publier : les tags
// (artiste, album, année, label, identifiants MusicBrainz posés par les
// logiciels de tag), la durée et la qualité de chaque piste. Il ne lit que
// les en-têtes, jamais tout le fichier, et ne modifie rien.
//
// Formats : FLAC, MP3 (ID3v2), M4A (AAC, ALAC), Ogg Vorbis, Opus, WAV, DSF ;
// les autres (APE, WavPack, AIFF, DFF) sont reconnus sans tags.
package audio

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/V3x0rV3x/D3X/internal/maketorrent"
)

// Track : une piste lue.
type Track struct {
	Rel        string            `json:"rel"`
	Codec      string            `json:"codec"` // FLAC, MP3, AAC, ALAC, Vorbis, Opus, WAV, DSD, APE, WavPack, AIFF
	Lossless   bool              `json:"lossless"`
	Duration   float64           `json:"duration,omitempty"` // secondes (0 : inconnue)
	SampleRate int               `json:"sampleRate,omitempty"`
	Bits       int               `json:"bits,omitempty"`
	Channels   int               `json:"channels,omitempty"`
	Bitrate    int               `json:"bitrate,omitempty"` // kbit/s, formats avec pertes
	VBR        bool              `json:"vbr,omitempty"`
	Cover      bool              `json:"cover,omitempty"` // image embarquée
	Tags       map[string]string `json:"tags,omitempty"`  // clés normalisées, voir keyOf
	Error      string            `json:"error,omitempty"`
}

// Ext : extensions audio reconnues, et leur codec quand l'extension suffit.
var Ext = map[string]string{
	".flac": "FLAC", ".mp3": "MP3", ".m4a": "", ".ogg": "", ".oga": "", ".opus": "Opus",
	".wav": "WAV", ".dsf": "DSD", ".dff": "DSD", ".ape": "APE", ".wv": "WavPack",
	".aif": "AIFF", ".aiff": "AIFF",
}

// IsAudio dit si un nom de fichier est une piste audio.
func IsAudio(name string) bool {
	_, ok := Ext[strings.ToLower(path.Ext(name))]
	return ok
}

// Dominates dit si une release est un album d'après le poids de ses pistes et
// de ses vidéos : sans vidéo, dès qu'elle a une piste ; avec, quand l'audio
// pèse plus lourd. Une discographie qui contient un clip ou un live reste un
// album (Indrop, 28/10/26) ; un film livré avec sa bande originale reste un
// film. Publier permet de corriger un cas limite à la main.
func Dominates(audioBytes, videoBytes int64) bool {
	return audioBytes > 0 && audioBytes > videoBytes
}

// ReadFile lit une piste. Une piste illisible renvoie son erreur dans Track.
func ReadFile(abs, rel string) Track {
	t := Track{Rel: rel}
	f, err := os.Open(abs)
	if err != nil {
		t.Error = err.Error()
		return t
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		t.Error = err.Error()
		return t
	}
	read(&t, f, st.Size(), strings.ToLower(path.Ext(rel)))
	return t
}

// read remplit t depuis r (taille n) ; jamais de panique sur un fichier abîmé.
func read(t *Track, r io.ReaderAt, n int64, ext string) {
	defer func() {
		if p := recover(); p != nil {
			t.Error = fmt.Sprint("fichier abîmé : ", p)
		}
	}()
	t.Tags = map[string]string{}
	t.Codec = Ext[ext]
	var err error
	switch ext {
	case ".flac":
		err = readFLAC(t, r, n)
	case ".mp3":
		err = readMP3(t, r, n)
	case ".m4a":
		err = readMP4(t, r, n)
	case ".ogg", ".oga", ".opus":
		err = readOgg(t, r, n)
	case ".wav":
		err = readWAV(t, r, n)
	case ".dsf":
		err = readDSF(t, r, n)
	}
	switch t.Codec {
	case "FLAC", "ALAC", "WAV", "DSD", "APE", "WavPack", "AIFF":
		t.Lossless = true
	}
	if err != nil {
		t.Error = err.Error()
	}
	if len(t.Tags) == 0 {
		t.Tags = nil
	}
}

var errFormat = errors.New("format inattendu")

func readAt(r io.ReaderAt, off int64, n int) ([]byte, error) {
	if n < 0 || n > 64<<20 {
		return nil, errFormat
	}
	b := make([]byte, n)
	m, err := r.ReadAt(b, off)
	if m == n {
		return b, nil
	}
	if err == nil {
		err = io.ErrUnexpectedEOF
	}
	return b[:m], err
}

func be16(b []byte) int { return int(b[0])<<8 | int(b[1]) }
func be24(b []byte) int { return int(b[0])<<16 | int(b[1])<<8 | int(b[2]) }
func be32(b []byte) int64 {
	return int64(b[0])<<24 | int64(b[1])<<16 | int64(b[2])<<8 | int64(b[3])
}
func le16(b []byte) int { return int(b[0]) | int(b[1])<<8 }
func le32(b []byte) int64 {
	return int64(b[0]) | int64(b[1])<<8 | int64(b[2])<<16 | int64(b[3])<<24
}
func le64(b []byte) int64 { return le32(b) | le32(b[4:])<<32 }

// ---------- clés de tags ----------

// keyOf ramène les noms de tags des différents formats à une clé commune.
func keyOf(k string) string {
	switch strings.ToUpper(strings.TrimSpace(k)) {
	case "ARTIST", "TPE1", "TP1", "©ART":
		return "artist"
	case "ALBUMARTIST", "ALBUM ARTIST", "ALBUM_ARTIST", "TPE2", "TP2", "AART":
		return "albumartist"
	case "ALBUM", "TALB", "TAL", "©ALB":
		return "album"
	case "TITLE", "TIT2", "TT2", "©NAM":
		return "title"
	case "DATE", "YEAR", "TDRC", "TYER", "TYE", "©DAY":
		return "date"
	case "ORIGINALDATE", "ORIGINALYEAR", "TDOR", "TORY":
		return "originaldate"
	case "TRACKNUMBER", "TRACK", "TRCK", "TRK", "TRKN":
		return "track"
	case "TRACKTOTAL", "TOTALTRACKS":
		return "tracktotal"
	case "DISCNUMBER", "DISC", "TPOS", "TPA", "DISK":
		return "disc"
	case "DISCTOTAL", "TOTALDISCS":
		return "disctotal"
	case "LABEL", "ORGANIZATION", "PUBLISHER", "TPUB", "TPB":
		return "label"
	case "CATALOGNUMBER", "CATALOG", "CATALOG NUMBER":
		return "catalog"
	case "BARCODE", "UPC", "EAN":
		return "barcode"
	case "MEDIA", "TMED", "MEDIATYPE":
		return "media"
	case "GENRE", "TCON", "TCO", "©GEN":
		return "genre"
	case "MUSICBRAINZ_ALBUMID", "MUSICBRAINZ ALBUM ID":
		return "mb_albumid"
	case "MUSICBRAINZ_RELEASEGROUPID", "MUSICBRAINZ RELEASE GROUP ID":
		return "mb_releasegroupid"
	case "MUSICBRAINZ_ALBUMARTISTID", "MUSICBRAINZ ALBUM ARTIST ID":
		return "mb_albumartistid"
	}
	return ""
}

func setTag(t *Track, k, v string) {
	key := keyOf(k)
	v = strings.TrimSpace(strings.TrimRight(v, "\x00"))
	if key == "" || v == "" {
		return
	}
	if _, ok := t.Tags[key]; !ok {
		t.Tags[key] = v
	}
}

// ---------- FLAC ----------

func readFLAC(t *Track, r io.ReaderAt, n int64) error {
	off := int64(0)
	head, err := readAt(r, 0, 4)
	if err != nil {
		return err
	}
	// Un ID3v2 posé devant le flux FLAC (ça arrive) est sauté.
	if string(head[:3]) == "ID3" {
		h, err := readAt(r, 0, 10)
		if err != nil {
			return err
		}
		off = 10 + int64(syncsafe(h[6:10]))
		if head, err = readAt(r, off, 4); err != nil {
			return err
		}
	}
	if string(head) != "fLaC" {
		return errFormat
	}
	off += 4
	for i := 0; i < 128 && off+4 <= n; i++ {
		h, err := readAt(r, off, 4)
		if err != nil {
			return err
		}
		last, typ, size := h[0]&0x80 != 0, h[0]&0x7f, int64(be24(h[1:4]))
		off += 4
		switch typ {
		case 0: // STREAMINFO
			b, err := readAt(r, off, 18)
			if err != nil {
				return err
			}
			rate := int(b[10])<<12 | int(b[11])<<4 | int(b[12])>>4
			t.Channels = int(b[12]>>1&0x07) + 1
			t.Bits = (int(b[12]&0x01)<<4 | int(b[13]>>4)) + 1
			samples := int64(b[13]&0x0f)<<32 | be32(b[14:18])
			t.SampleRate = rate
			if rate > 0 && samples > 0 {
				t.Duration = float64(samples) / float64(rate)
			}
		case 4: // VORBIS_COMMENT
			if size > 16<<20 {
				return errFormat
			}
			b, err := readAt(r, off, int(size))
			if err != nil {
				return err
			}
			vorbisComments(t, b)
		case 6: // PICTURE
			t.Cover = true
		}
		off += size
		if last {
			break
		}
	}
	return nil
}

// vorbisComments lit un bloc de commentaires Vorbis (FLAC, Ogg) : longueurs
// en petit-boutiste, « CLÉ=valeur ».
func vorbisComments(t *Track, b []byte) {
	if len(b) < 8 {
		return
	}
	vl := int(le32(b))
	p := 4 + vl
	if p+4 > len(b) || vl < 0 {
		return
	}
	count := int(le32(b[p:]))
	p += 4
	for i := 0; i < count && i < 10000 && p+4 <= len(b); i++ {
		l := int(le32(b[p:]))
		p += 4
		if l < 0 || p+l > len(b) {
			return
		}
		kv := string(b[p : p+l])
		p += l
		if k, v, ok := strings.Cut(kv, "="); ok {
			if strings.EqualFold(k, "METADATA_BLOCK_PICTURE") {
				t.Cover = true
				continue
			}
			setTag(t, k, v)
		}
	}
}

// ---------- WAV ----------

func readWAV(t *Track, r io.ReaderAt, n int64) error {
	h, err := readAt(r, 0, 12)
	if err != nil {
		return err
	}
	if string(h[0:4]) != "RIFF" || string(h[8:12]) != "WAVE" {
		return errFormat
	}
	off := int64(12)
	byteRate := int64(0)
	for i := 0; i < 64 && off+8 <= n; i++ {
		c, err := readAt(r, off, 8)
		if err != nil {
			return err
		}
		size := le32(c[4:8])
		switch string(c[0:4]) {
		case "fmt ":
			b, err := readAt(r, off+8, 16)
			if err != nil {
				return err
			}
			t.Channels = le16(b[2:4])
			t.SampleRate = int(le32(b[4:8]))
			byteRate = le32(b[8:12])
			t.Bits = le16(b[14:16])
		case "data":
			if byteRate > 0 {
				t.Duration = float64(size) / float64(byteRate)
			}
			return nil
		}
		off += 8 + size + size%2
	}
	return nil
}

// ---------- DSF ----------

func readDSF(t *Track, r io.ReaderAt, n int64) error {
	b, err := readAt(r, 0, 28+52)
	if err != nil {
		return err
	}
	if string(b[0:4]) != "DSD " || string(b[28:32]) != "fmt " {
		return errFormat
	}
	f := b[28:]
	t.Channels = int(le32(f[24:28]))
	t.SampleRate = int(le32(f[28:32]))
	t.Bits = 1
	if count := le64(f[36:44]); t.SampleRate > 0 && count > 0 {
		t.Duration = float64(count) / float64(t.SampleRate)
	}
	return nil
}

// ---------- album ----------

// Album : ce que disent les pistes d'un dossier, réunies.
type Album struct {
	Artist         string  `json:"artist,omitempty"`
	Title          string  `json:"title,omitempty"` // nom de l'album
	Year           string  `json:"year,omitempty"`
	Label          string  `json:"label,omitempty"`
	Catalog        string  `json:"catalog,omitempty"`
	Barcode        string  `json:"barcode,omitempty"`
	Media          string  `json:"media,omitempty"` // CD, Digital Media, Vinyl…
	MBRelease      string  `json:"mbRelease,omitempty"`
	MBReleaseGroup string  `json:"mbReleaseGroup,omitempty"`
	Various        bool    `json:"various,omitempty"` // compilation d'artistes
	Codec          string  `json:"codec,omitempty"`
	Lossless       bool    `json:"lossless,omitempty"`
	Bits           int     `json:"bits,omitempty"`
	SampleRate     int     `json:"sampleRate,omitempty"`
	Bitrate        int     `json:"bitrate,omitempty"`
	VBR            bool    `json:"vbr,omitempty"`
	Mixed          bool    `json:"mixed,omitempty"` // plusieurs codecs ou qualités
	Discs          int     `json:"discs,omitempty"`
	Duration       float64 `json:"duration,omitempty"`
	Cover          bool    `json:"cover,omitempty"` // image dans le dossier ou les pistes
	Token          string  `json:"token,omitempty"` // « FLAC 24bit-96kHz », « MP3 320 »
	Tracks         []Track `json:"tracks"`
}

// File : un fichier de la release (chemin dans la release, chemin réel).
type File struct {
	Rel  string
	Abs  string
	Size int64
}

// Analyze lit les pistes d'une release et les réunit en album. Sans piste
// audio : nil.
func Analyze(files []File) *Album {
	var tracks []Track
	cover := false
	for _, f := range files {
		low := strings.ToLower(path.Base(f.Rel))
		if strings.HasSuffix(low, ".jpg") || strings.HasSuffix(low, ".jpeg") || strings.HasSuffix(low, ".png") {
			cover = true
		}
		if !IsAudio(f.Rel) {
			continue
		}
		tracks = append(tracks, ReadFile(f.Abs, f.Rel))
		if len(tracks) >= 500 {
			break
		}
	}
	return Merge(tracks, cover)
}

// Merge réunit des pistes déjà lues (sur la seedbox, par l'agent).
func Merge(tracks []Track, cover bool) *Album {
	if len(tracks) == 0 {
		return nil
	}
	sort.SliceStable(tracks, func(i, j int) bool {
		di, dj := num(tracks[i].Tags["disc"]), num(tracks[j].Tags["disc"])
		if di != dj {
			return di < dj
		}
		ti, tj := num(tracks[i].Tags["track"]), num(tracks[j].Tags["track"])
		if ti != tj && ti > 0 && tj > 0 {
			return ti < tj
		}
		return tracks[i].Rel < tracks[j].Rel
	})
	a := &Album{Tracks: tracks, Cover: cover}
	pick := func(keys ...string) string {
		for _, k := range keys {
			if v := majority(tracks, k); v != "" {
				return v
			}
		}
		return ""
	}
	a.Title = pick("album")
	a.Artist = pick("albumartist")
	if a.Artist == "" {
		if v := majority(tracks, "artist"); v != "" && share(tracks, "artist", v) >= 0.75 {
			a.Artist = v
		} else if v != "" {
			a.Various = true
		}
	}
	if strings.EqualFold(a.Artist, "Various Artists") || strings.EqualFold(a.Artist, "Artistes variés") {
		a.Various = true
	}
	// L'année de l'édition (celle que connaît MusicBrainz), sinon l'originale.
	if a.Year = year(pick("date")); a.Year == "" {
		a.Year = year(pick("originaldate"))
	}
	a.Label, a.Catalog, a.Barcode, a.Media = pick("label"), pick("catalog"), pick("barcode"), pick("media")
	a.MBRelease, a.MBReleaseGroup = uuid(pick("mb_albumid")), uuid(pick("mb_releasegroupid"))

	// Qualité : celle de la majorité des pistes ; plusieurs = mélange.
	type q struct {
		codec      string
		bits, rate int
	}
	counts := map[q]int{}
	discs := map[int]bool{}
	var kbps, kn int
	for _, t := range tracks {
		counts[q{t.Codec, t.Bits, t.SampleRate}]++
		if t.Cover {
			a.Cover = true
		}
		a.Duration += t.Duration
		if d := num(t.Tags["disc"]); d > 0 {
			discs[d] = true
		}
		if t.Bitrate > 0 {
			kbps += t.Bitrate
			kn++
		}
		if t.VBR {
			a.VBR = true
		}
	}
	var best q
	for k, c := range counts {
		if c > counts[best] || (c == counts[best] && k.codec < best.codec) {
			best = k
		}
	}
	a.Mixed = len(counts) > 1
	a.Codec, a.Bits, a.SampleRate = best.codec, best.bits, best.rate
	a.Lossless = tracks[0].Lossless
	for _, t := range tracks {
		if t.Codec == best.codec {
			a.Lossless = t.Lossless
			break
		}
	}
	if kn > 0 {
		a.Bitrate = kbps / kn
	}
	a.Discs = len(discs)
	if a.Discs == 0 {
		a.Discs = 1
	}
	a.Token = token(a)
	return a
}

// token : la qualité comme le veut la règle de nommage de V3X.
func token(a *Album) string {
	if a.Codec == "" {
		return ""
	}
	if a.Codec == "DSD" {
		if a.SampleRate > 0 {
			return "DSD" + strconv.Itoa(int(math.Round(float64(a.SampleRate)/44100)))
		}
		return ""
	}
	if a.Lossless {
		if a.Bits == 0 || a.SampleRate == 0 {
			return ""
		}
		return fmt.Sprintf("%s %dbit-%skHz", strings.ToUpper(a.Codec), a.Bits, khz(a.SampleRate))
	}
	c := strings.ToUpper(a.Codec)
	if a.VBR {
		return c + " VBR"
	}
	if a.Bitrate > 0 {
		return c + " " + strconv.Itoa(roundRate(a.Bitrate))
	}
	return ""
}

func khz(rate int) string {
	if rate%1000 == 0 {
		return strconv.Itoa(rate / 1000)
	}
	return strconv.FormatFloat(float64(rate)/1000, 'f', 1, 64)
}

// roundRate ramène un débit moyen au palier usuel le plus proche.
func roundRate(k int) int {
	best := k
	for _, s := range []int{96, 128, 160, 192, 224, 256, 320} {
		if abs(s-k) <= 8 {
			best = s
		}
	}
	return best
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// num lit « 3 », « 03 » ou « 3/12 ».
func num(s string) int {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "/")
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func year(s string) string {
	if len(s) >= 4 {
		if _, err := strconv.Atoi(s[:4]); err == nil {
			return s[:4]
		}
	}
	return ""
}

func uuid(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) == 36 && strings.Count(s, "-") == 4 {
		return s
	}
	return ""
}

// majority : la valeur la plus fréquente d'un tag (à égalité, la première).
func majority(tracks []Track, key string) string {
	counts := map[string]int{}
	var order []string
	for _, t := range tracks {
		v := t.Tags[key]
		if v == "" {
			continue
		}
		if counts[v] == 0 {
			order = append(order, v)
		}
		counts[v]++
	}
	best := ""
	for _, v := range order {
		if counts[v] > counts[best] {
			best = v
		}
	}
	return best
}

func share(tracks []Track, key, v string) float64 {
	n := 0
	for _, t := range tracks {
		if t.Tags[key] == v {
			n++
		}
	}
	return float64(n) / float64(len(tracks))
}

// FromPlan lit l'album d'une release listée par maketorrent.
func FromPlan(p *maketorrent.Plan) *Album {
	files := make([]File, 0, len(p.Files))
	for _, f := range p.Files {
		files = append(files, File{Rel: f.Rel, Abs: f.Abs, Size: f.Size})
	}
	return Analyze(files)
}
