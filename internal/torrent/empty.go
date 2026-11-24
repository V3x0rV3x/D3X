package torrent

import (
	"bytes"
	"crypto/sha1"
	"path"
	"regexp"
)

// Morceaux vides : un .torrent fabriqué avant la fin du téléchargement. Le
// client réserve le fichier à sa taille finale et les morceaux pas encore
// arrivés valent des zéros ; le .torrent promet alors des zéros et personne
// ne peut avoir la vraie vidéo (Indéfendable S01E03, 72 % vide, 24/11/26).
// Seuls comptent les morceaux ENTIÈREMENT dans une vidéo, une piste audio
// compressée ou un livre : leurs données ne sont jamais un morceau entier de
// zéros. Pas les ISO, archives, WAV ni VOB. Même règle que V3X
// (apps/api/src/lib/empty-pieces.ts), qui refuse ce .torrent.
//
// Remplissage des tags : un FLAC ou un MP3 garde souvent une zone de zéros
// juste après ses tags et sa pochette (bloc PADDING, remplissage ID3), au même
// endroit sur chaque piste d'un album. Le PREMIER bloc de zéros d'un fichier
// est ignoré quand il commence dans ses 32 premiers Mio, en fait 32 au plus et
// qu'il est suivi de vraies données. Une piste vide de bout en bout et les
// trous plus loin comptent toujours.
const padMax = 32 << 20

var emptyMediaRe = regexp.MustCompile(`(?i)\.(mkv|mp4|m4v|avi|m2ts|mts|ts|mov|wmv|webm|mpe?g|flac|mp3|m4a|m4b|aac|ogg|opus|wma|epub|pdf|cbz|cbr|mobi|azw3)$`)

// Empty : les morceaux vides d'un .torrent.
type Empty struct {
	Pieces int    // morceaux vides
	Bytes  int64  // octets de ces morceaux
	File   string // premier fichier touché (chemin avec la racine)
}

// EmptyMedia renvoie les morceaux vides quand il y en a assez pour être sûr
// que le fichier n'était pas fini : 2 au moins, et 8 Mio au moins ou 1 % des
// morceaux de média. nil sinon.
func (t *Torrent) EmptyMedia() *Empty {
	pl := t.PieceLength
	count := int64(len(t.Pieces) / 20)
	if pl <= 0 || count == 0 {
		return nil
	}
	zero := map[int64][]byte{}
	zeroHash := func(n int64) []byte {
		if h, ok := zero[n]; ok {
			return h
		}
		s := sha1.New()
		chunk := make([]byte, min(n, 1<<20))
		for left := n; left > 0; left -= int64(len(chunk)) {
			if left < int64(len(chunk)) {
				chunk = chunk[:left]
			}
			s.Write(chunk)
		}
		h := s.Sum(nil)
		zero[n] = h
		return h
	}
	var e Empty
	var media int64
	for _, f := range t.Files {
		if f.Size <= 0 || !emptyMediaRe.MatchString(path.Base(f.Path)) {
			continue
		}
		end := f.Offset + f.Size
		from := (f.Offset + pl - 1) / pl
		// Dernier morceau entièrement dans le fichier ; le tout dernier du
		// torrent, plus court, en est un quand le fichier finit les données.
		to := end/pl - 1
		if end == t.Total && (count-1)*pl >= f.Offset {
			to = count - 1
		}
		if to > count-1 {
			to = count - 1
		}
		var zs []int64
		size := func(i int64) int64 {
			if i == count-1 {
				return t.Total - i*pl
			}
			return pl
		}
		for i := from; i <= to; i++ {
			n := size(i)
			if n <= 0 {
				continue
			}
			media++
			if bytes.Equal(t.Pieces[i*20:i*20+20], zeroHash(n)) {
				zs = append(zs, i)
			}
		}
		// Remplissage des tags : premier bloc, près du début, suivi de vraies
		// données (le morceau d'après, dans le fichier, n'est pas vide).
		if len(zs) > 0 {
			k := 0
			for k+1 < len(zs) && zs[k+1] == zs[k]+1 {
				k++
			}
			a, b := zs[0], zs[k]
			if b+1 <= to && a*pl-f.Offset < padMax && (b-a+1)*pl <= padMax {
				zs = zs[k+1:]
			}
		}
		for _, i := range zs {
			e.Pieces++
			e.Bytes += size(i)
			if e.File == "" {
				e.File = f.Path
			}
		}
	}
	if e.Pieces < 2 || (e.Bytes < 8<<20 && int64(e.Pieces)*100 < media) {
		return nil
	}
	return &e
}
