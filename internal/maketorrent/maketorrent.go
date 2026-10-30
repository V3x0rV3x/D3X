// Package maketorrent fabrique le .torrent d'une release à partir des fichiers.
//
// Deux temps : Scan liste les fichiers (rapide, sans rien lire) et donne déjà
// les empreintes de contenu, de quoi savoir si la release existe sur V3X avant
// de hacher quoi que ce soit ; Hash lit les données et calcule les pièces sur
// tous les cœurs de la machine.
package maketorrent

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/V3x0rV3x/D3X/internal/bencode"
	"github.com/V3x0rV3x/D3X/internal/config"
	"github.com/V3x0rV3x/D3X/internal/names"
	"github.com/V3x0rV3x/D3X/internal/torrent"
)

// DefaultExclude : motifs écartés par défaut (voir config). Les fichiers et
// dossiers cachés (« .DS_Store », « .@__thumb »…) sont toujours écartés.
var DefaultExclude = config.DefaultExclude

// Source est le marqueur posé dans le dict info, comme le fait le site : le
// .torrent fabriqué a ainsi déjà l'info-hash qu'il aura sur V3X.
const Source = "V3X"

// Entry est un fichier de la release.
type Entry struct {
	Rel     string // chemin dans la release, séparateur « / » (mono-fichier : le nom), recomposé sur un Mac
	Abs     string // chemin sur le disque, tel quel
	Size    int64
	ModTime time.Time
}

// Plan est une release listée, prête à être hachée.
type Plan struct {
	Root        string
	Name        string
	Single      bool
	Files       []Entry
	Total       int64
	PieceLength int64
	ContentHash string
	SizeHash    string
	Skipped     []string // écartés (motifs, liens cassés, boucles)
}

// Scan liste la release (un fichier, ou un dossier et tout son contenu).
func Scan(root string, exclude []string) (*Plan, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	// Nom de la release et chemins du .torrent : recomposés sur un Mac, pour que
	// le client qui seede sur Windows ou Linux retrouve les fichiers à accents.
	p := &Plan{Root: abs, Name: names.FromDisk(filepath.Base(abs))}
	if !st.IsDir() {
		if !st.Mode().IsRegular() {
			return nil, errors.New("ce n'est ni un fichier ni un dossier")
		}
		p.Single = true
		p.Files = []Entry{{Rel: p.Name, Abs: abs, Size: st.Size(), ModTime: st.ModTime()}}
	} else {
		real, err := filepath.EvalSymlinks(abs)
		if err != nil {
			real = abs
		}
		if err := p.walk(abs, "", map[string]bool{real: true}, exclude); err != nil {
			return nil, err
		}
		sort.Slice(p.Files, func(i, j int) bool { return lessPath(p.Files[i].Rel, p.Files[j].Rel) })
	}
	if len(p.Files) == 0 {
		return nil, errors.New("aucun fichier à mettre dans le torrent")
	}
	for _, f := range p.Files {
		p.Total += f.Size
	}
	if p.Total == 0 {
		return nil, errors.New("tous les fichiers sont vides")
	}
	p.PieceLength = AutoPieceLength(p.Total)
	files := make([]torrent.File, len(p.Files))
	for i, f := range p.Files {
		path := f.Rel
		if !p.Single {
			path = p.Name + "/" + f.Rel
		}
		files[i] = torrent.File{Path: path, Size: f.Size}
	}
	p.ContentHash = torrent.ContentSignature(files, p.Total)
	p.SizeHash = torrent.SizeSignature(files, p.Total)
	return p, nil
}

// Excluded dit si un nom est écarté : caché, ou repris par un motif.
func Excluded(name string, patterns []string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	low := strings.ToLower(name)
	if systemDirs[low] {
		return true
	}
	for _, pat := range patterns {
		if ok, _ := filepath.Match(strings.ToLower(pat), low); ok {
			return true
		}
	}
	return false
}

// systemDirs : dossiers du système ou du NAS, jamais une release, souvent
// illisibles (corbeille Windows à la racine d'un disque : « open
// D:\$RECYCLE.BIN\S-1-5-18: Access is denied », Maxxi37 21/10/26). Écartés
// d'office, même si la liste d'exclusions des Réglages a été modifiée.
var systemDirs = map[string]bool{
	"$recycle.bin": true, "recycler": true, "system volume information": true,
	"$windows.~bt": true, "$windows.~ws": true, "$sysreset": true,
	"lost+found": true, "@eadir": true, "#recycle": true, "#snapshot": true, "@recycle": true,
}

