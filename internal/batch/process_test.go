package batch

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/V3x0rV3x/D3X/internal/audio"
	"github.com/V3x0rV3x/D3X/internal/crossseed"
	"github.com/V3x0rV3x/D3X/internal/journal"
	"github.com/V3x0rV3x/D3X/internal/mediainfo"
	"github.com/V3x0rV3x/D3X/internal/musicbrainz"
	"github.com/V3x0rV3x/D3X/internal/publish"
	"github.com/V3x0rV3x/D3X/internal/releases"
	"github.com/V3x0rV3x/D3X/internal/v3x"
)

// pubFake : de fausses publications en mémoire, branchées sur un fakeDeps.
type pubFake struct {
	mu        sync.Mutex
	jobs      map[string]*publish.Job
	fields    map[string]map[string]string
	starts    int
	make      func(path string) *publish.Job // le job au démarrage (vidéo prête par défaut)
	published func(j *publish.Job)           // après l'envoi (StDone, active par défaut)
	get       func(j *publish.Job)           // à chaque lecture, sous verrou
	inputs    []FicheInput
}

func videoJob(path string) *publish.Job {
	return &publish.Job{ID: "job-" + path, Path: path, Name: path[strings.LastIndex(path, "/")+1:], Total: 5 << 30, MainFile: 0, State: publish.StReady, Files: []publish.File{{Rel: "f.mkv", Size: 5 << 30, Video: true}}}
}

// happy : une vidéo dont tout est certain (catégorie, œuvre, MediaInfo).
func happy(entries ...releases.Entry) (*fakeDeps, *pubFake) {
	d := &fakeDeps{entries: entries}
	p := &pubFake{jobs: map[string]*publish.Job{}, fields: map[string]map[string]string{}, make: videoJob}
	d.pubStart = func(path string) (*publish.Job, error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.starts++
		j := p.make(path)
		p.jobs[j.ID] = j
		c := *j
		return &c, nil
	}
	d.pubGet = func(id string) (*publish.Job, bool) {
		p.mu.Lock()
		defer p.mu.Unlock()
		j, ok := p.jobs[id]
		if !ok {
			return nil, false
		}
		if p.get != nil {
			p.get(j)
		}
		c := *j
		return &c, true
	}
	d.pubPublish = func(id string, fields map[string]string) error {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.fields[id] = fields
		j := p.jobs[id]
		j.State = publish.StDone
		j.Published = &v3x.Uploaded{ID: "fiche-" + id, Status: "active"}
		if p.published != nil {
			p.published(j)
		}
		return nil
	}
	d.context = func() (*Context, error) {
		return &Context{Categories: []Category{
			{ID: "1", Name: "Films", Slug: "films", Children: []Category{{ID: "11", Name: "Film", Slug: "films-videos-film"}}},
			{ID: "5", Name: "Audio", Slug: "audio", Children: []Category{{ID: "51", Name: "Musique", Slug: "audio-musique"}}},
		}}, nil
	}
	d.fiche = func(jobID string, in FicheInput) (*Fiche, error) {
		p.mu.Lock()
		p.inputs = append(p.inputs, in)
		p.mu.Unlock()
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
	return d, p
}

func quick(d Deps) *Engine {
	e := New(d)
	e.pause, e.poll, e.checkPoll = time.Millisecond, time.Millisecond, time.Millisecond
	return e
}

func run(t *testing.T, e *Engine, o Options, paths ...string) *Run {
	t.Helper()
	if _, err := e.Start(Spec{Dir: "/r", Source: "local", Paths: paths, Options: o}); err != nil {
		t.Fatal(err)
	}
	return waitDone(t, e)
}

func (d *fakeDeps) eventsCopy() []journal.Event {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]journal.Event(nil), d.events...)
}

func (d *fakeDeps) wasCancelled(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, x := range d.cancelled {
		if x == id {
			return true
		}
	}
	return false
}

