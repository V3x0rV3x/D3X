package publish

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/V3x0rV3x/D3X/internal/client"
	"github.com/V3x0rV3x/D3X/internal/config"
	"github.com/V3x0rV3x/D3X/internal/crossseed"
	"github.com/V3x0rV3x/D3X/internal/links"
	"github.com/V3x0rV3x/D3X/internal/pathmap"
	"github.com/V3x0rV3x/D3X/internal/source"
)

// linksEnv : comme envOf, avec les liens activés pour les publications.
func linksEnv(site *fakeSite, targets ...crossseed.Target) crossseed.Clients {
	base := envOf(site, targets...)
	return func() (*crossseed.Env, error) {
		env, err := base()
		if err == nil {
			env.Cfg.Links = config.Links{On: true, Publish: true}
		}
		return env, err
	}
}

// realLinker : de vrais liens, D3X voyant les chemins comme le client.
func realLinker(_ context.Context, _ []pathmap.Rule, base string, pairs []links.Pair, remove bool) error {
	if remove {
		return links.Remove(base, pairs)
	}
	return links.Make(base, pairs)
}

// linksDir : un dossier de liens vide à côté des releases.
func linksDir(t *testing.T, parent string) string {
	t.Helper()
	d := filepath.Join(filepath.Dir(parent), "liens")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	return d
}