// walk parcourt un dossier en suivant les liens symboliques : un lien compte
// pour sa cible (sinon le torrent annonce la taille du lien et le hachage lit
// la cible). Lien cassé ignoré ; lien vers un dossier déjà parcouru ou parent
// ignoré, pour ne jamais boucler ni hacher un disque entier.
func (p *Plan) walk(dir, rel string, seen map[string]bool, exclude []string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		r := names.FromDisk(name)
		if rel != "" {
			r = rel + "/" + r
		}
		if Excluded(name, exclude) {
			p.Skipped = append(p.Skipped, r)
			continue
		}
		full := filepath.Join(dir, name)
		st, err := os.Stat(full)
		if err != nil {
			p.Skipped = append(p.Skipped, r+" (lien cassé ou illisible)")
			continue
		}
		switch {
		case st.IsDir():
			real, err := filepath.EvalSymlinks(full)
			if err != nil {
				real = full
			}
			if seen[real] {
				p.Skipped = append(p.Skipped, r+" (déjà parcouru)")
				continue
			}
			seen[real] = true
			if err := p.walk(full, r, seen, exclude); err != nil {
				return err
			}
		case st.Mode().IsRegular():
			p.Files = append(p.Files, Entry{Rel: r, Abs: full, Size: st.Size(), ModTime: st.ModTime()})
		default:
			p.Skipped = append(p.Skipped, r+" (ni fichier ni dossier)")
		}
	}
	return nil
}

// lessPath trie par segments de chemin (« a/b » avant « a b/c »).
func lessPath(a, b string) bool {
	sa, sb := strings.Split(a, "/"), strings.Split(b, "/")
	for i := 0; i < len(sa) && i < len(sb); i++ {
		if sa[i] != sb[i] {
			return sa[i] < sb[i]
		}
	}
	return len(sa) < len(sb)
}

// AutoPieceLength vise 2 000 pièces au plus, de 32 Kio à 16 Mio.
func AutoPieceLength(total int64) int64 {
	pl := int64(32 << 10)
	for pl < 16<<20 && total/pl >= 2000 {
		pl <<= 1
	}
	return pl
}

// Progress reçoit l'avancement du hachage en octets.
type Progress func(done, total int64)

// ErrChanged : un fichier a changé pendant le hachage.
var ErrChanged = errors.New("un fichier a changé pendant le hachage (téléchargement pas fini ?) : relance quand il ne bouge plus")

// Hash lit les données et renvoie le .torrent. Un .torrent déjà fabriqué pour
// exactement ces fichiers (mêmes chemins, tailles et dates) est repris du cache.
func (p *Plan) Hash(ctx context.Context, createdBy string, progress Progress) (raw []byte, fromCache bool, err error) {
	key := p.cacheKey()
	if b, ok := cacheGet(key, p); ok {
		if progress != nil {
			progress(p.Total, p.Total)
		}
		return b, true, nil
	}
	pieces, err := p.hashPieces(ctx, progress)
	if err != nil {
		return nil, false, err
	}
	info := bencode.Dict{
		"name":         []byte(p.Name),
		"piece length": p.PieceLength,
		"pieces":       pieces,
		"private":      int64(1),
		"source":       []byte(Source),
	}
	if p.Single {
		info["length"] = p.Files[0].Size
	} else {
		list := make([]bencode.Value, len(p.Files))
		for i, f := range p.Files {
			segs := strings.Split(f.Rel, "/")
			parts := make([]bencode.Value, len(segs))
			for j, s := range segs {
				parts[j] = []byte(s)
			}
			list[i] = bencode.Dict{"length": f.Size, "path": parts}
		}
		info["files"] = list
	}
	raw, err = bencode.Encode(bencode.Dict{
		"announce":      []byte("https://announce.v3x.club/announce"),
		"created by":    []byte(createdBy),
		"creation date": time.Now().Unix(),
		"info":          info,
	})
	if err != nil {
		return nil, false, err
	}
	cachePut(key, raw)
	return raw, false, nil
}

type job struct {
	index int
	buf   []byte
	n     int
}

