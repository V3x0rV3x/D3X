package batch

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/V3x0rV3x/D3X/internal/journal"
	"github.com/V3x0rV3x/D3X/internal/mediainfo"
	"github.com/V3x0rV3x/D3X/internal/musicbrainz"
	"github.com/V3x0rV3x/D3X/internal/publish"
	"github.com/V3x0rV3x/D3X/internal/releases"
	"github.com/V3x0rV3x/D3X/internal/source"
	"github.com/V3x0rV3x/D3X/internal/v3x"
)

// fakeDeps : chaque méthode a un champ-fonction ; non défini, elle rend une
// valeur neutre. events garde le journal.
type fakeDeps struct {
	mu      sync.Mutex
	events  []journal.Event
	entries []releases.Entry
	last    map[string]journal.Event
	holds   []time.Duration

	pubStart   func(path string) (*publish.Job, error)
	pubGet     func(id string) (*publish.Job, bool)
	pubPublish func(id string, fields map[string]string) error
	context    func() (*Context, error)
	fiche      func(jobID string, in FicheInput) (*Fiche, error)
	tmdb       func(q string) ([]Work, error)
	redo       func(name string) (string, error)
	requests   func(name string) (json.RawMessage, error)
	mb         func(jobID string) (*musicbrainz.Result, error)
	prepared   func(key string) (*Prepared, bool)
	series     func(title string) *Work
	mediainfo  func(ctx context.Context, req mediainfo.Request) (string, error)
	pubRead    func(id string, index int) ([]byte, int64, error)
	cancelled  []string
	dropped    []string
}

func (d *fakeDeps) Source(id string) (source.Source, error) { return source.Local{}, nil }
func (d *fakeDeps) Exclude() []string                       { return nil }
func (d *fakeDeps) Entries(_ context.Context, _ source.Source, paths []string) ([]releases.Entry, error) {
	var out []releases.Entry
	for _, p := range paths {
		for _, e := range d.entries {
			if e.Path == p {
				out = append(out, e)
			}
		}
	}
	return out, nil
}
func (d *fakeDeps) LastByPath() map[string]journal.Event { return d.last }
func (d *fakeDeps) PubStart(_ source.Source, path string) (*publish.Job, error) {
	if d.pubStart == nil {
		return nil, errors.New("pas de publication dans ce test")
	}
	return d.pubStart(path)
}
func (d *fakeDeps) PubGet(id string) (*publish.Job, bool) {
	if d.pubGet == nil {
		return nil, false
	}
	return d.pubGet(id)
}
func (d *fakeDeps) PubCancel(id string) {
	d.mu.Lock()
	d.cancelled = append(d.cancelled, id)
	d.mu.Unlock()
}
func (d *fakeDeps) PubPublish(id string, fields map[string]string) error {
	if d.pubPublish == nil {
		return errors.New("pas de publication dans ce test")
	}
	return d.pubPublish(id, fields)
}
func (d *fakeDeps) PubRead(_ context.Context, id string, i int, off, n int64) ([]byte, int64, error) {
	if d.pubRead == nil {
		return nil, 0, errors.New("pas de lecture")
	}
	return d.pubRead(id, i)
}
func (d *fakeDeps) PubFilePath(string, int) (string, bool) { return "/x/f.mkv", true }
func (d *fakeDeps) Context(ctx context.Context) (*Context, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if d.context == nil {
		return &Context{}, nil
	}
	return d.context()
}
func (d *fakeDeps) Fiche(ctx context.Context, jobID string, in FicheInput) (*Fiche, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if d.fiche == nil {
		return &Fiche{}, nil
	}
	return d.fiche(jobID, in)
}

// Comme un vrai appel HTTP : un contexte coupé pendant l'appel le fait échouer.
func (d *fakeDeps) TMDBSearch(ctx context.Context, q string) ([]Work, error) {
	if d.tmdb == nil {
		return nil, nil
	}
	res, err := d.tmdb(q)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return res, err
}
func (d *fakeDeps) Redo(_ context.Context, name string, _ int64, _, _ string) (string, error) {
	if d.redo == nil {
		return "", nil
	}
	return d.redo(name)
}
func (d *fakeDeps) RequestsFor(_ context.Context, name string) (json.RawMessage, error) {
	if d.requests == nil {
		return nil, nil
	}
	return d.requests(name)
}
func (d *fakeDeps) MBDecide(_ context.Context, jobID string) (*musicbrainz.Result, error) {
	if d.mb == nil {
		return &Result0, nil
	}
	return d.mb(jobID)
}
func (d *fakeDeps) Prepared(key string) (*Prepared, bool) {
	if d.prepared == nil {
		return nil, false
	}
	return d.prepared(key)
}
func (d *fakeDeps) DropPrepared(key string) {
	d.mu.Lock()
	d.dropped = append(d.dropped, key)
	d.mu.Unlock()
}
func (d *fakeDeps) RememberedSeries(title string) *Work {
	if d.series == nil {
		return nil
	}
	return d.series(title)
}
func (d *fakeDeps) Journal(e journal.Event) {
	d.mu.Lock()
	d.events = append(d.events, e)
	d.mu.Unlock()
}
func (d *fakeDeps) HoldPublished(x time.Duration) {
	d.mu.Lock()
	d.holds = append(d.holds, x)
	d.mu.Unlock()
}
func (d *fakeDeps) MediaInfo(ctx context.Context, req mediainfo.Request) (string, error) {
	if d.mediainfo == nil {
		return "", nil
	}
	return d.mediainfo(ctx, req)
}

