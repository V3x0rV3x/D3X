package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/V3x0rV3x/D3X/internal/qbit"
)

// Rename : un fichier d'un torrent, son chemin d'origine (From, celui du
// .torrent) et le chemin voulu dans le client (To). Index : son rang dans le
// .torrent. Une liste de renommages peut contenir des fichiers inchangés
// (From == To) : certains clients en ont besoin pour renommer un dossier.
type Rename struct {
	Index int
	From  string
	To    string
}

// ErrNoRename : le client ne sait pas renommer les fichiers d'un torrent.
var ErrNoRename = errors.New("ce client ne sait pas renommer les fichiers d'un torrent")

// Les renommages se font sur un torrent ajouté à l'arrêt, avant toute
// vérification, quand aucun fichier n'existe encore sous l'ancien nom : le
// client ne change alors que ses propres noms, rien ne bouge sur le disque.
// C'est à l'appelant de s'en assurer (voir crossseed).

// ---------- qBittorrent ----------

func (q *qbitClient) CanRename() bool { return true }

func (q *qbitClient) RenameFiles(ctx context.Context, hash string, renames []Rename) error {
	hash = q.idOf(hash) // hybride rangé sous sa v2 (voir qbit_hybrid.go)
	for _, r := range renames {
		if r.From == r.To {
			continue
		}
		if _, err := q.c.Raw(ctx, http.MethodPost, "/api/v2/torrents/renameFile", url.Values{"hash": {hash}, "oldPath": {r.From}, "newPath": {r.To}}); err != nil {
			return errors.New("renommage refusé par qBittorrent (" + r.From + ") : " + err.Error())
		}
	}
	return nil
}

// Lookup : qBittorrent 5 liste un dossier de son disque (complétion des
// chemins de son interface web). On remonte jusqu'au premier dossier
// listable : si le chemin cherché n'y figure pas, il n'existe pas.
func (q *qbitClient) Lookup(ctx context.Context, p string) (bool, bool, error) {
	cur := strings.TrimRight(p, `/\`)
	for depth := 0; depth < 64; depth++ {
		parent, name := splitClientPath(cur)
		if parent == "" || parent == cur {
			return false, false, nil
		}
		b, err := q.c.Raw(ctx, http.MethodGet, "/api/v2/app/getDirectoryContent", url.Values{"dirPath": {parent}, "mode": {"all"}})
		if err == nil {
			var entries []string
			if json.Unmarshal(b, &entries) != nil {
				return false, false, nil
			}
			found := false
			for _, e := range entries {
				_, n := splitClientPath(strings.TrimRight(e, `/\`))
				if strings.EqualFold(n, name) {
					found = true
					break
				}
			}
			if !found {
				return false, true, nil
			}
			if cur == strings.TrimRight(p, `/\`) {
				return true, true, nil
			}
			// Un dossier existe mais n'a pas pu être listé : on ne sait pas.
			return false, false, nil
		}
		var he *qbit.HTTPError
		if !errors.As(err, &he) {
			return false, false, err
		}
		cur = parent
	}
	return false, false, nil
}

// splitClientPath coupe un chemin du client (« / » ou « \ ») en dossier et nom.
func splitClientPath(p string) (string, string) {
	i := strings.LastIndexAny(p, `/\`)
	if i < 0 {
		return "", p
	}
	if i == 0 {
		return p[:1], p[1:]
	}
	if i == 2 && p[1] == ':' {
		return p[:3], p[3:]
	}
	return p[:i], p[i+1:]
}

// ---------- Deluge ----------

func (d *deluge) CanRename() bool { return true }

func (d *deluge) RenameFiles(ctx context.Context, hash string, renames []Rename) error {
	var list [][]interface{}
	for _, r := range renames {
		if r.From != r.To {
			list = append(list, []interface{}{r.Index, r.To})
		}
	}
	if len(list) == 0 {
		return nil
	}
	return d.call(ctx, "core.rename_files", []interface{}{hash, list}, nil)
}

// Lookup : taille d'un chemin vue par le démon Deluge (-1 : absent).
func (d *deluge) Lookup(ctx context.Context, p string) (bool, bool, error) {
	var size int64
	if err := d.call(ctx, "core.get_path_size", []interface{}{p}, &size); err != nil {
		return false, false, nil
	}
	return size >= 0, true, nil
}

// ---------- Transmission ----------

func (t *transmission) CanRename() bool { return true }

// RenameFiles : Transmission renomme un élément à la fois (fichier ou
// dossier) et jamais d'un dossier à l'autre. Les fichiers d'abord, puis les
// dossiers du plus profond à la racine.
func (t *transmission) RenameFiles(ctx context.Context, hash string, renames []Rename) error {
	ops, err := componentRenames(renames)
	if err != nil {
		return err
	}
	for _, op := range ops {
		if err := t.ids(ctx, "torrent-rename-path", hash, map[string]interface{}{"path": op[0], "name": op[1]}); err != nil {
			return errors.New("renommage refusé par Transmission (" + op[0] + ") : " + err.Error())
		}
	}
	return nil
}

// Lookup : Transmission ne dit pas ce qu'il y a sur son disque.
func (t *transmission) Lookup(context.Context, string) (bool, bool, error) { return false, false, nil }

// componentRenames traduit des renommages de chemins complets en renommages
// d'un seul élément ({chemin d'origine, nouveau nom}), pour les clients qui
// ne savent pas déplacer un fichier d'un dossier à l'autre.
func componentRenames(renames []Rename) ([][2]string, error) {
	type op struct {
		path, name string
		depth      int
	}
	dirs := map[string]string{}
	var ops []op
	for _, r := range renames {
		a, b := strings.Split(r.From, "/"), strings.Split(r.To, "/")
		if len(a) != len(b) {
			return nil, errors.New("les dossiers de ta copie ne sont pas rangés comme ceux de la release V3X : ce client ne sait pas déplacer un fichier d'un dossier à l'autre")
		}
		for k := 0; k < len(a)-1; k++ {
			key := strings.Join(a[:k+1], "/")
			if v, ok := dirs[key]; ok && v != b[k] {
				return nil, errors.New("un même dossier de la release V3X correspond à deux dossiers de ta copie")
			}
			dirs[key] = b[k]
		}
		if a[len(a)-1] != b[len(b)-1] {
			ops = append(ops, op{r.From, b[len(b)-1], len(a)})
		}
	}
	for key, name := range dirs {
		parts := strings.Split(key, "/")
		if parts[len(parts)-1] != name {
			ops = append(ops, op{key, name, len(parts)})
		}
	}
	sort.SliceStable(ops, func(i, j int) bool {
		if ops[i].depth != ops[j].depth {
			return ops[i].depth > ops[j].depth
		}
		return ops[i].path < ops[j].path
	})
	out := make([][2]string, 0, len(ops))
	for _, o := range ops {
		out = append(out, [2]string{o.path, o.name})
	}
	return out, nil
}

// ---------- rTorrent ----------

func (r *rtorrent) CanRename() bool { return false }

func (r *rtorrent) RenameFiles(context.Context, string, []Rename) error { return ErrNoRename }

func (r *rtorrent) Lookup(context.Context, string) (bool, bool, error) { return false, false, nil }