func TestProcessAlbum(t *testing.T) {
	const token = "FLAC 16bit-44.1kHz"
	setup := func(res *musicbrainz.Result) (*fakeDeps, *pubFake) {
		d, p := happy(releases.Entry{Name: "Artiste - Album (2001)", Path: "/r/Artiste - Album (2001)", Audio: true, Dir: true, Size: 300 << 20})
		p.make = func(path string) *publish.Job {
			return &publish.Job{ID: "job-al", Path: path, Name: "Artiste - Album (2001)", Total: 300 << 20, MainFile: 0, State: publish.StReady,
				Files: []publish.File{{Rel: "01.flac", Size: 300 << 20}},
				Audio: &audio.Album{Token: token, Tracks: []audio.Track{{}}}}
		}
		d.mediainfo = func(context.Context, mediainfo.Request) (string, error) { return "", nil }
		d.mb = func(string) (*musicbrainz.Result, error) { return res, nil }
		d.fiche = func(jobID string, in FicheInput) (*Fiche, error) {
			p.mu.Lock()
			p.inputs = append(p.inputs, in)
			p.mu.Unlock()
			f := &Fiche{Title: "Album", Presentation: "p", NFO: "n", Language: "VO"}
			f.Category = &struct {
				ID   Text   `json:"id"`
				Slug string `json:"slug"`
			}{ID: "51", Slug: "audio-musique"}
			if !strings.Contains(in.ReleaseName, token) {
				f.Warnings = []string{"audio_naming"}
			}
			return f, nil
		}
		return d, p
	}

	best := &musicbrainz.Candidate{Release: musicbrainz.Release{ID: "mb1", Title: "Album", Date: "2001-05-01", Artist: "Artiste"}}
	d, p := setup(&musicbrainz.Result{Certain: true, Best: best})
	r := run(t, quick(d), Options{FixName: true}, "/r/Artiste - Album (2001)")
	en := r.Entries[0]
	if en.State != "published" || !strings.HasPrefix(en.Detail, "publiée · Audio ▸ Musique · Album (2001) · nom complété : ") {
		t.Fatalf("album : %+v", en)
	}
	f := p.fields["job-al"]
	if f["tmdbUrl"] != "https://musicbrainz.org/release/mb1" || f["creator"] != "Artiste" || !strings.Contains(f["releaseName"], token) {
		t.Errorf("champs : %v", f)
	}
	in := p.inputs[0]
	if in.CategorySlug == nil || *in.CategorySlug != "audio-musique" || in.MbRef == nil || *in.MbRef != "https://musicbrainz.org/release/mb1" {
		t.Errorf("entrée de fiche : %+v", in)
	}

	d, _ = setup(&musicbrainz.Result{Certain: false})
	r = run(t, quick(d), Options{FixName: true}, "/r/Artiste - Album (2001)")
	if en := r.Entries[0]; en.State != "review" || !strings.Contains(en.Detail, "album incertain : album introuvable sur MusicBrainz") {
		t.Fatalf("album incertain : %+v", en)
	}
}