// Result0 : « album introuvable ».
var Result0 = musicbrainz.Result{Reason: "album introuvable sur MusicBrainz"}

func (d *fakeDeps) kinds() map[string]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[string]string{}
	for _, e := range d.events {
		out[e.Path] = e.Kind
	}
	return out
}

func waitDone(t *testing.T, e *Engine) *Run {
	t.Helper()
	for i := 0; i < 500; i++ {
		if r := e.State(); r != nil && r.State == StDone {
			return r
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("le lot ne finit pas")
	return nil
}

func entryByPath(r *Run, p string) *Entry {
	for _, e := range r.Entries {
		if e.Path == p {
			return e
		}
	}
	return nil
}

func TestEngineSimpleDecisions(t *testing.T) {
	d := &fakeDeps{entries: []releases.Entry{
		{Name: "Logiciel", Path: "/r/Logiciel", Exec: true, Size: 10},
		{Name: "Livre.epub", Path: "/r/Livre.epub", Size: 10},
		{Name: "Collection", Path: "/r/Collection", Video: true, Flat: true, Size: 10},
		{Name: "Enorme", Path: "/r/Enorme", Video: true, Size: 400 << 30},
		{Name: "Deja", Path: "/r/Deja", Video: true, Size: 10},
	}, last: map[string]journal.Event{"/r/Deja": {Kind: "batch_ignored", Detail: "écartée à la main"}}}
	e := New(d)
	r, err := e.Start(Spec{Dir: "/r", Source: "local", Paths: []string{"/r/Logiciel", "/r/Livre.epub", "/r/Collection", "/r/Enorme", "/r/Deja", "/r/Absente"}, Options: Options{Sim: true, Video: true, Cap: 300}})
	if err != nil {
		t.Fatal(err)
	}
	if r.State != StRunning || r.Total != 4 {
		t.Fatalf("démarrage : %+v", r)
	}
	r = waitDone(t, e)
	want := map[string][2]string{
		"/r/Logiciel":   {"review", "contient un exécutable : jamais publié par un lot"},
		"/r/Livre.epub": {"review", "ni vidéo ni album (option « Vidéos et albums seulement »)"},
		"/r/Collection": {"review", "dossier sans fichier à sa racine (collection, plusieurs releases ?)"},
		"/r/Enorme":     {"review", "au-delà du plafond de 300 Gio : pas hachée"},
		"/r/Deja":       {"ignored", "écartée à la main"},
	}
	for p, w := range want {
		en := entryByPath(r, p)
		if en == nil || en.State != w[0] || en.Detail != w[1] {
			t.Errorf("%s : %+v, attendu %v", p, en, w)
		}
	}
	if entryByPath(r, "/r/Absente") != nil {
		t.Error("une release absente du disque ne doit pas entrer dans le lot")
	}
	k := d.kinds()
	if k["/r/Logiciel"] != "batch_review" || k["/r/Deja"] != "" {
		t.Errorf("journal : %v", k)
	}
	if r.Done != 4 {
		t.Errorf("done = %d (l'entrée ignorée n'est pas traitée)", r.Done)
	}
}

func TestEngineOneAtATime(t *testing.T) {
	d := &fakeDeps{entries: []releases.Entry{{Name: "A", Path: "/r/A", Exec: true}}}
	e := New(d)
	if _, err := e.Start(Spec{Dir: "/r", Source: "local", Paths: []string{"/r/A"}}); err != nil {
		t.Fatal(err)
	}
	// Le second démarrage pendant le premier est refusé (ou le premier a déjà fini : alors accepté).
	if _, err := e.Start(Spec{Dir: "/r", Source: "local", Paths: []string{"/r/A"}}); err != nil && !errors.Is(err, ErrRunning) {
		t.Fatalf("erreur inattendue : %v", err)
	}
	waitDone(t, e)
	if _, err := e.Start(Spec{Dir: "/r", Source: "local", Paths: []string{"/r/A"}}); err != nil {
		t.Fatalf("après la fin, un nouveau lot doit démarrer : %v", err)
	}
	waitDone(t, e)
}

func TestEngineCapsAndStop(t *testing.T) {
	// Deux releases « publiées » par un faux chemin de publication : le plafond
	// en nombre arrête la seconde, et Stop arrête avant la troisième.
	jobs := map[string]*publish.Job{}
	var mu sync.Mutex
	d := &fakeDeps{entries: []releases.Entry{
		{Name: "A", Path: "/r/A", Video: true, Size: 5 << 30},
		{Name: "B", Path: "/r/B", Video: true, Size: 5 << 30},
		{Name: "C", Path: "/r/C", Video: true, Size: 5 << 30},
	}}
	d.pubStart = func(path string) (*publish.Job, error) {
		mu.Lock()
		defer mu.Unlock()
		j := &publish.Job{ID: "job-" + path, Path: path, Name: path[3:], Single: false, Total: 5 << 30, MainFile: 0, State: publish.StReady, Files: []publish.File{{Rel: "f.mkv", Size: 5 << 30, Video: true}}}
		jobs[j.ID] = j
		return j, nil
	}
	d.pubGet = func(id string) (*publish.Job, bool) {
		mu.Lock()
		defer mu.Unlock()
		j, ok := jobs[id]
		if !ok {
			return nil, false
		}
		c := *j
		return &c, true
	}
	d.pubPublish = func(id string, fields map[string]string) error {
		mu.Lock()
		defer mu.Unlock()
		jobs[id].State = publish.StDone
		jobs[id].Published = &v3x.Uploaded{ID: "fiche-" + id, Status: "active"}
		return nil
	}
	d.context = func() (*Context, error) {
		return &Context{Categories: []Category{{ID: "1", Name: "Films", Slug: "films", Children: []Category{{ID: "11", Name: "Film", Slug: "films-videos-film"}}}}}, nil
	}
	d.fiche = func(jobID string, in FicheInput) (*Fiche, error) {
		f := &Fiche{Title: "Titre", TmdbQuery: "Titre 2020", Presentation: "p", NFO: "n", Language: "VO"}
		f.Analysis.Title = "Titre"
		f.Category = &struct {
			ID   Text   `json:"id"`
			Slug string `json:"slug"`
		}{ID: "11", Slug: "films-videos-film"}
		return f, nil
	}
	d.tmdb = func(q string) ([]Work, error) {
		return []Work{{Type: "movie", ID: "7", Title: "Titre", Year: "2020"}}, nil
	}
	d.mediainfo = func(context.Context, mediainfo.Request) (string, error) { return "General\nFormat : Matroska", nil }
	e := New(d)
	e.pause = time.Millisecond // la pause de 7 s entre deux publications, raccourcie pour le test
	if _, err := e.Start(Spec{Dir: "/r", Source: "local", Paths: []string{"/r/A", "/r/B", "/r/C"}, Options: Options{Max: 1}}); err != nil {
		t.Fatal(err)
	}
	r := waitDone(t, e)
	if a := entryByPath(r, "/r/A"); a.State != "published" || a.Detail != "publiée · Films ▸ Film · Titre (2020)" {
		t.Fatalf("A : %+v", a)
	}
	if b := entryByPath(r, "/r/B"); b.State != "pending" || b.Detail != "plafond de publications atteint" {
		t.Fatalf("B : %+v", b)
	}
	if r.Published != 1 || r.PublishedBytes != 5<<30 {
		t.Fatalf("compteurs : %+v", r)
	}

	// Plafond en volume : B passe (5 Gio), C attend le lot suivant (10 > 8).
	e2 := New(d)
	e2.pause = time.Millisecond
	if _, err := e2.Start(Spec{Dir: "/r", Source: "local", Paths: []string{"/r/B", "/r/C"}, Options: Options{MaxGiB: 8}}); err != nil {
		t.Fatal(err)
	}
	r = waitDone(t, e2)
	if c := entryByPath(r, "/r/C"); c.State != "pending" || c.Detail != "attend le lot suivant : dépasserait les 8 Gio publiés de ce lot" {
		t.Fatalf("C : %+v", c)
	}

	// Arrêt : pendant la pause après A, Stop ; B n'est pas traitée.
	e3 := New(d)
	e3.pause = 200 * time.Millisecond
	if _, err := e3.Start(Spec{Dir: "/r", Source: "local", Paths: []string{"/r/A", "/r/B"}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		if r := e3.State(); r.Published == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	e3.Stop()
	r = waitDone(t, e3)
	if !r.Stopped || entryByPath(r, "/r/B").State != "pending" {
		t.Fatalf("arrêt : %+v", r)
	}
}

func TestEngineSeedLater(t *testing.T) {
	d := &fakeDeps{entries: []releases.Entry{{Name: "A", Path: "/r/A", Exec: true}}}
	e := New(d)
	if _, err := e.Start(Spec{Dir: "/r", Source: "local", Paths: []string{"/r/A"}, Options: Options{SeedLater: true}}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, e)
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.holds) < 2 || d.holds[0] != 5*time.Minute || d.holds[len(d.holds)-1] != 0 {
		t.Fatalf("bail : %v", d.holds)
	}
}
