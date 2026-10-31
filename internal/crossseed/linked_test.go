package crossseed

import (
	"context"
	"errors"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/V3x0rV3x/D3X/internal/client"
	"github.com/V3x0rV3x/D3X/internal/config"
	"github.com/V3x0rV3x/D3X/internal/links"
	"github.com/V3x0rV3x/D3X/internal/pathmap"
	"github.com/V3x0rV3x/D3X/internal/qbit"
	"github.com/V3x0rV3x/D3X/internal/torrent"
	"github.com/V3x0rV3x/D3X/internal/v3x"
)

// linkedRig : un moteur, un faux qBittorrent dont le disque simulé a la
// release d'origine dans /data/films, et un créateur de liens qui note ce
// qu'il pose et retire. publish : le réglage « Mes publications aussi ».
type linkedRig struct {
	e     *Engine
	fq    *fakeQbit
	tg    Target
	cfg   *config.Config
	raw   []byte
	hash  string
	made  []links.Pair
	gone  []links.Pair
	bases []string
	// shrink : le lien posé vers ce fichier (suffixe) a un octet de moins sur
	// le disque simulé, pour qu'une vérification échoue (TestRemoveLinked).
	shrink string
}

func newLinkedRig(t *testing.T, publish bool) *linkedRig {
	t.Helper()
	isolatedConfig(t)
	r := &linkedRig{}
	r.raw = mkTorrent("Film.2024", map[string]int64{"Film.2024.mkv": 3_000_000, "Film.2024.nfo": 10}, "https://announce.v3x.club/pk/announce", "")
	p, _ := torrent.Parse(r.raw)
	r.hash = strings.ToLower(p.InfoHash)
	r.fq = &fakeQbit{list: map[string]*fakeTorrent{}, complete: map[string]float64{}, disk: map[string]int64{
		"/data/films/Film.2024/Film.2024.mkv": 3_000_000, "/data/films/Film.2024/Film.2024.nfo": 10,
	}}
	qs := httptest.NewServer(r.fq.handler(t))
	t.Cleanup(qs.Close)
	q, _ := client.New(client.Options{URL: qs.URL, User: "admin", Pass: "secret"})
	v := v3x.New("http://127.0.0.1:1", "cle", "D3X/test")
	r.cfg = config.Defaults()
	r.cfg.Links = config.Links{On: true, Dir: "/data/liens", Publish: publish}
	clients := oneClient(q, v, r.cfg)
	env, _ := clients()
	r.tg = env.Targets[0]
	r.e = New(clients)
	r.e.SetLinker(func(_ context.Context, _ []pathmap.Rule, base string, pairs []links.Pair, remove bool) error {
		r.fq.mu.Lock()
		defer r.fq.mu.Unlock()
		r.bases = append(r.bases, base)
		if remove {
			for _, p := range pairs {
				delete(r.fq.disk, p.Dst)
			}
			r.gone = append(r.gone, pairs...)
			return nil
		}
		for _, p := range pairs {
			r.fq.disk[p.Dst] = p.Size
			if r.shrink != "" && strings.HasSuffix(p.Dst, r.shrink) {
				r.fq.disk[p.Dst] = p.Size - 1
			}
		}
		r.made = append(r.made, pairs...)
		return nil
	})
	return r
}

func dsts(pairs []links.Pair) []string {
	var out []string
	for _, p := range pairs {
		out = append(out, p.Src+" -> "+p.Dst)
	}
	sort.Strings(out)
	return out
}

// Publication par liens : chaque fichier du .torrent devient un lien dans le
// dossier de liens, au même chemin relatif, et le torrent est ajouté sur ce
// dossier, vérifié, démarré à 100 %. La surveillance retient les liens (vue
// pendant que la publication est gardée à l'arrêt : une fois en seed, l'ajout
// sort de la surveillance).
func TestAddLinked(t *testing.T) {
	r := newLinkedRig(t, true)
	r.e.HoldPublished(time.Minute)
	hash, err := r.e.AddLinked(context.Background(), r.tg, r.cfg, "pub:1", "fiche-1", r.raw, "/data/films")
	if err != nil || hash != r.hash {
		t.Fatalf("AddLinked : %q %v", hash, err)
	}
	r.e.mu.Lock()
	w := r.e.watches[r.hash]
	var ldir string
	var nl int
	if w != nil {
		ldir, nl = w.LinksDir, len(w.Links)
	}
	r.e.mu.Unlock()
	if w == nil || ldir != "/data/liens" || nl != 2 {
		t.Fatalf("surveillance sans les liens : %q, %d liens", ldir, nl)
	}
	r.e.HoldPublished(0)
	waitFor(t, func() bool { st, ok := r.e.WatchOf(r.hash); return ok && st.Status == StSeeding })
	r.fq.mu.Lock()
	defer r.fq.mu.Unlock()
	want := []string{
		"/data/films/Film.2024/Film.2024.mkv -> /data/liens/Film.2024/Film.2024.mkv",
		"/data/films/Film.2024/Film.2024.nfo -> /data/liens/Film.2024/Film.2024.nfo",
	}
	if got := dsts(r.made); strings.Join(got, "|") != strings.Join(want, "|") || len(r.gone) != 0 {
		t.Fatalf("liens : %v, retirés %v", got, r.gone)
	}
	if r.bases[0] != "/data/liens" || r.fq.list[r.hash].SavePath != "/data/liens" {
		t.Fatalf("base %q, ajouté dans %q", r.bases[0], r.fq.list[r.hash].SavePath)
	}
}