// hashPieces : un lecteur remplit des tampons de la taille d'une pièce, les
// fichiers mis bout à bout ; plusieurs ouvriers calculent les SHA-1 en
// parallèle. La mémoire est bornée (≈ 128 Mio au plus).
func (p *Plan) hashPieces(ctx context.Context, progress Progress) ([]byte, error) {
	pl := p.PieceLength
	count := int((p.Total + pl - 1) / pl)
	out := make([]byte, count*20)
	workers := runtime.NumCPU()
	if workers > 8 {
		workers = 8
	}
	nbuf := workers + 2
	if max := int((128 << 20) / pl); nbuf > max {
		nbuf = max
	}
	if nbuf < 2 {
		nbuf = 2
	}
	free := make(chan []byte, nbuf)
	for i := 0; i < nbuf; i++ {
		free <- make([]byte, pl)
	}
	jobs := make(chan job, nbuf)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				sum := sha1.Sum(j.buf[:j.n])
				copy(out[j.index*20:], sum[:])
				free <- j.buf
			}
		}()
	}

	var done atomic.Int64
	stopTick := make(chan struct{})
	if progress != nil {
		go func() {
			t := time.NewTicker(250 * time.Millisecond)
			defer t.Stop()
			for {
				select {
				case <-t.C:
					progress(done.Load(), p.Total)
				case <-stopTick:
					return
				}
			}
		}()
	}

	readErr := func() error {
		defer close(jobs)
		var (
			buf   []byte
			fill  int
			index int
		)
		next := func() error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case buf = <-free:
				fill = 0
				return nil
			}
		}
		if err := next(); err != nil {
			return err
		}
		for _, f := range p.Files {
			fh, err := os.Open(f.Abs)
			if err != nil {
				return err
			}
			if st, err := fh.Stat(); err != nil || st.Size() != f.Size {
				fh.Close()
				return ErrChanged
			}
			var read int64
			for read < f.Size {
				want := int64(len(buf) - fill)
				if left := f.Size - read; left < want {
					want = left
				}
				if limited() && want > limitChunk {
					want = limitChunk
				}
				if err := throttle(ctx, int(want)); err != nil {
					fh.Close()
					return err
				}
				n, err := io.ReadFull(fh, buf[fill:fill+int(want)])
				read += int64(n)
				fill += n
				done.Add(int64(n))
				if err != nil {
					fh.Close()
					if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
						return ErrChanged
					}
					return err
				}
				if fill == len(buf) {
					jobs <- job{index: index, buf: buf, n: fill}
					index++
					if err := next(); err != nil {
						fh.Close()
						return err
					}
				}
			}
			fh.Close()
		}
		if fill > 0 {
			jobs <- job{index: index, buf: buf, n: fill}
			index++
		}
		if index != count {
			return fmt.Errorf("hachage incohérent : %d pièces au lieu de %d", index, count)
		}
		return nil
	}()
	wg.Wait()
	close(stopTick)
	if readErr != nil {
		return nil, readErr
	}
	if progress != nil {
		progress(p.Total, p.Total)
	}
	return out, nil
}

// ---------- cache des .torrent fabriqués ----------

func (p *Plan) cacheKey() string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%d\n%s\n", p.Root, p.PieceLength, Source)
	for _, f := range p.Files {
		fmt.Fprintf(h, "%s\x00%d\x00%d\n", f.Rel, f.Size, f.ModTime.UnixNano())
	}
	return hex.EncodeToString(h.Sum(nil))
}

func cacheDir() (string, error) {
	d, err := config.Dir()
	if err != nil {
		return "", err
	}
	d = filepath.Join(d, "torrents")
	return d, os.MkdirAll(d, 0o700)
}

// cacheGet relit une entrée et la vérifie (nom, taille, contenu) avant de la servir.
func cacheGet(key string, p *Plan) ([]byte, bool) {
	d, err := cacheDir()
	if err != nil {
		return nil, false
	}
	b, err := os.ReadFile(filepath.Join(d, key+".torrent"))
	if err != nil {
		return nil, false
	}
	t, err := torrent.Parse(b)
	if err != nil || t.Name != p.Name || t.Total != p.Total || t.ContentHash != p.ContentHash {
		return nil, false
	}
	now := time.Now()
	_ = os.Chtimes(filepath.Join(d, key+".torrent"), now, now)
	return b, true
}

const cacheKeep = 500

func cachePut(key string, raw []byte) {
	d, err := cacheDir()
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(d, key+".torrent"), raw, 0o600)
	entries, err := os.ReadDir(d)
	if err != nil || len(entries) <= cacheKeep {
		return
	}
	type ent struct {
		name string
		at   time.Time
	}
	var list []ent
	for _, e := range entries {
		if info, err := e.Info(); err == nil {
			list = append(list, ent{e.Name(), info.ModTime()})
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].at.After(list[j].at) })
	for _, e := range list[cacheKeep:] {
		_ = os.Remove(filepath.Join(d, e.name))
	}
}

// HumanPiece écrit une taille de pièce (« 4 Mio »).
func HumanPiece(pl int64) string {
	if pl >= 1<<20 {
		return strconv.FormatInt(pl>>20, 10) + " Mio"
	}
	return strconv.FormatInt(pl>>10, 10) + " Kio"
}
