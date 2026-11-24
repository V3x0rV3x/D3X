package torrent

import (
	"crypto/sha1"
	"strconv"
	"testing"

	"github.com/V3x0rV3x/D3X/internal/bencode"
)

// emptyFixture : un .torrent dont les morceaux listés dans zeros sont des
// zéros, les autres des données quelconques.
func emptyFixture(t *testing.T, name string, pl int64, files []int64, zeros ...int) *Torrent {
	t.Helper()
	var total int64
	for _, s := range files {
		total += s
	}
	count := (total + pl - 1) / pl
	isZero := map[int]bool{}
	for _, i := range zeros {
		isZero[i] = true
	}
	var pieces []byte
	for i := int64(0); i < count; i++ {
		n := pl
		if i == count-1 {
			n = total - i*pl
		}
		var h [20]byte
		if isZero[int(i)] {
			h = sha1.Sum(make([]byte, n))
		} else {
			h = sha1.Sum([]byte("données " + strconv.FormatInt(i, 10)))
		}
		pieces = append(pieces, h[:]...)
	}
	info := bencode.Dict{"name": []byte(name), "piece length": pl, "pieces": pieces}
	if len(files) == 1 {
		info["length"] = files[0]
	} else {
		list := []bencode.Value{}
		for i, s := range files {
			p := "a.nfo"
			if i > 0 {
				p = "e" + strconv.Itoa(i) + ".mkv"
			}
			list = append(list, bencode.Dict{"length": s, "path": []bencode.Value{[]byte(p)}})
		}
		info["files"] = list
	}
	raw, err := bencode.Encode(bencode.Dict{"info": info})
	if err != nil {
		t.Fatal(err)
	}
	tor, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return tor
}

func TestEmptyMedia(t *testing.T) {
	const mib = 1 << 20
	// Indéfendable S01E03 : des trous éparpillés dans la vidéo (le premier,
	// près du début et suivi de données, peut être du remplissage : ignoré).
	if e := emptyFixture(t, "Serie.S01E03.mkv", mib, []int64{10 * mib}, 1, 4, 7).EmptyMedia(); e == nil || e.Pieces != 2 || e.Bytes != 2*mib || e.File != "Serie.S01E03.mkv" {
		t.Fatalf("trous de vidéo : %+v", e)
	}
	// Un seul morceau vide (bourrage, zone réservée) : jamais un refus.
	if e := emptyFixture(t, "Film.mkv", mib, []int64{10 * mib}, 4).EmptyMedia(); e != nil {
		t.Fatalf("un morceau isolé : %+v", e)
	}
	// Le dernier morceau, plus court, compte aussi.
	if e := emptyFixture(t, "Film.mkv", mib, []int64{9*mib + 12345}, 1, 5, 9).EmptyMedia(); e == nil || e.Pieces != 2 || e.Bytes != mib+12345 {
		t.Fatalf("dernier morceau : %+v", e)
	}
	// Une image disque a des zones vides normales.
	if e := emptyFixture(t, "Jeu.iso", mib, []int64{10 * mib}, 1, 4, 7).EmptyMedia(); e != nil {
		t.Fatalf("ISO : %+v", e)
	}
	// Multi-fichiers : seuls les morceaux entièrement dans la vidéo comptent.
	if e := emptyFixture(t, "R", mib, []int64{mib + 10, 3*mib - 10}, 0, 1, 2, 3).EmptyMedia(); e == nil || e.Pieces != 2 || e.File != "R/e1.mkv" {
		t.Fatalf("multi-fichiers : %+v", e)
	}
	// Gros fichier : 2 morceaux sur 2 000, sous 8 Mio et sous 1 % : passe.
	if e := emptyFixture(t, "Film.mkv", 1<<20, []int64{2000 * mib}, 10, 20).EmptyMedia(); e != nil {
		t.Fatalf("2 morceaux sur 2 000 : %+v", e)
	}
	// Côte Ouest : 11 morceaux de 16 Mio sur des milliers : refusé (8 Mio).
	if e := emptyFixture(t, "Pack.mkv", 16*mib, []int64{3000 * 16 * mib}, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15).EmptyMedia(); e == nil || e.Pieces != 11 {
		t.Fatalf("Côte Ouest : %+v", e)
	}
	// Album : zéros juste après les tags de chaque piste (remplissage, relevé
	// du 24/11/26 : faux positifs sur des FLAC et des MP3 sains) : passe.
	album := []int64{1000, 10 * mib, 10 * mib, 10 * mib}
	pad := []int{4, 5, 6, 14, 15, 16, 24, 25, 26}
	if e := emptyFixture(t, "Album", mib, album, pad...).EmptyMedia(); e != nil {
		t.Fatalf("remplissage des tags : %+v", e)
	}
	// Même album, une piste vide de bout en bout (Guitar Boogie) : refusé.
	whole := append([]int{}, pad...)
	for i := 11; i <= 20; i++ {
		whole = append(whole, i)
	}
	if e := emptyFixture(t, "Album", mib, album, whole...).EmptyMedia(); e == nil || e.File != "Album/e2.mkv" {
		t.Fatalf("piste vide entière : %+v", e)
	}
	// Un bloc en tête de plus de 32 Mio n'est pas du remplissage : refusé.
	head := []int{}
	for i := 1; i <= 40; i++ {
		head = append(head, i)
	}
	if e := emptyFixture(t, "Film.mkv", mib, []int64{100 * mib}, head...).EmptyMedia(); e == nil || e.Pieces != 40 {
		t.Fatalf("bloc de 40 Mio en tête : %+v", e)
	}
}