// Rien n'est posé ni ajouté quand les liens sont impossibles d'avance, et
// l'erreur est un LinkError : l'appelant peut se replier sur l'origine.
func TestAddLinkedRefused(t *testing.T) {
	var le *LinkError

	// Réglage éteint pour les publications.
	r := newLinkedRig(t, false)
	_, err := r.e.AddLinked(context.Background(), r.tg, r.cfg, "pub:1", "fiche-1", r.raw, "/data/films")
	if !errors.As(err, &le) || !strings.Contains(le.Reason, "éteints") || len(r.made) != 0 {
		t.Fatalf("réglage éteint : %v, liens %v", err, r.made)
	}

	// Un torrent du client occupe déjà « liens/Film.2024 ».
	r = newLinkedRig(t, true)
	squat := mkTorrent("Film.2024", map[string]int64{"autre.mkv": 7}, "https://autre.invalid/announce", "AUTRE")
	sp, _ := torrent.Parse(squat)
	sh := strings.ToLower(sp.InfoHash)
	r.fq.list[sh] = &fakeTorrent{Torrent: qbit.Torrent{Hash: sh, Name: sp.Name, SavePath: "/data/liens", ContentPath: "/data/liens/Film.2024", Progress: 1, State: "stalledUP"}, raw: squat}
	_, err = r.e.AddLinked(context.Background(), r.tg, r.cfg, "pub:1", "fiche-1", r.raw, "/data/films")
	if !errors.As(err, &le) || !strings.Contains(le.Reason, "déjà l'emplacement") || len(r.made) != 0 || len(r.fq.adds) != 0 {
		t.Fatalf("racine occupée : %v, liens %v, ajouts %d", err, r.made, len(r.fq.adds))
	}

	// Le créateur de liens refuse (autre disque, lecture seule) : rien d'ajouté.
	r = newLinkedRig(t, true)
	r.e.SetLinker(func(context.Context, []pathmap.Rule, string, []links.Pair, bool) error {
		return errors.New("le dossier des liens doit être sur le même disque que tes fichiers")
	})
	_, err = r.e.AddLinked(context.Background(), r.tg, r.cfg, "pub:1", "fiche-1", r.raw, "/data/films")
	if !errors.As(err, &le) || !strings.HasPrefix(le.Reason, "liens : le dossier des liens doit") || len(r.fq.adds) != 0 {
		t.Fatalf("liens refusés : %v, ajouts %d", err, len(r.fq.adds))
	}
}

// Liens posés mais ajout refusé par le client : les liens sont retirés et
// l'erreur reste un LinkError (rien dans le client, repli possible).
func TestAddLinkedAddFails(t *testing.T) {
	r := newLinkedRig(t, true)
	r.fq.failAdd = true
	_, err := r.e.AddLinked(context.Background(), r.tg, r.cfg, "pub:1", "fiche-1", r.raw, "/data/films")
	var le *LinkError
	if !errors.As(err, &le) {
		t.Fatalf("LinkError attendu : %v", err)
	}
	r.fq.mu.Lock()
	defer r.fq.mu.Unlock()
	if len(r.made) != 2 || strings.Join(dsts(r.gone), "|") != strings.Join(dsts(r.made), "|") || r.fq.list[r.hash] != nil {
		t.Fatalf("posés %v, retirés %v, dans le client %v", r.made, r.gone, r.fq.list[r.hash] != nil)
	}
}