func TestProcessStopDuringHashing(t *testing.T) {
	d, p := happy(releases.Entry{Name: "A", Path: "/r/A", Video: true, Size: 5 << 30})
	p.make = func(path string) *publish.Job {
		j := videoJob(path)
		j.State, j.Hashed = publish.StHashing, 1<<30
		return j
	}
	e := quick(d)
	if _, err := e.Start(Spec{Dir: "/r", Source: "local", Paths: []string{"/r/A"}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 500 && !strings.HasPrefix(e.State().Detail, "hachage"); i++ {
		time.Sleep(2 * time.Millisecond)
	}
	e.Stop()
	r := waitDone(t, e)
	if en := r.Entries[0]; en.State != "pending" || en.Detail != "" {
		t.Fatalf("arrêt pendant le hachage : %+v", en)
	}
	if !d.wasCancelled("job-/r/A") {
		t.Error("le job doit être annulé")
	}
	if ev := d.eventsCopy(); len(ev) != 0 {
		t.Errorf("rien au journal : %v", ev)
	}
}

// L'arrêt pendant un appel au site ne change pas l'issue de la release.
func TestProcessStopDuringSiteCall(t *testing.T) {
	for _, sim := range []bool{false, true} {
		d, _ := happy(releases.Entry{Name: "A", Path: "/r/A", Video: true, Size: 5 << 30})
		in, release := make(chan struct{}), make(chan struct{})
		d.tmdb = func(q string) ([]Work, error) {
			close(in)
			<-release
			return []Work{{Type: "movie", ID: "7", Title: "Titre", Year: "2020"}}, nil
		}
		e := quick(d)
		if _, err := e.Start(Spec{Dir: "/r", Source: "local", Paths: []string{"/r/A"}, Options: Options{Sim: sim}}); err != nil {
			t.Fatal(err)
		}
		<-in
		e.Stop()
		close(release)
		r := waitDone(t, e)
		want := "published"
		if sim {
			want = "simulated"
		}
		if en := r.Entries[0]; en.State != want {
			t.Fatalf("sim=%v : %+v", sim, en)
		}
	}
}

func TestProcessReady(t *testing.T) {
	prep := &Prepared{Name: "X", Summary: "Films ▸ Film · X (2020)", Fields: map[string]string{"releaseName": "X", "categoryId": "11"}}
	setup := func(found bool) (*fakeDeps, *pubFake) {
		d, p := happy(releases.Entry{Name: "X", Path: "/r/X", Video: true, Size: 10})
		d.last = map[string]journal.Event{"/r/X": {Kind: "batch_ready"}}
		d.prepared = func(key string) (*Prepared, bool) {
			if !found || key != "/r/X" {
				return nil, false
			}
			return prep, true
		}
		return d, p
	}

	d, p := setup(true)
	r := run(t, quick(d), Options{}, "/r/X")
	if en := r.Entries[0]; en.State != "published" || en.Detail != "publiée · Films ▸ Film · X (2020)" {
		t.Fatalf("prête : %+v", en)
	}
	if f := p.fields["job-/r/X"]; len(f) != 2 || f["releaseName"] != "X" || f["categoryId"] != "11" {
		t.Errorf("champs : %v", f)
	}
	if len(d.dropped) != 1 || d.dropped[0] != "/r/X" {
		t.Errorf("fiche gardée non retirée : %v", d.dropped)
	}

	d, p = setup(true)
	r = run(t, quick(d), Options{Sim: true}, "/r/X")
	if en := r.Entries[0]; en.Detail != "serait publiée avec ta fiche : Films ▸ Film · X (2020)" || p.starts != 0 {
		t.Fatalf("simulation : %+v, %d démarrages", en, p.starts)
	}

	d, _ = setup(false)
	r = run(t, quick(d), Options{}, "/r/X")
	if en := r.Entries[0]; en.State != "review" || en.Detail != "fiche gardée introuvable : « Revoir » pour la refaire" {
		t.Fatalf("sans fiche gardée : %+v", en)
	}
}

func TestProcessWaitChecked(t *testing.T) {
	d, p := happy(releases.Entry{Name: "A", Path: "/r/A", Video: true, Size: 10})
	p.published = func(j *publish.Job) { j.State = publish.StSeeding }
	n := 0
	p.get = func(j *publish.Job) {
		if j.State == publish.StSeeding {
			if n++; n > 2 {
				j.State, j.Seed = publish.StDone, &crossseed.WatchState{Status: "seeding", Progress: 1}
			}
		}
	}
	r := run(t, quick(d), Options{WaitCheck: true}, "/r/A")
	if en := r.Entries[0]; en.State != "published" || !strings.HasSuffix(en.Detail, " · vérifiée à 100 %, en seed") {
		t.Fatalf("vérification : %+v", en)
	}

	d, p = happy(releases.Entry{Name: "A", Path: "/r/A", Video: true, Size: 10})
	p.published = func(j *publish.Job) { j.Seed = &crossseed.WatchState{Status: "checking", Progress: 1, Detail: held} }
	r = run(t, quick(d), Options{WaitCheck: true, SeedLater: true}, "/r/A")
	if en := r.Entries[0]; !strings.Contains(en.Detail, "seed lancé à la fin du lot") || strings.Contains(en.Detail, held) {
		t.Fatalf("seed à la fin : %+v", en)
	}
}

func TestProcessCases(t *testing.T) {
	t.Run("doublon", func(t *testing.T) {
		d, p := happy(releases.Entry{Name: "A", Path: "/r/A", Video: true, Size: 10})
		p.make = func(path string) *publish.Job {
			j := videoJob(path)
			j.Duplicate = &v3x.Match{Kind: "exact", ID: "d1", Name: "Dup"}
			return j
		}
		r := run(t, quick(d), Options{}, "/r/A")
		ev := d.eventsCopy()
		if r.Entries[0].State != "duplicate" || len(ev) != 1 || ev[0].ID != "d1" || ev[0].Kind != "batch_duplicate" || !d.wasCancelled("job-/r/A") {
			t.Fatalf("doublon : %+v, %v", r.Entries[0], ev)
		}
	})
	// Refusée à l'envoi parce que son contenu est déjà sur V3X : « Déjà
	// présent », pas « Erreur » (raoul77, 27/10/26).
	t.Run("doublon refusé à l'envoi", func(t *testing.T) {
		d, p := happy(releases.Entry{Name: "A", Path: "/r/A", Video: true, Size: 10})
		p.published = func(j *publish.Job) {
			j.State, j.Published = publish.StReady, nil
			j.Error, j.ErrorBody = "duplicate_content", `{"error":"duplicate_content","kind":"size","id":"e1","name":"Film.Existant","existingStatus":"active"}`
		}
		r := run(t, quick(d), Options{}, "/r/A")
		en, ev := r.Entries[0], d.eventsCopy()
		if en.State != "duplicate" || en.Detail != "le même contenu est déjà sur V3X : Film.Existant · la Médiathèque la met en seed avec ces fichiers" || r.Published != 0 {
			t.Fatalf("doublon refusé : %+v", en)
		}
		if len(ev) != 1 || ev[0].Kind != "batch_duplicate" || ev[0].ID != "e1" || !d.wasCancelled("job-/r/A") {
			t.Fatalf("journal : %v", ev)
		}
	})
	t.Run("retirée", func(t *testing.T) {
		d, p := happy(releases.Entry{Name: "A", Path: "/r/A", Video: true, Size: 10})
		p.make = func(path string) *publish.Job {
			j := videoJob(path)
			j.Gone = &v3x.Gone{Status: "removed"}
			return j
		}
		r := run(t, quick(d), Options{}, "/r/A")
		if en := r.Entries[0]; en.State != "review" || !strings.Contains(en.Detail, "« Revoir »") {
			t.Fatalf("retirée : %+v", en)
		}
	})
	t.Run("mediainfo", func(t *testing.T) {
		d, _ := happy(releases.Entry{Name: "A", Path: "/r/A", Video: true, Size: 10})
		d.mediainfo = func(context.Context, mediainfo.Request) (string, error) { return "", errors.New("illisible") }
		r := run(t, quick(d), Options{}, "/r/A")
		if en := r.Entries[0]; en.State != "review" || !strings.Contains(en.Detail, "MediaInfo illisible") {
			t.Fatalf("mediainfo : %+v", en)
		}
	})
	t.Run("nfo", func(t *testing.T) {
		d, p := happy(releases.Entry{Name: "A", Path: "/r/A", Video: true, Size: 10})
		p.make = func(path string) *publish.Job {
			j := videoJob(path)
			j.Files = append(j.Files, publish.File{Rel: "A.nfo", Size: 9})
			return j
		}
		d.pubRead = func(id string, i int) ([]byte, int64, error) {
			if i != 1 {
				return nil, 0, errors.New("mauvais fichier")
			}
			return []byte("NFO perso"), 9, nil
		}
		run(t, quick(d), Options{KeepNfo: true}, "/r/A")
		if got := p.fields["job-/r/A"]["nfo"]; got != "NFO perso\n\nGeneral\nFormat : Matroska" {
			t.Fatalf("nfo : %q", got)
		}
	})
	t.Run("panique", func(t *testing.T) {
		d, _ := happy(releases.Entry{Name: "P", Path: "/r/P", Video: true, Size: 10}, releases.Entry{Name: "A", Path: "/r/A", Video: true, Size: 10})
		ok := d.fiche
		d.fiche = func(jobID string, in FicheInput) (*Fiche, error) {
			if in.ReleaseName == "P" {
				var f *Fiche
				_ = f.Title // déréférence nil
			}
			return ok(jobID, in)
		}
		r := run(t, quick(d), Options{Sim: true}, "/r/P", "/r/A")
		if en := entryByPath(r, "/r/P"); en.State != "error" || !strings.Contains(en.Detail, "nil pointer") {
			t.Fatalf("panique : %+v", en)
		}
		if !d.wasCancelled("job-/r/P") {
			t.Error("la publication de la release en panique doit être annulée")
		}
		if en := entryByPath(r, "/r/A"); en.State != "simulated" {
			t.Fatalf("la suivante : %+v", en)
		}
	})
}
