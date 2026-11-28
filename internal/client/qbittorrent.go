package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/V3x0rV3x/D3X/internal/qbit"
	"github.com/V3x0rV3x/D3X/internal/torrent"
)

type qbitClient struct {
	c  *qbit.Client
	hy hybridIDs // empreinte v1 → identifiant du client des torrents hybrides
}

func newQbit(o Options) (Client, error) {
	c, err := qbit.New(qbit.Options{URL: o.URL, User: o.User, Pass: o.Pass, Insecure: o.Insecure})
	if err != nil {
		return nil, err
	}
	return &qbitClient{c: c}, nil
}

func (q *qbitClient) Kind() string { return "qBittorrent" }

func (q *qbitClient) Version(ctx context.Context) (string, error) { return q.c.Version(ctx) }

func (q *qbitClient) List(ctx context.Context, hashes []string) ([]Torrent, error) {
	ids := make([]string, len(hashes))
	for i, h := range hashes {
		ids[i] = q.idOf(h)
	}
	list, err := q.c.List(ctx, "", ids)
	if err != nil {
		return nil, err
	}
	// Une empreinte demandée et absente peut être la v1 d'un hybride que le
	// client range sous sa v2 (voir qbit_hybrid.go) : on la cherche encore une
	// fois sous son identifiant.
	if len(hashes) > 0 {
		seen := map[string]bool{}
		for _, t := range list {
			for _, h := range t.Hashes() {
				seen[h] = true
			}
		}
		var extra []string
		for _, h := range hashes {
			if seen[strings.ToLower(h)] {
				continue
			}
			if id, ok := q.refreshIDs(ctx, h); ok && !seen[id] {
				extra = append(extra, id)
				seen[id] = true
			}
		}
		if len(extra) > 0 {
			if more, err := q.c.List(ctx, "", extra); err == nil {
				list = append(list, more...)
			}
		}
	}
	out := make([]Torrent, 0, len(list))
	for _, t := range list {
		out = append(out, Torrent{
			Hash: strings.ToLower(t.Hash), Hashes: t.Hashes(), Name: t.Name, ContentPath: t.ContentPath, SavePath: t.SavePath,
			Size: t.Size, TotalSize: t.TotalSize, Progress: t.Progress,
			Checking: t.Checking(), Downloading: t.Downloading(), Stopped: t.Stopped(), Trackers: t.TrackerHosts(),
			Category: t.Category,
			Tags:     tagList(strings.Split(t.Tags, ",")),
		})
	}
	return out, nil
}

func (q *qbitClient) Export(ctx context.Context, hash string) ([]byte, error) {
	b, err := q.c.Export(ctx, q.idOf(hash))
	if errors.Is(err, qbit.ErrNoExport) {
		return nil, ErrNoExport
	}
	return b, err
}

// Files : la liste de fichiers de qBittorrent (qBittorrent avant 4.5, sans export).
func (q *qbitClient) Files(ctx context.Context, hash string) ([]torrent.File, int64, error) {
	id := q.idOf(hash)
	b, err := q.c.Raw(ctx, http.MethodGet, "/api/v2/torrents/files", url.Values{"hash": {id}})
	if isNotFound(err) {
		// Peut-être un hybride rangé sous sa v2 (voir qbit_hybrid.go).
		if nid, ok := q.refreshIDs(ctx, hash); ok && nid != id {
			b, err = q.c.Raw(ctx, http.MethodGet, "/api/v2/torrents/files", url.Values{"hash": {nid}})
		}
	}
	if err != nil {
		return nil, 0, err
	}
	var files []struct {
		Index *int   `json:"index"`
		Name  string `json:"name"`
		Size  int64  `json:"size"`
	}
	if err := json.Unmarshal(b, &files); err != nil {
		return nil, 0, err
	}
	out := make([]torrent.File, 0, len(files))
	var total int64
	for i, f := range files {
		idx := i
		if f.Index != nil {
			idx = *f.Index
		}
		out = append(out, torrent.File{Path: strings.ReplaceAll(f.Name, `\`, "/"), Size: f.Size, Index: idx})
		total += f.Size
	}
	return out, total, nil
}

func (q *qbitClient) CanStripRoot() bool { return true }

func (q *qbitClient) Add(ctx context.Context, name string, raw []byte, o AddOptions) error {
	err := q.c.Add(ctx, name, raw, qbit.AddOptions{SavePath: o.SavePath, Category: o.Category, Tags: o.Tag, Stopped: true, NoSubfolder: o.NoRoot})
	if errors.Is(err, qbit.ErrAlreadyThere) {
		return ErrAlreadyThere
	}
	if err == nil {
		q.justAdded()
	}
	return err
}

// AutoManage passe en gestion automatique un torrent rangé dans le dossier
// même de sa catégorie (YataReborn, 21/11/26) : il suivra la catégorie, comme
// un téléchargement ordinaire. Rangé ailleurs, il reste en gestion manuelle :
// la gestion automatique le déplacerait vers le dossier de la catégorie.
func (q *qbitClient) AutoManage(ctx context.Context, t Torrent) (bool, error) {
	if t.Category == "" || t.SavePath == "" {
		return false, nil
	}
	dir, err := q.c.CategoryPath(ctx, t.Category)
	if err != nil || dir == "" || !SamePath(dir, t.SavePath) {
		return false, err
	}
	return true, q.c.SetAutoManagement(ctx, true, t.Hash)
}

// SamePath : deux chemins du client désignent-ils le même dossier ? « / » et
// « \ » se valent, le séparateur final ne compte pas, et un chemin Windows se
// compare sans la casse.
func SamePath(a, b string) bool {
	n := func(p string) string {
		return strings.TrimRight(strings.ReplaceAll(strings.TrimSpace(p), `\`, "/"), "/")
	}
	na, nb := n(a), n(b)
	if na == "" || nb == "" {
		return false
	}
	win := strings.Contains(a, `\`) || (len(a) >= 2 && a[1] == ':')
	if win {
		return strings.EqualFold(na, nb)
	}
	return na == nb
}

// Ces appels ne répondent pas d'erreur pour une empreinte inconnue : on donne
// d'emblée l'identifiant du client (voir qbit_hybrid.go).
func (q *qbitClient) Recheck(ctx context.Context, h string) error { return q.c.Recheck(ctx, q.idOf(h)) }
func (q *qbitClient) Start(ctx context.Context, h string) error   { return q.c.Start(ctx, q.idOf(h)) }
func (q *qbitClient) Stop(ctx context.Context, h string) error    { return q.c.Stop(ctx, q.idOf(h)) }
func (q *qbitClient) RemoveKeepFiles(ctx context.Context, h string) error {
	return q.c.RemoveKeepFiles(ctx, q.idOf(h))
}
