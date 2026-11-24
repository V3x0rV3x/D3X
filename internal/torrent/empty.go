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
		for i := from; i <= to; i++ {
			n := pl
			if i == count-1 {
				n = t.Total - i*pl
			}
			if n <= 0 {
				continue
			}
			media++
			if bytes.Equal(t.Pieces[i*20:i*20+20], zeroHash(n)) {
				e.Pieces++
				e.Bytes += n
				if e.File == "" {
					e.File = f.Path
				}
			}
		}
	}
	if e.Pieces < 2 || (e.Bytes < 8<<20 && int64(e.Pieces)*100 < media) {
		return nil
	}
	return &e
}
