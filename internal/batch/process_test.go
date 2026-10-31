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
	e.noteWait = 50 * time.Millisecond
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
	d.mu.Lock()
	nLinks := len(d.linksSet)
	d.mu.Unlock()
	if nLinks != 1 {
		t.Fatalf("option liens non posée sur la release gardée : %d", nLinks)
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

// Option « Seeder par liens » : posée sur chaque publication (vraie ou
// fausse), et la ligne dit où la release est rangée, ou pourquoi elle est
// partie sur ses fichiers d'origine.
func TestProcessLinks(t *testing.T) {
	d, p := happy(releases.Entry{Name: "A", Path: "/r/A", Video: true, Size: 10})
	p.published = func(j *publish.Job) { j.SeedVia, j.LinksDir = "links", "/liens" }
	r := run(t, quick(d), Options{Links: true}, "/r/A")
	d.mu.Lock()
	on, ok := d.linksSet["job-/r/A"]
	d.mu.Unlock()
	if !ok || !on {
		t.Fatal("option liens non posée sur la publication")
	}
	if en := r.Entries[0]; en.State != "published" || !strings.HasSuffix(en.Detail, " · liens dans « /liens »") {
		t.Fatalf("par liens : %+v", en)
	}

	// Repli : la raison est dite.
	d, p = happy(releases.Entry{Name: "A", Path: "/r/A", Video: true, Size: 10})
	p.published = func(j *publish.Job) {
		j.SeedVia, j.LinksNote = "origin", "liens impossibles : le dossier des liens « /liens » n'existe pas"
	}
	r = run(t, quick(d), Options{Links: true}, "/r/A")
	if en := r.Entries[0]; !strings.HasSuffix(en.Detail, " · liens impossibles : le dossier des liens « /liens » n'existe pas, seed sur les fichiers d'origine") {
		t.Fatalf("repli : %+v", en)
	}

	// Option décochée : posée à faux, rien d'ajouté au détail.
	d, p = happy(releases.Entry{Name: "A", Path: "/r/A", Video: true, Size: 10})
	p.published = func(j *publish.Job) { j.SeedVia = "origin" }
	r = run(t, quick(d), Options{}, "/r/A")
	d.mu.Lock()
	on, ok = d.linksSet["job-/r/A"]
	d.mu.Unlock()
	if !ok || on || strings.Contains(r.Entries[0].Detail, "liens") {
		t.Fatalf("option décochée : posée=%v on=%v détail %q", ok, on, r.Entries[0].Detail)
	}

	// L'ajout au client traîne : la mention n'attend pas plus que noteWait.
	d, p = happy(releases.Entry{Name: "A", Path: "/r/A", Video: true, Size: 10})
	p.published = func(j *publish.Job) { j.State = publish.StSeeding }
	r = run(t, quick(d), Options{Links: true}, "/r/A")
	if en := r.Entries[0]; en.State != "published" || strings.Contains(en.Detail, "liens") {
		t.Fatalf("ajout lent : %+v", en)
	}

	// Option refusée (client sans dossier de liens) : la raison est dite sur
	// la ligne, sans attendre l'ajout au client.
	d, p = happy(releases.Entry{Name: "A", Path: "/r/A", Video: true, Size: 10})
	d.linksErr = errors.New("ce client n'a pas de dossier de liens (Réglages ▸ Liens)")
	p.published = func(j *publish.Job) { j.State = publish.StSeeding }
	e := quick(d)
	e.noteWait = time.Hour
	start := time.Now()
	r = run(t, e, Options{Links: true}, "/r/A")
	if en := r.Entries[0]; en.State != "published" || !strings.HasSuffix(en.Detail, " · liens impossibles : ce client n'a pas de dossier de liens (Réglages ▸ Liens), seed sur les fichiers d'origine") {
		t.Fatalf("option refusée : %+v", en)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("option refusée : l'ajout au client a été attendu")
	}

	// Le seed a échoué après la pose des liens : pas de « liens dans ».
	d, p = happy(releases.Entry{Name: "A", Path: "/r/A", Video: true, Size: 10})
	p.published = func(j *publish.Job) {
		j.SeedVia, j.LinksDir, j.SeedError = "links", "/liens", "le client n'a pas confirmé l'ajout"
	}
	r = run(t, quick(d), Options{Links: true}, "/r/A")
	if en := r.Entries[0]; en.State != "published" || strings.Contains(en.Detail, "liens dans") {
		t.Fatalf("seed en échec : %+v", en)
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

// Règles de réupload (1.1.41) : toute release du lot passe la même
// vérification, avant le hachage, qu'elle vienne d'un dossier parcouru ou
// d'une fiche gardée ; les autres releases du lot comptent comme copies.
func TestProcessPolicy(t *testing.T) {
	tagged := releases.Entry{Name: "Film (1992) - 1080p x264 ac3 mHDgz.mkv", Path: "/r/Film (1992) - 1080p x264 ac3 mHDgz.mkv", Video: true, Size: 3 << 30, Files: 1}
	detag := releases.Entry{Name: "Film (1992).mkv", Path: "/r/Film (1992).mkv", Video: true, Size: 3 << 30, Files: 1}
	free := releases.Entry{Name: "Libre.2021.1080p.WEB.x264-LIBRE", Path: "/r/Libre.2021.1080p.WEB.x264-LIBRE", Video: true, Size: 1 << 30, Files: 1}

	d, p := happy(tagged, detag, free)
	var mu sync.Mutex
	var seen []PolicyQuery
	d.policy = func(q PolicyQuery) (*PolicyStop, error) {
		mu.Lock()
		seen = append(seen, q)
		mu.Unlock()
		for _, n := range q.Names {
			if strings.Contains(n, "mHDgz") {
				return &PolicyStop{Label: "Réupload interdit · mHDgz", Motive: "la team mHDgz n'autorise pas le réupload de ses releases, D3X ne la publie pas"}, nil
			}
		}
		for _, o := range q.Others {
			if strings.Contains(o.Name, "mHDgz") && o.Size == q.Size && o.Files == q.Files {
				return &PolicyStop{Label: "Detag · Réupload interdit · mHDgz", Motive: "copie sans le tag"}, nil
			}
		}
		return nil, nil
	}
	r := run(t, quick(d), Options{}, tagged.Path, detag.Path, free.Path)
	got := map[string]*Entry{}
	for _, en := range r.Entries {
		got[en.Path] = en
	}
	if en := got[tagged.Path]; en.State != "review" || !strings.HasPrefix(en.Detail, "Réupload interdit · mHDgz : ") {
		t.Errorf("taguée : %+v", en)
	}
	if en := got[detag.Path]; en.State != "review" || !strings.HasPrefix(en.Detail, "Detag · Réupload interdit · mHDgz : ") {
		t.Errorf("detag : %+v", en)
	}
	if en := got[free.Path]; en.State != "published" {
		t.Errorf("libre : %+v", en)
	}
	// Les deux releases refusées ne sont jamais hachées.
	if p.starts != 1 {
		t.Errorf("%d publications démarrées, attendu 1", p.starts)
	}
	for _, q := range seen {
		if !q.Local || q.Path == "" {
			t.Errorf("requête sans emplacement : %+v", q)
		}
	}
	for _, e := range d.eventsCopy() {
		if e.Path == detag.Path && e.Kind != "batch_review" {
			t.Errorf("journal du detag : %+v", e)
		}
	}

	// Règles illisibles : rien ne part, la release est en erreur.
	d, p = happy(free)
	d.policy = func(PolicyQuery) (*PolicyStop, error) { return nil, errors.New("site injoignable") }
	r = run(t, quick(d), Options{}, free.Path)
	if en := r.Entries[0]; en.State != "error" || !strings.Contains(en.Detail, "règles de réupload des teams illisibles") || p.starts != 0 {
		t.Errorf("règles illisibles : %+v, %d démarrages", en, p.starts)
	}

	// Fiche gardée : le nom choisi dans Publier passe aussi la règle.
	d, p = happy(releases.Entry{Name: "X", Path: "/r/X", Video: true, Size: 10})
	d.last = map[string]journal.Event{"/r/X": {Kind: "batch_ready"}}
	d.prepared = func(key string) (*Prepared, bool) {
		return &Prepared{Name: "X", Fields: map[string]string{"releaseName": "X.2020.1080p.WEB.x264-mHDgz", "categoryId": "11"}}, true
	}
	d.policy = func(q PolicyQuery) (*PolicyStop, error) {
		for _, n := range q.Names {
			if strings.Contains(n, "mHDgz") {
				return &PolicyStop{Label: "Réupload interdit · mHDgz", Motive: "non"}, nil
			}
		}
		return nil, nil
	}
	r = run(t, quick(d), Options{}, "/r/X")
	if en := r.Entries[0]; en.State != "review" || !strings.HasPrefix(en.Detail, "Réupload interdit · mHDgz") || p.starts != 0 {
		t.Errorf("fiche gardée : %+v, %d démarrages", en, p.starts)
	}
}