// Déjà dans le client : les liens posés sont retirés, ErrInClient garde son sens.
func TestAddLinkedInClient(t *testing.T) {
	r := newLinkedRig(t, true)
	p, _ := torrent.Parse(r.raw)
	r.fq.list[r.hash] = &fakeTorrent{Torrent: qbit.Torrent{Hash: r.hash, Name: p.Name, SavePath: "/data/films", ContentPath: "/data/films/Film.2024", Progress: 1, State: "stalledUP"}, raw: r.raw}
	hash, err := r.e.AddLinked(context.Background(), r.tg, r.cfg, "pub:1", "fiche-1", r.raw, "/data/films")
	if !errors.Is(err, ErrInClient) || hash != r.hash {
		t.Fatalf("ErrInClient attendu : %q %v", hash, err)
	}
	r.fq.mu.Lock()
	defer r.fq.mu.Unlock()
	if strings.Join(dsts(r.gone), "|") != strings.Join(dsts(r.made), "|") || len(r.made) == 0 {
		t.Fatalf("posés %v, retirés %v", r.made, r.gone)
	}
}

// Vérification qui échoue après un ajout par liens, puis « Retirer » : le
// torrent sort du client et les liens sont enlevés, l'original n'est pas
// touché (ici : seul le disque simulé sous /data/liens bouge).
func TestRemoveLinked(t *testing.T) {
	r := newLinkedRig(t, true)
	// Le lien vers la vidéo n'a pas la bonne taille sur le disque simulé
	// (fichier modifié entre le hachage et la vérification) : la vérification
	// tombe sous 100 %, le torrent reste arrêté.
	r.shrink = "Film.2024.mkv"
	if _, err := r.e.AddLinked(context.Background(), r.tg, r.cfg, "pub:1", "fiche-1", r.raw, "/data/films"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { st, ok := r.e.WatchOf(r.hash); return ok && st.Status == StFailed })
	if err := r.e.Remove("pub:1"); err != nil {
		t.Fatalf("Remove : %v", err)
	}
	r.fq.mu.Lock()
	defer r.fq.mu.Unlock()
	if len(r.fq.deleted) != 1 || r.fq.deleted[0] != r.hash {
		t.Fatalf("retirés du client : %v", r.fq.deleted)
	}
	if strings.Join(dsts(r.gone), "|") != strings.Join(dsts(r.made), "|") || len(r.gone) != 2 {
		t.Fatalf("liens posés %v, retirés %v", r.made, r.gone)
	}
	if r.fq.disk["/data/films/Film.2024/Film.2024.nfo"] != 10 {
		t.Fatal("l'original a été touché")
	}
	st, _ := r.e.WatchOf(r.hash)
	if st.Status != StRemoved || !strings.Contains(st.Detail, "liens retirés") {
		t.Fatalf("après retrait : %+v", st)
	}
}

// Cross-seed « noms différents » par liens : un torrent du client sans chemin
// (client qui ne le donne pas) n'occupe aucun emplacement ; un vrai occupant
// refuse toujours l'ajout. Le créateur de liens échoue exprès : l'atteindre
// prouve que le contrôle d'emplacement est passé.
func TestAddLinkedRenamedIgnoresEmptyContentPath(t *testing.T) {
	isolatedConfig(t)
	raw := mkTorrent("Occupe", map[string]int64{"X.mkv": 7}, "https://announce.v3x.club/pk/announce", "")
	p, err := torrent.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	e := New(nil)
	tg := Target{ID: "q", LinksDir: "/data/liens"}
	reached := 0
	link := func(_ context.Context, _ []pathmap.Rule, _ string, _ []links.Pair, _ bool) error {
		reached++
		return errors.New("disque plein")
	}
	pairs := []client.Rename{{Index: 0, From: "Occupe/X.mkv", To: "Autre Copie/x.mkv"}}
	r := &Row{Key: "k", Match: &v3x.Match{ID: "id"}}
	st, detail := e.addLinked(context.Background(), tg, config.Defaults(), r, link, raw, p, "/data/autre", pairs, []client.Torrent{{Name: "sans chemin", ContentPath: ""}})
	if reached != 1 || st != StRenamed || detail != "liens : disque plein" {
		t.Fatalf("chemin vide : %d appel(s), %s, %q", reached, st, detail)
	}
	st, detail = e.addLinked(context.Background(), tg, config.Defaults(), r, link, raw, p, "/data/autre", pairs, []client.Torrent{{ContentPath: ""}, {ContentPath: "/data/liens/Occupe"}})
	if reached != 1 || st != StRenamed || !strings.Contains(detail, "déjà l'emplacement") {
		t.Fatalf("occupé : %d appel(s), %s, %q", reached, st, detail)
	}
}