func publishReady(t *testing.T, m *Manager, dir string) *Job {
	t.Helper()
	j, err := m.Start(source.Local{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	wait(t, func() bool { g, _ := m.Get(j.ID); return g.State == StReady })
	return j
}

func publishDone(t *testing.T, m *Manager, id string) *Job {
	t.Helper()
	if err := m.Publish(id, map[string]string{"categoryId": "11", "releaseName": "Film.2024.1080p.WEB.H264-D3X"}); err != nil {
		t.Fatal(err)
	}
	wait(t, func() bool {
		g, _ := m.Get(id)
		return g.State == StDone && g.Seed != nil && g.Seed.Status == crossseed.StSeeding
	})
	g, _ := m.Get(id)
	return g
}

// publishAdded : comme publishDone, sans attendre que le client ait fini de
// vérifier (le repli et le drapeau ne dépendent que de l'ajout).
func publishAdded(t *testing.T, m *Manager, id string) *Job {
	t.Helper()
	if err := m.Publish(id, map[string]string{"categoryId": "11", "releaseName": "Film.2024.1080p.WEB.H264-D3X"}); err != nil {
		t.Fatal(err)
	}
	wait(t, func() bool { g, _ := m.Get(id); return g.State == StDone })
	g, _ := m.Get(id)
	return g
}

// « Mes publications aussi » : la release publiée est rangée dans le dossier
// de liens du client, chaque fichier étant un lien physique vers l'original,
// et c'est là que le client l'ajoute.
func TestPublishByLinks(t *testing.T) {
	parent, dir := release(t)
	liens := linksDir(t, parent)
	fc := newFakeClient(t)
	site := newFakeSite(t)
	q, _ := client.New(client.Options{URL: fc.srv.URL})
	clients := linksEnv(site, crossseed.Target{ID: "c1", Name: "qBittorrent", Client: q, LinksDir: liens})
	eng := crossseed.New(clients)
	eng.SetLinker(realLinker)
	m := New(clients, eng, "test")

	j := publishReady(t, m, dir)
	if g, _ := m.Get(j.ID); !g.Links || g.LinksDir != liens {
		t.Fatalf("liens attendus dès le départ : links=%v dir=%q", g.Links, g.LinksDir)
	}
	g := publishDone(t, m, j.ID)
	if g.SeedError != "" || g.SeedVia != "links" || g.LinksNote != "" {
		t.Fatalf("seed : %q via %q note %q", g.SeedError, g.SeedVia, g.LinksNote)
	}
	lk, err := os.Stat(filepath.Join(liens, "Film.2024.1080p.WEB.H264-D3X", "Film.2024.1080p.WEB.H264-D3X.mkv"))
	if err != nil {
		t.Fatalf("lien attendu : %v", err)
	}
	orig, _ := os.Stat(filepath.Join(dir, "Film.2024.1080p.WEB.H264-D3X.mkv"))
	if !os.SameFile(lk, orig) {
		t.Fatal("le lien n'est pas la même donnée que l'original")
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if fc.added["savepath"] != liens {
		t.Fatalf("ajout au client : %v", fc.added)
	}
}

// Liens impossibles (ici : le dossier de liens n'existe pas) : repli sur les
// fichiers d'origine, raison gardée, publication seedée quand même.
func TestPublishLinksFallback(t *testing.T) {
	parent, dir := release(t)
	absent := filepath.Join(filepath.Dir(parent), "absent")
	fc := newFakeClient(t)
	site := newFakeSite(t)
	q, _ := client.New(client.Options{URL: fc.srv.URL})
	clients := linksEnv(site, crossseed.Target{ID: "c1", Name: "qBittorrent", Client: q, LinksDir: absent})
	eng := crossseed.New(clients)
	eng.SetLinker(realLinker)
	m := New(clients, eng, "test")

	j := publishReady(t, m, dir)
	g := publishAdded(t, m, j.ID)
	if g.SeedError != "" || g.SeedVia != "origin" || !strings.HasPrefix(g.LinksNote, "liens impossibles : ") || g.Links {
		t.Fatalf("repli : %q via %q note %q links %v", g.SeedError, g.SeedVia, g.LinksNote, g.Links)
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if fc.added["savepath"] != parent {
		t.Fatalf("ajout au client : %v", fc.added)
	}
}

// Le drapeau suit le client : un client sans dossier de liens le remet à faux
// et refuse de le rallumer ; le membre peut l'éteindre pour une fois.
func TestPublishLinksFlag(t *testing.T) {
	parent, dir := release(t)
	liens := linksDir(t, parent)
	fc1, fc2 := newFakeClient(t), newFakeClient(t)
	site := newFakeSite(t)
	q1, _ := client.New(client.Options{URL: fc1.srv.URL})
	q2, _ := client.New(client.Options{URL: fc2.srv.URL})
	clients := linksEnv(site,
		crossseed.Target{ID: "c1", Name: "PC", Client: q1, LinksDir: liens},
		crossseed.Target{ID: "c2", Name: "NAS", Client: q2})
	m := New(clients, crossseed.New(clients), "test")

	j := publishReady(t, m, dir)
	if g, _ := m.Get(j.ID); g.Client != "c1" || !g.Links {
		t.Fatalf("départ : client %s links %v", g.Client, g.Links)
	}
	if err := m.SetClient(j.ID, "c2"); err != nil {
		t.Fatal(err)
	}
	if g, _ := m.Get(j.ID); g.Links || g.LinksDir != "" {
		t.Fatalf("client sans dossier de liens : %+v", g)
	}
	if err := m.SetLinks(j.ID, true); err == nil || !strings.Contains(err.Error(), "dossier de liens") {
		t.Fatalf("rallumer sans dossier : %v", err)
	}
	if err := m.SetClient(j.ID, "c1"); err != nil {
		t.Fatal(err)
	}
	if g, _ := m.Get(j.ID); !g.Links {
		t.Fatal("retour au client avec dossier : drapeau attendu")
	}
	if err := m.SetLinks(j.ID, false); err != nil {
		t.Fatal(err)
	}
	g := publishAdded(t, m, j.ID)
	if g.SeedVia != "origin" || g.LinksNote != "" {
		t.Fatalf("éteint pour une fois : via %q note %q", g.SeedVia, g.LinksNote)
	}
	fc1.mu.Lock()
	defer fc1.mu.Unlock()
	if fc1.added["savepath"] != parent {
		t.Fatalf("ajout au client : %v", fc1.added)
	}
}

// Publication refusée par le site : aucun lien n'a été posé (les liens ne
// viennent qu'après l'acceptation).
func TestPublishRefusedNoLinks(t *testing.T) {
	parent, dir := release(t)
	liens := linksDir(t, parent)
	fc := newFakeClient(t)
	site := newFakeSite(t)
	site.refuse = true
	q, _ := client.New(client.Options{URL: fc.srv.URL})
	clients := linksEnv(site, crossseed.Target{ID: "c1", Name: "qBittorrent", Client: q, LinksDir: liens})
	eng := crossseed.New(clients)
	eng.SetLinker(realLinker)
	m := New(clients, eng, "test")

	j := publishReady(t, m, dir)
	if err := m.Publish(j.ID, map[string]string{"categoryId": "11", "releaseName": "x"}); err != nil {
		t.Fatal(err)
	}
	wait(t, func() bool { g, _ := m.Get(j.ID); return g.State == StReady && g.Error != "" })
	if ents, _ := os.ReadDir(liens); len(ents) != 0 {
		t.Fatalf("dossier de liens non vide : %v", ents)
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if fc.added != nil {
		t.Fatalf("rien ne devait être ajouté : %v", fc.added)
	}
}
