package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"time"

	"github.com/V3x0rV3x/D3X/internal/batch"
	"github.com/V3x0rV3x/D3X/internal/journal"
	"github.com/V3x0rV3x/D3X/internal/mediainfo"
	"github.com/V3x0rV3x/D3X/internal/musicbrainz"
	"github.com/V3x0rV3x/D3X/internal/publish"
	"github.com/V3x0rV3x/D3X/internal/releases"
	"github.com/V3x0rV3x/D3X/internal/source"
	"github.com/V3x0rV3x/D3X/internal/v3x"
)

// batchDeps : ce que le lot demande au serveur (batch.Deps), sans HTTP. Le
// moteur passe context.Background() : chaque appel au site, à la seedbox ou
// à MusicBrainz porte ici son propre délai.
type batchDeps struct{ s *Server }

func (d batchDeps) Source(id string) (source.Source, error) { return d.s.source(id) }

func (d batchDeps) Exclude() []string {
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	return d.s.cfg.ExcludeList()
}

func (d batchDeps) Entries(ctx context.Context, src source.Source, paths []string) ([]releases.Entry, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	return src.Entries(ctx, paths, d.Exclude())
}

func (d batchDeps) LastByPath() map[string]journal.Event { return journal.LastByPath() }

func (d batchDeps) PubStart(src source.Source, path string) (*publish.Job, error) {
	return d.s.pub.Start(src, path)
}
func (d batchDeps) PubGet(id string) (*publish.Job, bool) { return d.s.pub.Get(id) }
func (d batchDeps) PubCancel(id string)                   { d.s.pub.Cancel(id) }
func (d batchDeps) PubPublish(id string, fields map[string]string) error {
	return d.s.pub.Publish(id, publishFields(fields))
}
func (d batchDeps) PubRead(ctx context.Context, id string, index int, off, n int64) ([]byte, int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return d.s.pub.ReadFile(ctx, id, index, off, n)
}
func (d batchDeps) PubFilePath(id string, index int) (string, bool) {
	return d.s.pub.FilePath(id, index)
}

func (d batchDeps) Context(ctx context.Context) (*batch.Context, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	raw, err := d.s.publishContext(ctx, false)
	if err != nil {
		return nil, err
	}
	var c batch.Context
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, errors.New("contexte de publication illisible")
	}
	return &c, nil
}

func (d batchDeps) Fiche(ctx context.Context, jobID string, in batch.FicheInput) (*batch.Fiche, error) {
	dl, ok := d.s.pub.Download(jobID)
	if !ok {
		return nil, errors.New("publication inconnue")
	}
	v, err := d.s.v3xClient()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	raw, err := v.Fiche(ctx, v3x.FicheRequest{ReleaseName: in.ReleaseName, MediaInfo: in.MediaInfo, TmdbRef: in.TmdbRef, MbRef: in.MbRef, Download: dl, CategorySlug: in.CategorySlug})
	if err != nil {
		return nil, err
	}
	var f batch.Fiche
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, errors.New("fiche illisible")
	}
	return &f, nil
}

func (d batchDeps) TMDBSearch(ctx context.Context, q string) ([]batch.Work, error) {
	v, err := d.s.v3xClient()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	raw, err := v.TMDB(ctx, "search", url.Values{"q": {q}})
	if err != nil {
		return nil, err
	}
	var out struct {
		Results []batch.Work `json:"results"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, errors.New("réponse TMDB illisible")
	}
	// Films et séries seulement, comme la page.
	var works []batch.Work
	for _, w := range out.Results {
		if w.Type == "movie" || w.Type == "tv" {
			works = append(works, w)
		}
	}
	return works, nil
}

func (d batchDeps) Redo(ctx context.Context, name string, size int64, tmdbRef, categorySlug string) (string, error) {
	v, err := d.s.v3xClient()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	q := url.Values{"name": {name}, "size": {strconv.FormatInt(size, 10)}}
	if tmdbRef != "" {
		q.Set("tmdb", tmdbRef)
	}
	if categorySlug != "" {
		q.Set("category", categorySlug)
	}
	raw, err := v.Versions(ctx, q)
	if err != nil {
		return "", err
	}
	var out struct {
		Redo []struct {
			Name string `json:"name"`
		} `json:"redo"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", errors.New("versions illisibles")
	}
	if len(out.Redo) == 0 {
		return "", nil
	}
	return out.Redo[0].Name, nil
}

func (d batchDeps) RequestsFor(ctx context.Context, name string) (json.RawMessage, error) {
	v, err := d.s.v3xClient()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	reqs, err := d.s.requests(ctx, v)
	if err != nil {
		return nil, err
	}
	hits := matchRequest(name, reqs)
	if len(hits) == 0 {
		return nil, nil
	}
	return json.Marshal(hits)
}

func (d batchDeps) MBDecide(ctx context.Context, jobID string) (*musicbrainz.Result, error) {
	j, ok := d.s.pub.Get(jobID)
	if !ok {
		return nil, errors.New("publication inconnue")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	return d.s.mb.Decide(ctx, localOf(j.Name, j.Audio))
}

func (d batchDeps) Prepared(key string) (*batch.Prepared, bool) {
	preparedMu.Lock()
	defer preparedMu.Unlock()
	p, ok := loadPrepared()[key]
	if !ok {
		return nil, false
	}
	return &batch.Prepared{Name: p.Name, Summary: p.Summary, Fields: p.Fields}, true
}

func (d batchDeps) DropPrepared(key string)                   { dropPrepared(key) }
func (d batchDeps) RememberedSeries(title string) *batch.Work { return rememberedSeries(title) }
func (d batchDeps) Journal(e journal.Event)                   { journal.Add(e) }
func (d batchDeps) HoldPublished(x time.Duration)             { d.s.engine.HoldPublished(x) }

// MediaInfo : le binaire (5 min au plus), ou une page. L'attente d'une page
// n'a pas de délai : le lot est en pause jusqu'à sa réponse ou à l'arrêt
// (le contexte du lot, coupé par Stop).
func (d batchDeps) MediaInfo(ctx context.Context, req mediainfo.Request) (string, error) {
	return d.s.mi.Analyze(ctx, req)
}

// publishFields : les mêmes champs acceptés, et les mêmes ajoutés, que la
// route /ui/pub/{id}/publish (ceux de la page d'upload du site, rien d'autre).
func publishFields(in map[string]string) map[string]string {
	allowed := map[string]bool{"categoryId": true, "description": true, "descriptionFormat": true, "title": true, "releaseName": true, "nfo": true, "language": true, "season": true, "tmdbUrl": true, "noTmdb": true, "anonymous": true, "exclusive": true, "teamRelease": true, "posterUrl": true, "creator": true, "virusTotalUrl": true}
	fields := map[string]string{"rightsDeclared": "true", "sessionId": "d3x-" + time.Now().UTC().Format("20060102")}
	for k, v := range in {
		if allowed[k] {
			fields[k] = v
		}
	}
	return fields
}

var _ batch.Deps = batchDeps{}
