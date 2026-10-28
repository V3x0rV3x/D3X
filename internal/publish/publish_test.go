package publish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/V3x0rV3x/D3X/internal/audio"
	"github.com/V3x0rV3x/D3X/internal/bencode"
	"github.com/V3x0rV3x/D3X/internal/client"
	"github.com/V3x0rV3x/D3X/internal/config"
	"github.com/V3x0rV3x/D3X/internal/crossseed"
	"github.com/V3x0rV3x/D3X/internal/maketorrent"
	"github.com/V3x0rV3x/D3X/internal/pathmap"
	"github.com/V3x0rV3x/D3X/internal/qbit"
	"github.com/V3x0rV3x/D3X/internal/source"
	"github.com/V3x0rV3x/D3X/internal/torrent"
	"github.com/V3x0rV3x/D3X/internal/v3x"
)

// fakeClient : un faux qBittorrent qui note ses ajouts et ses démarrages ;
// une vérification dure deux tours puis donne 100 %.
type fakeClient struct {
	mu      sync.Mutex
	list    map[string]*fakeEnt
	added   map[string]string
	started []string
	srv     *httptest.Server
}

type fakeEnt struct {
	qbit.Torrent
	left int
}

func newFakeClient(t *testing.T) *fakeClient {
	f := &fakeClient{list: map[string]*fakeEnt{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/api/v2/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "x", Path: "/"})
			io.WriteString(w, "Ok.")
		case "/api/v2/app/version":
			io.WriteString(w, "v5.0.0")
		case "/api/v2/app/webapiVersion":
			io.WriteString(w, "2.11.0")
		case "/api/v2/torrents/info":
			out := []qbit.Torrent{}
			for h, e := range f.list {
				if q := r.Form.Get("hashes"); q != "" && !strings.Contains(q, h) {
					continue
				}
				if e.left > 0 {
					e.left--
					e.State = "checkingUP"
					if e.left == 0 {
						e.State, e.Progress = "stoppedUP", 1
					}
				}
				out = append(out, e.Torrent)
			}
			json.NewEncoder(w).Encode(out)
		case "/api/v2/torrents/add":
			r.ParseMultipartForm(1 << 20)
			f.added = map[string]string{}
			for k, v := range r.MultipartForm.Value {
				f.added[k] = v[0]
			}
			fh := r.MultipartForm.File["torrents"][0]
			fd, _ := fh.Open()
			raw, _ := io.ReadAll(fd)
			p, _ := torrent.Parse(raw)
			h := strings.ToLower(p.InfoHash)
			f.list[h] = &fakeEnt{Torrent: qbit.Torrent{Hash: h, Name: p.Name, State: "stoppedDL"}}
			io.WriteString(w, "Ok.")
		case "/api/v2/torrents/recheck":
			if e := f.list[strings.ToLower(r.Form.Get("hashes"))]; e != nil {
				e.left = 2
			}
		case "/api/v2/torrents/start":
			f.started = append(f.started, r.Form.Get("hashes"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// fakeSite : un faux V3X sans doublon, qui accepte la publication et rend le
// .torrent de l'uploadeur.
type fakeSite struct {
	uploaded []byte
	fields   map[string]string
	srv      *httptest.Server
}

func newFakeSite(t *testing.T) *fakeSite {
	s := &fakeSite{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/d3x/match":
			io.WriteString(w, `{"results":[{"key":"0","match":null}]}`)
		case r.URL.Path == "/api/torrents":
			r.ParseMultipartForm(8 << 20)
			s.fields = map[string]string{}
			for k, v := range r.MultipartForm.Value {
				s.fields[k] = v[0]
			}
			f, _, _ := r.FormFile("file")
			s.uploaded, _ = io.ReadAll(f)
			p, _ := torrent.Parse(s.uploaded)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]interface{}{"id": "aaaaaaaa-0000-4000-8000-000000000001", "status": "active", "infoHash": p.InfoHash, "uploadBonus": 400})
		case strings.HasSuffix(r.URL.Path, "/download"):
			d, _ := bencode.Decode(s.uploaded)
			root := d.Root.(bencode.Dict)
			root["announce"] = []byte("https://announce.v3x.club/pk/announce")
			b, _ := bencode.Encode(root)
			w.Write(b)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// release : un dossier de release dans parent.
func release(t *testing.T) (parent, dir string) {
	tmp := t.TempDir()
	t.Setenv("AppData", tmp)
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)
	parent = filepath.Join(tmp, "Releases")
	dir = filepath.Join(parent, "Film.2024.1080p.WEB.H264-D3X")
	os.MkdirAll(dir, 0o755)
	data := make([]byte, 5<<20)
	rand.New(rand.NewSource(1)).Read(data)
	os.WriteFile(filepath.Join(dir, "Film.2024.1080p.WEB.H264-D3X.mkv"), data, 0o644)
	os.WriteFile(filepath.Join(dir, ".DS_Store"), []byte("x"), 0o644)
	return parent, dir
}

// envOf : V3X et des clients joignables, dans cet ordre.
func envOf(site *fakeSite, targets ...crossseed.Target) crossseed.Clients {
	cfg := config.Defaults()
	for _, t := range targets {
		cfg.Clients = append(cfg.Clients, config.ClientConfig{ID: t.ID, Name: t.Name, PathMap: t.PathMap})
	}
	v := v3x.New(site.srv.URL, "cle", "D3X/test")
	return func() (*crossseed.Env, error) {
		c := *cfg
		return &crossseed.Env{V3X: v, Cfg: &c, Targets: targets}, nil
	}
}

// Parcours complet contre un faux qBittorrent et un faux V3X : liste, hachage,
// publication, .torrent de l'uploadeur, ajout à l'arrêt, vérification, seed.
func TestPublishFlow(t *testing.T) {
	parent, dir := release(t)
	fc := newFakeClient(t)
	site := newFakeSite(t)
	q, _ := client.New(client.Options{URL: fc.srv.URL})
	clients := envOf(site, crossseed.Target{ID: "c1", Name: "qBittorrent", Client: q})
	eng := crossseed.New(clients)
	m := New(clients, eng, "test")

	j, err := m.Start(source.Local{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if j.Duplicate != nil || len(j.Files) != 1 || j.MainFile != 0 || len(j.Skipped) != 1 {
		t.Fatalf("liste : %+v", j)
	}
	if j.Client != "c1" || j.ClientDir != parent {
		t.Fatalf("client %q, dossier %q", j.Client, j.ClientDir)
	}
	wait(t, func() bool { g, _ := m.Get(j.ID); return g.State == StReady })
	g, _ := m.Get(j.ID)
	if g.InfoHash == "" {
		t.Fatal("info-hash attendu dès la fin du hachage")
	}
	if err := m.Publish(j.ID, map[string]string{"categoryId": "11", "releaseName": "Film.2024.1080p.WEB.H264-D3X"}); err != nil {
		t.Fatal(err)
	}
	wait(t, func() bool {
		g, _ := m.Get(j.ID)
		return g.State == StDone && g.Seed != nil && g.Seed.Status == crossseed.StSeeding
	})
	g, _ = m.Get(j.ID)
	if g.SeedError != "" {
		t.Fatalf("seed : %s", g.SeedError)
	}
	if site.fields["categoryId"] != "11" {
		t.Fatalf("champs envoyés : %v", site.fields)
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if fc.added["savepath"] != parent || fc.added["stopped"] != "true" || fc.added["skip_checking"] != "false" {
		t.Fatalf("ajout au client : %v", fc.added)
	}
	p, _ := torrent.Parse(site.uploaded)
	if !strings.EqualFold(p.InfoHash, g.InfoHash) || len(fc.started) != 1 {
		t.Fatalf("info-hash publié %s, suivi %s, démarrés %v", p.InfoHash, g.InfoHash, fc.started)
	}
}

// Deux clients : par défaut, celui dont la correspondance de dossiers couvre
// le dossier de la release ; le membre peut en choisir un autre, le dossier
// vu par le client est alors recalculé ; la release part en seed dans le
// client choisi, à son chemin, jamais dans l'autre.
func TestPublishTwoClients(t *testing.T) {
	parent, dir := release(t)
	box, nas := newFakeClient(t), newFakeClient(t)
	site := newFakeSite(t)
	qb, _ := client.New(client.Options{URL: box.srv.URL})
	qn, _ := client.New(client.Options{URL: nas.srv.URL})
	nasRules := []pathmap.Rule{{From: parent, To: "/volume1/releases"}}
	clients := envOf(site, crossseed.Target{ID: "box", Name: "Seedbox", Client: qb}, crossseed.Target{ID: "nas", Name: "NAS", Client: qn, PathMap: nasRules})
	eng := crossseed.New(clients)
	m := New(clients, eng, "test")

	j, err := m.Start(source.Local{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if j.Client != "nas" || j.ClientName != "NAS" || j.ClientDir != "/volume1/releases" {
		t.Fatalf("client par défaut : %q %q %q", j.Client, j.ClientName, j.ClientDir)
	}
	// Choix de la seedbox : pas de règle, le même chemin.
	if err := m.SetClient(j.ID, "box"); err != nil {
		t.Fatal(err)
	}
	if g, _ := m.Get(j.ID); g.Client != "box" || g.ClientDir != parent {
		t.Fatalf("après choix de la seedbox : %q %q", g.Client, g.ClientDir)
	}
	// Dossier corrigé à la main : la règle à retenir concerne la seedbox.
	rule, cid, err := m.SetClientDir(j.ID, "/home/moi/releases")
	if err != nil || cid != "box" || rule == nil || *rule != (pathmap.Rule{From: parent, To: "/home/moi/releases"}) {
		t.Fatalf("règle : %+v %q %v", rule, cid, err)
	}
	// Retour au NAS, puis publication.
	if err := m.SetClient(j.ID, "nas"); err != nil {
		t.Fatal(err)
	}
	if err := m.SetClient(j.ID, "inconnu"); err == nil {
		t.Fatal("client inconnu accepté")
	}
	wait(t, func() bool { g, _ := m.Get(j.ID); return g.State == StReady })
	if err := m.Publish(j.ID, map[string]string{"categoryId": "11"}); err != nil {
		t.Fatal(err)
	}
	wait(t, func() bool {
		g, _ := m.Get(j.ID)
		return g.State == StDone && g.Seed != nil && g.Seed.Status == crossseed.StSeeding
	})
	if err := m.SetClient(j.ID, "box"); err == nil {
		t.Fatal("client changé après l'envoi")
	}
	nas.mu.Lock()
	defer nas.mu.Unlock()
	box.mu.Lock()
	defer box.mu.Unlock()
	if nas.added["savepath"] != "/volume1/releases" || len(nas.started) != 1 {
		t.Fatalf("NAS : ajout %v, démarrés %v", nas.added, nas.started)
	}
	if box.added != nil || len(box.list) != 0 {
		t.Fatalf("la seedbox ne devait rien recevoir : %v", box.added)
	}
}

// listOnly : un client qui ne sait que donner sa liste.
type listOnly struct {
	client.Client
	list []client.Torrent
}

func (l listOnly) List(context.Context, []string) ([]client.Torrent, error) { return l.list, nil }

type downClient struct{ client.Client }

func (downClient) List(context.Context, []string) ([]client.Torrent, error) {
	return nil, errors.New("injoignable")
}

// Le client choisi par défaut : règle qui couvre le dossier, sinon celui qui
// seede déjà dans ce dossier, sinon celui de la machine des fichiers, sinon
// le premier.
func TestPick(t *testing.T) {
	parent := t.TempDir()
	os.MkdirAll(filepath.Join(parent, "Deja.La"), 0o755)
	seeds := listOnly{list: []client.Torrent{{ContentPath: "/srv/rel/Deja.La"}}}
	empty := listOnly{}
	env := func(ts ...crossseed.Target) *crossseed.Env {
		return &crossseed.Env{Cfg: config.Defaults(), Targets: ts}
	}
	ctx := context.Background()
	cases := []struct {
		name    string
		env     *crossseed.Env
		id, dir string
	}{
		{"règle", env(crossseed.Target{ID: "a", Client: seeds}, crossseed.Target{ID: "b", Client: empty, PathMap: []pathmap.Rule{{From: parent, To: "/b"}}}), "b", "/b"},
		{"seede déjà là", env(crossseed.Target{ID: "a", Client: empty, Host: "127.0.0.1"}, crossseed.Target{ID: "b", Client: seeds}), "b", "/srv/rel"},
		{"même machine", env(crossseed.Target{ID: "a", Client: empty, Host: "box.exemple.net"}, crossseed.Target{ID: "b", Client: downClient{}, Host: "localhost"}), "b", parent},
		{"le premier", env(crossseed.Target{ID: "a", Client: empty, Host: "box.exemple.net"}, crossseed.Target{ID: "b", Client: empty}), "a", parent},
	}
	for _, c := range cases {
		p := pick(ctx, c.env, source.Local{}, parent)
		if p.t.ID != c.id || p.dir != c.dir {
			t.Errorf("%s : client %q dossier %q, attendu %q %q", c.name, p.t.ID, p.dir, c.id, c.dir)
		}
	}
}

func wait(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("délai dépassé")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Les dossiers à secrets ne se publient pas.
func TestPrivateFolders(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("AppData", tmp)
	t.Setenv("XDG_CONFIG_HOME", tmp)
	dir, _ := config.Dir()
	for _, p := range []string{filepath.Join(dir, "config.json"), dir, "/home/membre/.ssh/id_ed25519", `C:\Users\x\.ssh`, "/home/membre/.d3x/agent"} {
		if private(source.Local{}, p) == nil {
			t.Errorf("%s accepté", p)
		}
	}
	for _, p := range []string{"/data/Films/Film.2020", `D:\Releases\Album`} {
		if err := private(source.Local{}, p); err != nil {
			t.Errorf("%s refusé : %v", p, err)
		}
	}
}

func TestFilePath(t *testing.T) {
	m := New(nil, nil, "test")
	m.jobs["j"] = &Job{ID: "j", plan: &maketorrent.Plan{Files: []maketorrent.Entry{{Rel: "a.mkv", Abs: "/x/a.mkv", Size: 3}}}}
	if p, ok := m.FilePath("j", 0); !ok || p != "/x/a.mkv" {
		t.Fatalf("%q %v", p, ok)
	}
	if _, ok := m.FilePath("j", 1); ok {
		t.Fatal("index hors limites")
	}
	if _, ok := m.FilePath("inconnu", 0); ok {
		t.Fatal("job inconnu")
	}
}

// slowHash : un hachage qui ne finit qu'une fois annulé ; compte ceux qui
// ont fini.
type slowHash struct {
	source.Local
	ended *atomic.Int32
}

func (s slowHash) Hash(ctx context.Context, _ *maketorrent.Plan, _ []string, _ string, _ maketorrent.Progress) ([]byte, bool, error) {
	defer s.ended.Add(1)
	<-ctx.Done()
	return nil, false, ctx.Err()
}

// Le Lot annule un doublon juste après Start : l'annulation doit exister dès
// le retour de Start, sinon le hachage tourne jusqu'au bout sans job pour
// l'arrêter (bertho-zero, ticket n°3, 27/10/26 : 23 hachages à la fois).
func TestCancelRightAfterStart(t *testing.T) {
	_, dir := release(t)
	fc := newFakeClient(t)
	site := newFakeSite(t)
	q, _ := client.New(client.Options{URL: fc.srv.URL})
	clients := envOf(site, crossseed.Target{ID: "c1", Name: "qBittorrent", Client: q})
	m := New(clients, crossseed.New(clients), "test")
	var ended atomic.Int32
	src := slowHash{ended: &ended}
	const n = 20
	for i := 0; i < n; i++ {
		j, err := m.Start(src, dir)
		if err != nil {
			t.Fatal(err)
		}
		m.mu.Lock()
		armed := m.jobs[j.ID] != nil && m.jobs[j.ID].cancel != nil
		m.mu.Unlock()
		if !armed {
			t.Fatal("pas d'annulation au retour de Start")
		}
		m.Cancel(j.ID)
	}
	wait(t, func() bool { return ended.Load() == n })
	if m.Busy() {
		t.Fatal("publications annulées encore suivies")
	}
}

// Dossier rangé par une médiathèque (Hypsis, 27/10/26) : le nom proposé est
// celui de sa vidéo quand le dossier n'a pas l'air d'un nom de release.
func TestNameFromVideo(t *testing.T) {
	plan := func(name string, single bool, files ...maketorrent.Entry) *maketorrent.Plan {
		p := &maketorrent.Plan{Name: name, Single: single, Files: files}
		for _, f := range files {
			p.Total += f.Size
		}
		return p
	}
	movie := maketorrent.Entry{Rel: "1917.2019.MULTi.1080p.BluRay.x264-GROUPE.mkv", Size: 8 << 30}
	srt := maketorrent.Entry{Rel: "1917.2019.MULTi.1080p.BluRay.x264-GROUPE.fr.srt", Size: 80 << 10}
	for _, c := range []struct {
		name string
		p    *maketorrent.Plan
		main int
		want string
	}{
		{"dossier de médiathèque", plan("1917 (2019)", false, srt, movie), 1, "1917.2019.MULTi.1080p.BluRay.x264-GROUPE"},
		{"dossier de release", plan("1917.2019.MULTi.1080p.BluRay.x264-GROUPE", false, movie), 0, ""},
		{"fichier seul", plan(movie.Rel, true, movie), 0, ""},
		{"vidéo sans nom de release", plan("1917 (2019)", false, maketorrent.Entry{Rel: "1917 (2019).mkv", Size: 8 << 30}), 0, ""},
		{"saison", plan("Série (2020)", false,
			maketorrent.Entry{Rel: "Saison 1/Serie.S01E01.1080p.WEB.H264-GRP.mkv", Size: 2 << 30},
			maketorrent.Entry{Rel: "Saison 1/Serie.S01E02.1080p.WEB.H264-GRP.mkv", Size: 1 << 30},
			maketorrent.Entry{Rel: "Saison 1/Serie.S01E03.1080p.WEB.H264-GRP.mkv", Size: 1 << 30}), 0, ""},
	} {
		if got := nameFromVideo(c.p, c.main); got != c.want {
			t.Errorf("%s : %q, attendu %q", c.name, got, c.want)
		}
	}
}

func TestKindOf(t *testing.T) {
	plan := func(files ...maketorrent.Entry) *maketorrent.Plan { return &maketorrent.Plan{Files: files} }
	flac := func(n string, size int64) maketorrent.Entry { return maketorrent.Entry{Rel: n + ".flac", Size: size} }
	for _, c := range []struct {
		name       string
		p          *maketorrent.Plan
		video, aud int
		album      bool
	}{
		{"film", plan(maketorrent.Entry{Rel: "Film.2020.1080p.mkv", Size: 8 << 30}), 0, -1, false},
		{"album", plan(flac("01", 30<<20), flac("02", 40<<20)), -1, 1, true},
		// Indrop, 28/10/26 : discographie avec un clip, l'audio pèse plus lourd.
		{"discographie avec un clip", plan(flac("01", 400<<20), flac("02", 500<<20), maketorrent.Entry{Rel: "Clip.mp4", Size: 200 << 20}), 2, 1, true},
		// Un film livré avec sa bande originale reste un film.
		{"film et sa bande originale", plan(maketorrent.Entry{Rel: "Film.2020.1080p.mkv", Size: 8 << 30}, flac("BO", 300<<20)), 0, 1, false},
		{"rien", plan(maketorrent.Entry{Rel: "livre.epub", Size: 1 << 20}), -1, -1, false},
	} {
		k := kindOf(c.p)
		if k.mainVideo != c.video || k.mainAudio != c.aud || k.album != c.album {
			t.Errorf("%s : vidéo %d piste %d album %v, attendu %d %d %v", c.name, k.mainVideo, k.mainAudio, k.album, c.video, c.aud, c.album)
		}
	}
}

// audioSource : une source dont l'analyse des pistes renvoie un album.
type audioSource struct {
	source.Local
	tracks int
}

func (a audioSource) Audio(context.Context, *maketorrent.Plan, []string) (*audio.Album, error) {
	al := &audio.Album{}
	for i := 0; i < a.tracks; i++ {
		al.Tracks = append(al.Tracks, audio.Track{Rel: fmt.Sprintf("%02d.flac", i+1)})
	}
	return al, nil
}

func TestSetMusic(t *testing.T) {
	m := New(nil, nil, "test")
	plan := &maketorrent.Plan{Name: "Groupe - Live", Files: []maketorrent.Entry{
		{Rel: "Groupe - Live/Live.mkv", Size: 4 << 30},
		{Rel: "Groupe - Live/01.flac", Size: 300 << 20},
	}}
	m.jobs["j"] = &Job{ID: "j", MainFile: 0, plan: plan, src: audioSource{tracks: 1}}
	if err := m.SetMusic("j", true); err != nil {
		t.Fatal(err)
	}
	j, _ := m.Get("j")
	if j.Audio == nil || len(j.Audio.Tracks) != 1 || j.MainFile != 1 {
		t.Fatalf("en album : audio %v, fichier principal %d", j.Audio, j.MainFile)
	}
	if err := m.SetMusic("j", false); err != nil {
		t.Fatal(err)
	}
	j, _ = m.Get("j")
	if j.Audio != nil || j.MainFile != 0 {
		t.Fatalf("en vidéo : audio %v, fichier principal %d", j.Audio, j.MainFile)
	}
	m.jobs["v"] = &Job{ID: "v", plan: &maketorrent.Plan{Files: []maketorrent.Entry{{Rel: "Film.mkv", Size: 1 << 30}}}, src: audioSource{}}
	if err := m.SetMusic("v", true); err == nil {
		t.Fatal("une release sans piste audio ne passe pas en album")
	}
}
