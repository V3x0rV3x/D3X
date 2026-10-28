// Package releases lit les dossiers où vivent les releases : contenu d'un
// dossier (sélecteur) et liste des releases d'un lot. Le même code tourne sur
// la machine du membre et, par l'agent, sur une seedbox.
package releases

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/V3x0rV3x/D3X/internal/audio"
	"github.com/V3x0rV3x/D3X/internal/maketorrent"
)

// FsEntry est une entrée d'un dossier.
type FsEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Dir  bool   `json:"dir"`
	Size int64  `json:"size,omitempty"` // fichiers seulement : un dossier n'est jamais mesuré ici
	Skip bool   `json:"skip,omitempty"` // écarté des releases (caché, aperçus de médiathèque…)
}

// Listing est le contenu d'un dossier. Parent vaut nil à la racine des points
// de départ, "" pour revenir à ces points de départ.
type Listing struct {
	Path    string    `json:"path"`
	Parent  *string   `json:"parent"`
	Entries []FsEntry `json:"entries"`
}

// Browse liste un dossier. Sans chemin : les points de départ (lecteurs sous
// Windows ; dossier perso, racine et montages ailleurs).
func Browse(path string, exclude []string) (*Listing, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return &Listing{Path: "", Parent: nil, Entries: roots()}, nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, errors.New("dossier introuvable")
	}
	if !st.IsDir() {
		abs = filepath.Dir(abs)
	}
	list, err := os.ReadDir(abs)
	if err != nil {
		return nil, errors.New("lecture refusée : " + err.Error())
	}
	out := make([]FsEntry, 0, len(list))
	for _, e := range list {
		full := filepath.Join(abs, e.Name())
		st, err := os.Stat(full) // suit les liens
		if err != nil {
			continue
		}
		ent := FsEntry{Name: e.Name(), Path: full, Dir: st.IsDir(), Skip: maketorrent.Excluded(e.Name(), exclude)}
		if !ent.Dir {
			ent.Size = st.Size()
		}
		out = append(out, ent)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	parent := ""
	if up := filepath.Dir(abs); up != abs {
		parent = up
	}
	return &Listing{Path: abs, Parent: &parent, Entries: out}, nil
}

func roots() []FsEntry {
	var out []FsEntry
	// Dans l'image Docker, le dossier personnel est /config (réglages, jeton) :
	// on part des médias montés.
	if home, err := os.UserHomeDir(); err == nil && os.Getenv("D3X_MANAGED") != "docker" {
		out = append(out, FsEntry{Name: "Dossier personnel", Path: home, Dir: true})
	}
	if runtime.GOOS == "windows" {
		for c := 'A'; c <= 'Z'; c++ {
			d := string(c) + `:\`
			if _, err := os.Stat(d); err == nil {
				out = append(out, FsEntry{Name: "Lecteur " + string(c) + ":", Path: d, Dir: true})
			}
		}
		return out
	}
	out = append(out, FsEntry{Name: "/", Path: "/", Dir: true})
	for _, m := range []string{"/mnt", "/media", "/Volumes", "/srv", "/data", "/home"} {
		if st, err := os.Stat(m); err == nil && st.IsDir() {
			out = append(out, FsEntry{Name: m, Path: m, Dir: true})
		}
	}
	return out
}

// Entry est une release d'un dossier de lot.
type Entry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Dir   bool   `json:"dir"`
	Size  int64  `json:"size"`
	Files int    `json:"files"`
	Video bool   `json:"video"`
	Audio bool   `json:"audio"`           // pistes audio, sans vidéo : un album
	Exec  bool   `json:"exec"`            // contient un exécutable : jamais publié seul
	Flat  bool   `json:"flat"`            // dossier sans fichier à sa racine (collection, cross-seeds)
	Error string `json:"error,omitempty"` // illisible
}

// VideoExt : extensions vidéo (MediaInfo, filtre « vidéos seulement »).
var VideoExt = map[string]bool{".mkv": true, ".mp4": true, ".avi": true, ".m2ts": true, ".ts": true, ".mov": true, ".wmv": true, ".webm": true, ".mpg": true, ".mpeg": true, ".m4v": true, ".iso": true, ".vob": true}

var execExt = map[string]bool{".exe": true, ".msi": true, ".bat": true, ".cmd": true, ".scr": true, ".com": true, ".ps1": true, ".vbs": true, ".js": true, ".jar": true, ".apk": true, ".dll": true, ".lnk": true, ".sh": true}

// List : chaque entrée d'un dossier (sous-dossier ou fichier) est une release.
func List(dir string, exclude []string) ([]Entry, error) {
	list, err := os.ReadDir(dir)
	if err != nil {
		return nil, errors.New("dossier illisible : " + err.Error())
	}
	out := make([]Entry, 0, len(list))
	for _, e := range list {
		if maketorrent.Excluded(e.Name(), exclude) {
			continue
		}
		out = append(out, entryOf(filepath.Join(dir, e.Name()), exclude))
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// Entries : les releases de chemins choisis (depuis le cross-seed, par
// exemple), dans l'ordre donné ; un chemin illisible garde son erreur.
func Entries(paths []string, exclude []string) []Entry {
	out := make([]Entry, 0, len(paths))
	for _, p := range paths {
		out = append(out, entryOf(p, exclude))
	}
	return out
}

func entryOf(full string, exclude []string) Entry {
	be := Entry{Name: filepath.Base(full), Path: full}
	if st, err := os.Stat(full); err == nil {
		be.Dir = st.IsDir()
	}
	p, err := maketorrent.Scan(full, exclude)
	if err != nil {
		be.Error = err.Error()
	} else {
		be.Path, be.Size, be.Files = p.Root, p.Total, len(p.Files)
		be.Flat = !p.Single
		var videoBytes, audioBytes int64
		for _, f := range p.Files {
			ext := strings.ToLower(filepath.Ext(f.Rel))
			if VideoExt[ext] {
				be.Video = true
				videoBytes += f.Size
			}
			if audio.IsAudio(f.Rel) {
				be.Audio = true
				audioBytes += f.Size
			}
			if execExt[ext] {
				be.Exec = true
			}
			if !strings.Contains(f.Rel, "/") {
				be.Flat = false
			}
		}
		// Vidéo OU album, jamais les deux : l'audio l'emporte quand il pèse
		// plus lourd que la vidéo (discographie avec un clip), comme dans Publier.
		if be.Audio && be.Video {
			if audio.Dominates(audioBytes, videoBytes) {
				be.Video = false
			} else {
				be.Audio = false
			}
		}
	}
	// Album en plusieurs disques (CD1, Disc 2…) : un seul album, pas une
	// collection.
	if be.Flat && be.Audio && discFolders(p) {
		be.Flat = false
	}
	return be
}

var discDir = regexp.MustCompile(`(?i)^(cd|dis[ck]|dvd|vinyl|face|side)[ ._-]*\d+`)

// discFolders : chaque sous-dossier de la release est un disque.
func discFolders(p *maketorrent.Plan) bool {
	if p == nil || len(p.Files) == 0 {
		return false
	}
	for _, f := range p.Files {
		rest := strings.TrimPrefix(f.Rel, p.Name+"/")
		top, _, ok := strings.Cut(rest, "/")
		if !ok {
			continue
		}
		if !discDir.MatchString(top) {
			return false
		}
	}
	return true
}
