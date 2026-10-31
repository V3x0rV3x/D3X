package publish

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/V3x0rV3x/D3X/internal/bencode"
	"github.com/V3x0rV3x/D3X/internal/client"
	"github.com/V3x0rV3x/D3X/internal/config"
	"github.com/V3x0rV3x/D3X/internal/crossseed"
	"github.com/V3x0rV3x/D3X/internal/source"
	"github.com/V3x0rV3x/D3X/internal/torrent"
	"github.com/V3x0rV3x/D3X/internal/v3x"
)

// Release déjà sur V3X à l'identique (Turias, 22/10/26 : Publier renvoyait
// vers Cross-seed, qui ne la voyait pas) : rien n'est publié, le .torrent de la
// fiche V3X est ajouté à l'arrêt sur ces fichiers, vérifié, puis démarré.
func TestSeedExisting(t *testing.T) {
	parent, dir := release(t)
	onV3X, p := v3xTorrentOf(t, dir)
	const id = "bbbbbbbb-0000-4000-8000-000000000002"
	site := seedExistingSite(t, onV3X, id)
	uploads := &site.uploads

	fc := newFakeClient(t)
	q, _ := client.New(client.Options{URL: fc.srv.URL})
	clients := envOf(site, crossseed.Target{ID: "c1", Name: "qBittorrent", Client: q})
	m := New(clients, crossseed.New(clients), "test")

	j, err := m.Start(source.Local{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if j.Duplicate == nil || j.Duplicate.Kind != "exact" {
		t.Fatalf("doublon attendu : %+v", j.Duplicate)
	}
	if err := m.SeedExisting(j.ID); err != nil {
		t.Fatal(err)
	}
	wait(t, func() bool {
		g, _ := m.Get(j.ID)
		return g.State == StDone && g.Seed != nil && g.Seed.Status == crossseed.StSeeding
	})
	g, _ := m.Get(j.ID)
	if !g.SeedOnly || g.SeedError != "" || g.Published != nil || uploads.Load() != 0 {
		t.Fatalf("seed seul attendu : %+v (envois au site : %d)", g, uploads.Load())
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if fc.added["savepath"] != parent || fc.added["stopped"] != "true" {
		t.Fatalf("ajout au client : %v", fc.added)
	}
	if len(fc.started) != 1 || !strings.EqualFold(fc.started[0], p.InfoHash) {
		t.Fatalf("démarrés : %v, attendu %s", fc.started, p.InfoHash)
	}
	// Une seconde fois : déjà en route.
	if err := m.SeedExisting(j.ID); err == nil {
		t.Fatal("deuxième demande acceptée")
	}
}

// v3xTorrentOf : le .torrent de la release tel que V3X le rend (annonce
// avec passkey), et sa lecture.
func v3xTorrentOf(t *testing.T, dir string) ([]byte, *torrent.Torrent) {
	t.Helper()
	ctx := context.Background()
	ex := config.Defaults().ExcludeList()
	plan, err := source.Local{}.Scan(ctx, dir, ex)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := source.Local{}.Hash(ctx, plan, ex, "V3X", func(int64, int64) {})
	if err != nil {
		t.Fatal(err)
	}
	d, _ := bencode.Decode(raw)
	root := d.Root.(bencode.Dict)
	root["announce"] = []byte("https://announce.v3x.club/pk/announce")
	onV3X, _ := bencode.Encode(root)
	p, err := torrent.Parse(onV3X)
	if err != nil {
		t.Fatal(err)
	}
	return onV3X, p
}

// seedExistingSite : un faux V3X où la release est déjà, à l'identique, sous
// la fiche id ; il compte les envois (site.uploads) et les refuse.
func seedExistingSite(t *testing.T, onV3X []byte, id string) *fakeSite {
	t.Helper()
	p, err := torrent.Parse(onV3X)
	if err != nil {
		t.Fatal(err)
	}
	site := &fakeSite{}
	site.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/d3x/match":
			json.NewEncoder(w).Encode(map[string]interface{}{"results": []map[string]interface{}{{"key": "0", "match": v3x.Match{Kind: "exact", ID: id, Name: p.Name, InfoHash: p.InfoHash, Seeders: 3}}}})
		case "/torznab/download":
			if r.URL.Query().Get("id") != id {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Write(onV3X)
		case "/api/torrents":
			site.uploads.Add(1)
			w.WriteHeader(http.StatusBadRequest)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(site.srv.Close)
	return site
}

// « Seeder la release V3X avec ces fichiers » suit le réglage des liens : le
// .torrent de la fiche est ajouté sur le dossier de liens.
func TestSeedExistingByLinks(t *testing.T) {
	parent, dir := release(t)
	liens := linksDir(t, parent)
	onV3X, p := v3xTorrentOf(t, dir)
	const id = "bbbbbbbb-0000-4000-8000-000000000002"
	site := seedExistingSite(t, onV3X, id)
	fc := newFakeClient(t)
	q, _ := client.New(client.Options{URL: fc.srv.URL})
	clients := linksEnv(site, crossseed.Target{ID: "c1", Name: "qBittorrent", Client: q, LinksDir: liens})
	eng := crossseed.New(clients)
	eng.SetLinker(realLinker)
	m := New(clients, eng, "test")

	j, err := m.Start(source.Local{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if g, _ := m.Get(j.ID); !g.Links {
		t.Fatal("drapeau liens attendu")
	}
	if err := m.SeedExisting(j.ID); err != nil {
		t.Fatal(err)
	}
	// Dès la demande, le client, son dossier et la voie sont figés : le seed
	// les lit après le téléchargement du .torrent.
	if err := m.SetClient(j.ID, "c1"); err == nil {
		t.Fatal("changement de client accepté pendant le seed")
	}
	if _, _, err := m.SetClientDir(j.ID, "/ailleurs"); err == nil {
		t.Fatal("changement de dossier accepté pendant le seed")
	}
	if err := m.SetLinks(j.ID, false); err == nil {
		t.Fatal("changement de voie accepté pendant le seed")
	}
	wait(t, func() bool {
		g, _ := m.Get(j.ID)
		return g.State == StDone && g.Seed != nil && g.Seed.Status == crossseed.StSeeding
	})
	g, _ := m.Get(j.ID)
	if g.SeedError != "" || g.SeedVia != "links" || !strings.EqualFold(g.InfoHash, p.InfoHash) {
		t.Fatalf("seed : %q via %q hash %s", g.SeedError, g.SeedVia, g.InfoHash)
	}
	lk, err := os.Stat(filepath.Join(liens, p.Name, "Film.2024.1080p.WEB.H264-D3X.mkv"))
	if err != nil {
		t.Fatalf("lien attendu : %v", err)
	}
	if orig, _ := os.Stat(filepath.Join(dir, "Film.2024.1080p.WEB.H264-D3X.mkv")); !os.SameFile(lk, orig) {
		t.Fatal("le lien n'est pas la même donnée que l'original")
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if fc.added["savepath"] != liens {
		t.Fatalf("ajout au client : %v", fc.added)
	}
}
