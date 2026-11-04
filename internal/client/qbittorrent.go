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

type qbitClient struct{ c *qbit.Client }

func newQbit(o Options) (Client, error) {
	c, err := qbit.New(qbit.Options{URL: o.URL, User: o.User, Pass: o.Pass, Insecure: o.Insecure})
	if err != nil {
		return nil, err
	}
	return &qbitClient{c}, nil
}

func (q *qbitClient) Kind() string { return "qBittorrent" }

func (q *qbitClient) Version(ctx context.Context) (string, error) { return q.c.Version(ctx) }

func (q *qbitClient) List(ctx context.Context, hashes []string) ([]Torrent, error) {
	list, err := q.c.List(ctx, "", hashes)
	if err != nil {
		return nil, err
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
	b, err := q.c.Export(ctx, hash)
	if errors.Is(err, qbit.ErrNoExport) {
		return nil, ErrNoExport
	}
	return b, err
}

// Files : la liste de fichiers de qBittorrent (qBittorrent avant 4.5, sans export).
func (q *qbitClient) Files(ctx context.Context, hash string) ([]torrent.File, int64, error) {
	b, err := q.c.Raw(ctx, http.MethodGet, "/api/v2/torrents/files", url.Values{"hash": {hash}})
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

func (q *qbitClient) Add(ctx context.Context, name string, raw []byte, o AddOptions) error {
	err := q.c.Add(ctx, name, raw, qbit.AddOptions{SavePath: o.SavePath, Category: o.Category, Tags: o.Tag, Stopped: true})
	if errors.Is(err, qbit.ErrAlreadyThere) {
		return ErrAlreadyThere
	}
	return err
}

func (q *qbitClient) Recheck(ctx context.Context, h string) error { return q.c.Recheck(ctx, h) }
func (q *qbitClient) Start(ctx context.Context, h string) error   { return q.c.Start(ctx, h) }
func (q *qbitClient) Stop(ctx context.Context, h string) error    { return q.c.Stop(ctx, h) }
func (q *qbitClient) RemoveKeepFiles(ctx context.Context, h string) error {
	return q.c.RemoveKeepFiles(ctx, h)
}
