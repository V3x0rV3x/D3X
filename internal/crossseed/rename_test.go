package crossseed

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/V3x0rV3x/D3X/internal/client"
	"github.com/V3x0rV3x/D3X/internal/config"
	"github.com/V3x0rV3x/D3X/internal/links"
	"github.com/V3x0rV3x/D3X/internal/pathmap"
	"github.com/V3x0rV3x/D3X/internal/qbit"
	"github.com/V3x0rV3x/D3X/internal/torrent"
	"github.com/V3x0rV3x/D3X/internal/v3x"
)

func files(kv ...interface{}) []torrent.File {
	var out []torrent.File
	for i := 0; i < len(kv); i += 2 {
		out = append(out, torrent.File{Path: kv[i].(string), Size: int64(kv[i+1].(int)), Index: i / 2})
	}
	return out
}

func TestPlanRenames(t *testing.T) {
	// Seule la racine diffère.
	p, err := planRenames(files("Film.2020-GRP/Film.mkv", 100, "Film.2020-GRP/Film.nfo", 5), files("Ma Copie/Film.nfo", 5, "Ma Copie/Film.mkv", 100))
	if err != nil || p[0].To != "Ma Copie/Film.mkv" || p[1].To != "Ma Copie/Film.nfo" || p[0].Index != 0 || p[1].Index != 1 {
		t.Fatalf("%v %+v", err, p)
	}
	// Tailles identiques : l'ordre des chemins départage, le nom d'abord.
	p, _ = planRenames(files("S/E02.mkv", 50, "S/E01.mkv", 50, "S/extra.nfo", 50), files("L/extra.nfo", 50, "L/Show.S01E01.mkv", 50, "L/Show.S01E02.mkv", 50))
	want := map[string]string{"S/E01.mkv": "L/Show.S01E01.mkv", "S/E02.mkv": "L/Show.S01E02.mkv", "S/extra.nfo": "L/extra.nfo"}
	for _, r := range p {
		if want[r.From] != r.To {
			t.Fatalf("%s -> %s, attendu %s", r.From, r.To, want[r.From])
		}
	}
	// Mono-fichier.
	if p, err = planRenames(files("Movie.mkv", 10), files("Film (2020).mkv", 10)); err != nil || p[0].To != "Film (2020).mkv" {
		t.Fatalf("%v %+v", err, p)
	}
	if _, err = planRenames(files("a", 1, "b", 2), files("a", 1)); err == nil {
		t.Fatal("nombre de fichiers différent accepté")
	}
	if _, err = planRenames(files("a", 1, "b", 2), files("a", 1, "b", 3)); err == nil {
		t.Fatal("tailles différentes acceptées")
	}
}

func TestRenameSafe(t *testing.T) {
	local := files("L/a.mkv", 1, "L/b.mkv", 2)
	absent := func(string) (bool, bool, error) { return false, true, nil }
	// Noms croisés : un nom V3X est celui d'un autre fichier de la copie.
	crossed := []client.Rename{{Index: 0, From: "L/b.mkv", To: "L/a.mkv"}, {Index: 1, From: "L/a.mkv", To: "L/b.mkv"}}
	if err := renameSafe(crossed, local, "/data", "/data/L", nil, absent); err == nil || !strings.Contains(err.Error(), "croisés") {
		t.Fatalf("noms croisés : %v", err)
	}
	// Casse seule : refusée (sur un disque qui distingue la casse, autre fichier).
	if err := renameSafe([]client.Rename{{Index: 0, From: "L/A.MKV", To: "L/a.mkv"}}, local, "/data", "/data/L", nil, absent); err == nil || !strings.Contains(err.Error(), "majuscules") {
		t.Fatalf("casse seule : %v", err)
	}
	if err := renameSafe([]client.Rename{{Index: 0, From: "L/B.MKV", To: "L/a.mkv"}}, local, "/data", "/data/L", nil, absent); err == nil {
		t.Fatal("nom croisé à la casse près accepté")
	}
	pairs := []client.Rename{{Index: 0, From: "V3X.Root/a.mkv", To: "L/a.mkv"}, {Index: 1, From: "V3X.Root/b.mkv", To: "L/b.mkv"}}
	// Un autre torrent du client est rangé à l'emplacement de la racine V3X.
	other := []client.Torrent{{ContentPath: "/data/L"}, {ContentPath: "/data/V3X.Root"}}
	if err := renameSafe(pairs, local, "/data", "/data/L", other, absent); err == nil || !strings.Contains(err.Error(), "autre torrent") {
		t.Fatalf("emplacement occupé : %v", err)
	}
	// Un torrent sans dossier racine rangé dans le même dossier : son
	// ContentPath est le dossier d'enregistrement, pas un emplacement de
	// release ; il ne bloque pas (YataReborn, 27/11). Ni un torrent sans
	// chemin connu.
	noRoot := []client.Torrent{{ContentPath: "/data", SavePath: "/data"}, {ContentPath: ""}}
	if err := renameSafe(pairs, local, "/data", "/data/L", noRoot, absent); err != nil {
		t.Fatalf("torrent sans dossier racine : %v", err)
	}
	// Mais un vrai dossier de release au même endroit bloque toujours.
	if err := renameSafe(pairs, local, "/data", "/data/L", []client.Torrent{{ContentPath: "/data/V3X.Root", SavePath: "/data"}}, absent); err == nil || !strings.Contains(err.Error(), "autre torrent") {
		t.Fatalf("emplacement occupé avec SavePath : %v", err)
	}
	// Les torrents qui partagent ta copie ne gênent pas ; dossier absent : ses
	// fichiers ne sont même pas demandés.
	var asked []string
	count := func(rel string) (bool, bool, error) { asked = append(asked, rel); return false, true, nil }
	if err := renameSafe(pairs, local, "/data", "/data/L", other[:1], count); err != nil || len(asked) != 1 || asked[0] != "V3X.Root" {
		t.Fatalf("%v, demandés : %v", err, asked)
	}
	// Présent sur le disque : refus.
	present := func(rel string) (bool, bool, error) { return rel == "V3X.Root", true, nil }
	if err := renameSafe(pairs, local, "/data", "/data/L", nil, present); err == nil || !strings.Contains(err.Error(), "disque") {
		t.Fatalf("présent : %v", err)
	}
	// Disque impossible à vérifier : refus, jamais d'ajout à l'aveugle.
	unknown := func(string) (bool, bool, error) { return false, false, nil }
	if err := renameSafe(pairs, local, "/data", "/data/L", nil, unknown); err == nil || !strings.Contains(err.Error(), "ne peut pas vérifier") {
		t.Fatalf("inconnu : %v", err)
	}
	// Disque vu par D3X : vérification locale.
	dir := t.TempDir()
	if err := renameSafe(pairs, local, dir, filepath.Join(dir, "L"), nil, diskLookup(context.Background(), nil, dir, nil)); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(dir, "V3X.Root"), 0o755)
	if err := renameSafe(pairs, local, dir, filepath.Join(dir, "L"), nil, diskLookup(context.Background(), nil, dir, nil)); err == nil || !strings.Contains(err.Error(), "disque") {
		t.Fatalf("dossier présent : %v", err)
	}
}

// De bout en bout : la release V3X arrive sous ses noms, D3X l'ajoute à
// l'arrêt, renomme ses fichiers comme ta copie, vérifie, puis la démarre.
func TestRenamedApply(t *testing.T) {
	isolatedConfig(t)

	// Ta copie, rangée sous tes noms, et la même release sur V3X.
	mineFiles := map[string]int64{"film.mkv": 3_000_000_000, "film.nfo": 4_000}
	v3xFiles := map[string]int64{"Film.2020.1080p.WEB-GRP.mkv": 3_000_000_000, "Film.2020.1080p.WEB-GRP.nfo": 4_000}
	copyRaw := mkTorrent("Ma Copie", mineFiles, "https://autre.invalid/announce", "AUTRE")
	v3xRaw := mkTorrent("Film.2020.1080p.WEB-GRP", v3xFiles, "https://announce.v3x.club/pk/announce", "")
	// Une deuxième : sa racine V3X est déjà l'emplacement d'un autre torrent.
	copy2 := mkTorrent("Autre Copie", map[string]int64{"x.mkv": 1_500_000_000}, "https://autre.invalid/announce", "AUTRE")
	v3x2 := mkTorrent("Occupe", map[string]int64{"X.mkv": 1_500_000_000}, "https://announce.v3x.club/pk/announce", "")
	squat := mkTorrent("Occupe", map[string]int64{"autre.mkv": 7}, "https://autre.invalid/announce", "AUTRE")
	pv, _ := torrent.Parse(v3xRaw)
	pv2, _ := torrent.Parse(v3x2)

	fq := &fakeQbit{list: map[string]*fakeTorrent{}, complete: map[string]float64{}, disk: map[string]int64{
		"/data/autre/Ma Copie/film.mkv": 3_000_000_000, "/data/autre/Ma Copie/film.nfo": 4_000,
		"/data/autre/Autre Copie/x.mkv": 1_500_000_000, "/data/autre/Occupe/autre.mkv": 7,
	}}
	for _, raw := range [][]byte{copyRaw, copy2, squat} {
		p, _ := torrent.Parse(raw)
		h := strings.ToLower(p.InfoHash)
		fq.list[h] = &fakeTorrent{Torrent: qbit.Torrent{
			Hash: h, Name: p.Name, SavePath: "/data/autre", ContentPath: "/data/autre/" + p.Name, Progress: 1, State: "stalledUP",
			Size: p.Total, TotalSize: p.Total, MagnetURI: "magnet:?xt=urn:btih:" + h + "&tr=https%3A%2F%2Fautre.invalid%2Fannounce",
		}, raw: raw}
	}
	qs := httptest.NewServer(fq.handler(t))
	defer qs.Close()
	vs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/me":
			io.WriteString(w, `{"username":"test","ratio":2.5,"hitAndRun":0}`)
		case "/api/d3x/match":
			var in struct{ Items []v3x.MatchItem }
			json.NewDecoder(r.Body).Decode(&in)
			type res struct {
				Key   string     `json:"key"`
				Match *v3x.Match `json:"match"`
			}
			var out []res
			for _, it := range in.Items {
				var m *v3x.Match
				switch it.SizeHash {
				case pv.SizeHash:
					m = &v3x.Match{Kind: "renamed", ID: "33333333-3333-3333-3333-333333333333", Name: pv.Name, InfoHash: pv.InfoHash, Seeders: 1, Size: pv.Total}
				case pv2.SizeHash:
					m = &v3x.Match{Kind: "renamed", ID: "44444444-4444-4444-4444-444444444444", Name: pv2.Name, InfoHash: pv2.InfoHash, Seeders: 1, Size: pv2.Total}
				}
				out = append(out, res{Key: it.Key, Match: m})
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"results": out})
		case "/torznab/download":
			switch r.URL.Query().Get("id") {
			case "33333333-3333-3333-3333-333333333333":
				w.Write(v3xRaw)
			case "44444444-4444-4444-4444-444444444444":
				w.Write(v3x2)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer vs.Close()

	q, _ := client.New(client.Options{URL: qs.URL, User: "admin", Pass: "secret"})
	v := v3x.New(vs.URL, "cle", "D3X/test")
	cfg := config.Defaults()
	e := New(oneClient(q, v, cfg))
	if err := e.StartScan(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return !e.Scanning() })
	var keys []string
	for _, r := range e.Rows() {
		if r.Status == StRenamed {
			if !r.CanAdd {
				t.Fatalf("%s : pas ajoutable avec qBittorrent", r.Name)
			}
			keys = append(keys, r.Key)
		}
	}
	if len(keys) != 2 {
		t.Fatalf("%d lignes « noms différents », 2 attendues", len(keys))
	}
	if n, err := e.Apply(keys); err != nil || n != 2 {
		t.Fatalf("%d %v", n, err)
	}
	h := strings.ToLower(pv.InfoHash)
	waitFor(t, func() bool { st, ok := e.WatchOf(h); return ok && st.Status == StSeeding })
	// La deuxième est traitée (et refusée) dans la même file.
	waitFor(t, func() bool {
		for _, r := range e.Rows() {
			if r.Name == "Autre Copie" && strings.Contains(r.Detail, "autre torrent") {
				return true
			}
		}
		return false
	})

	fq.mu.Lock()
	defer fq.mu.Unlock()
	names := fq.names[h]
	got := map[string]bool{}
	for _, n := range names {
		got[n] = true
	}
	if !got["Ma Copie/film.mkv"] || !got["Ma Copie/film.nfo"] || len(fq.renames) != 2 {
		t.Fatalf("noms dans le client : %v, renommages %v", names, fq.renames)
	}
	if fq.list[h].SavePath != "/data/autre" {
		t.Fatalf("ajouté dans %s", fq.list[h].SavePath)
	}
	// La deuxième n'a même pas été ajoutée.
	if fq.list[strings.ToLower(pv2.InfoHash)] != nil {
		t.Fatal("release ajoutée alors que sa racine est l'emplacement d'un autre torrent")
	}
	for _, r := range e.Rows() {
		if r.Name == "Autre Copie" && (r.Status != StRenamed || !strings.Contains(r.Detail, "autre torrent")) {
			t.Fatalf("Autre Copie : %s %s", r.Status, r.Detail)
		}
	}
}

func TestWrappedRoot(t *testing.T) {
	// Fichier seul rangé dans un dossier en plus (Terminator 3 QTZ, YataReborn).
	if r := wrappedRoot([]client.Rename{{From: "Le Soulèvement des Machines (2003)/T3.mkv", To: "T3.mkv"}}); r != "Le Soulèvement des Machines (2003)" {
		t.Fatalf("fichier seul : %q", r)
	}
	if r := wrappedRoot([]client.Rename{{From: "D/Copie/a.mkv", To: "Copie/a.mkv"}, {From: "D/Copie/a.nfo", To: "Copie/a.nfo"}}); r != "D" {
		t.Fatalf("dossier : %q", r)
	}
	for _, p := range [][]client.Rename{
		{{From: "D/a.mkv", To: "b.mkv"}},                                 // nom changé
		{{From: "a.mkv", To: "a.mkv"}},                                   // pas de racine
		{{From: "D/a.mkv", To: "a.mkv"}, {From: "E/b.mkv", To: "b.mkv"}}, // deux racines
		{{From: "D/Copie/a.mkv", To: "Autre/a.mkv"}},                     // dossier renommé
	} {
		if r := wrappedRoot(p); r != "" {
			t.Fatalf("%+v : %q", p, r)
		}
	}
}

// La fiche V3X range seulement ta copie dans un dossier en plus : D3X
// l'ajoute à qBittorrent sans ce dossier, sans aucun renommage.
func TestWrappedRootApply(t *testing.T) {
	isolatedConfig(t)

	mine := map[string]int64{"film.mkv": 2_000_000_000, "film.nfo": 3_000}
	copyRaw := mkTorrent("Ma Copie", mine, "https://autre.invalid/announce", "AUTRE")
	v3xRaw := mkTorrent("Dossier En Plus", map[string]int64{"Ma Copie/film.mkv": 2_000_000_000, "Ma Copie/film.nfo": 3_000}, "https://announce.v3x.club/pk/announce", "")
	pv, _ := torrent.Parse(v3xRaw)
	pc, _ := torrent.Parse(copyRaw)
	hc := strings.ToLower(pc.InfoHash)

	fq := &fakeQbit{list: map[string]*fakeTorrent{}, complete: map[string]float64{}, disk: map[string]int64{
		"/data/autre/Ma Copie/film.mkv": 2_000_000_000, "/data/autre/Ma Copie/film.nfo": 3_000,
	}}
	fq.list[hc] = &fakeTorrent{Torrent: qbit.Torrent{
		Hash: hc, Name: pc.Name, SavePath: "/data/autre", ContentPath: "/data/autre/Ma Copie", Progress: 1, State: "stalledUP",
		Size: pc.Total, TotalSize: pc.Total, MagnetURI: "magnet:?xt=urn:btih:" + hc + "&tr=https%3A%2F%2Fautre.invalid%2Fannounce",
	}, raw: copyRaw}
	qs := httptest.NewServer(fq.handler(t))
	defer qs.Close()
	const id = "66666666-6666-6666-6666-666666666666"
	vs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/me":
			io.WriteString(w, `{"username":"test","ratio":2.5,"hitAndRun":0}`)
		case "/api/d3x/match":
			var in struct{ Items []v3x.MatchItem }
			json.NewDecoder(r.Body).Decode(&in)
			type res struct {
				Key   string     `json:"key"`
				Match *v3x.Match `json:"match"`
			}
			var out []res
			for _, it := range in.Items {
				var m *v3x.Match
				if it.SizeHash == pv.SizeHash {
					m = &v3x.Match{Kind: "renamed", ID: id, Name: pv.Name, InfoHash: pv.InfoHash, Seeders: 1, Size: pv.Total}
				}
				out = append(out, res{Key: it.Key, Match: m})
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"results": out})
		case "/torznab/download":
			w.Write(v3xRaw)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer vs.Close()

	q, _ := client.New(client.Options{URL: qs.URL, User: "admin", Pass: "secret"})
	e := New(oneClient(q, v3x.New(vs.URL, "cle", "D3X/test"), config.Defaults()))
	if err := e.StartScan(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return !e.Scanning() })
	var keys []string
	for _, r := range e.Rows() {
		if r.Status == StRenamed {
			keys = append(keys, r.Key)
		}
	}
	if len(keys) != 1 {
		t.Fatalf("%d lignes « noms différents », 1 attendue", len(keys))
	}
	if n, err := e.Apply(keys); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	h := strings.ToLower(pv.InfoHash)
	waitFor(t, func() bool { st, ok := e.WatchOf(h); return ok && st.Status == StSeeding })

	fq.mu.Lock()
	defer fq.mu.Unlock()
	if len(fq.renames) != 0 {
		t.Fatalf("renommages : %v", fq.renames)
	}
	last := fq.adds[len(fq.adds)-1]
	if last["contentLayout"] != "NoSubfolder" || last["root_folder"] != "false" || last["savepath"] != "/data/autre" {
		t.Fatalf("ajout : %v", last)
	}
	got := map[string]bool{}
	for _, n := range fq.names[h] {
		got[n] = true
	}
	if !got["Ma Copie/film.mkv"] || !got["Ma Copie/film.nfo"] {
		t.Fatalf("noms dans le client : %v", fq.names[h])
	}
}

// Renommer serait risqué (la racine V3X est l'emplacement d'un autre
// torrent) : avec les liens physiques choisis, la release va dans le dossier
// des liens, ses fichiers liés à ceux de ta copie, sans renommage.
func TestLinkedApply(t *testing.T) {
	isolatedConfig(t)

	copyRaw := mkTorrent("Autre Copie", map[string]int64{"x.mkv": 1_500_000_000}, "https://autre.invalid/announce", "AUTRE")
	v3xRaw := mkTorrent("Occupe", map[string]int64{"X.mkv": 1_500_000_000}, "https://announce.v3x.club/pk/announce", "")
	squat := mkTorrent("Occupe", map[string]int64{"autre.mkv": 7}, "https://autre.invalid/announce", "AUTRE")
	pv, _ := torrent.Parse(v3xRaw)

	fq := &fakeQbit{list: map[string]*fakeTorrent{}, complete: map[string]float64{}, disk: map[string]int64{
		"/data/autre/Autre Copie/x.mkv": 1_500_000_000, "/data/autre/Occupe/autre.mkv": 7,
	}}
	for _, raw := range [][]byte{copyRaw, squat} {
		p, _ := torrent.Parse(raw)
		h := strings.ToLower(p.InfoHash)
		fq.list[h] = &fakeTorrent{Torrent: qbit.Torrent{
			Hash: h, Name: p.Name, SavePath: "/data/autre", ContentPath: "/data/autre/" + p.Name, Progress: 1, State: "stalledUP",
			Size: p.Total, TotalSize: p.Total, MagnetURI: "magnet:?xt=urn:btih:" + h + "&tr=https%3A%2F%2Fautre.invalid%2Fannounce",
		}, raw: raw}
	}
	qs := httptest.NewServer(fq.handler(t))
	defer qs.Close()
	const id = "55555555-5555-5555-5555-555555555555"
	vs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/me":
			io.WriteString(w, `{"username":"test","ratio":2.5,"hitAndRun":0}`)
		case "/api/d3x/match":
			var in struct{ Items []v3x.MatchItem }
			json.NewDecoder(r.Body).Decode(&in)
			var out []map[string]interface{}
			for _, it := range in.Items {
				res := map[string]interface{}{"key": it.Key, "match": nil}
				if it.SizeHash == pv.SizeHash {
					res["match"] = v3x.Match{Kind: "renamed", ID: id, Name: pv.Name, InfoHash: pv.InfoHash, Seeders: 1, Size: pv.Total}
				}
				out = append(out, res)
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"results": out})
		case "/torznab/download":
			w.Write(v3xRaw)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer vs.Close()

	q, _ := client.New(client.Options{URL: qs.URL, User: "admin", Pass: "secret"})
	v := v3x.New(vs.URL, "cle", "D3X/test")
	cfg := config.Defaults()
	cfg.Links = config.Links{On: true, Dir: "/data/liens"}
	e := New(oneClient(q, v, cfg))
	var made []links.Pair
	e.SetLinker(func(_ context.Context, _ []pathmap.Rule, base string, pairs []links.Pair, remove bool) error {
		if base != "/data/liens" || remove {
			t.Errorf("liens : %s %v", base, remove)
		}
		fq.mu.Lock()
		defer fq.mu.Unlock()
		for _, p := range pairs {
			fq.disk[p.Dst] = p.Size // le lien existe désormais sur le disque simulé
		}
		made = append(made, pairs...)
		return nil
	})
	if err := e.StartScan(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return !e.Scanning() })
	var key string
	for _, r := range e.Rows() {
		if r.Status == StRenamed && r.CanAdd {
			key = r.Key
		}
	}
	if key == "" {
		t.Fatal("ligne « noms différents » absente")
	}
	if _, err := e.Apply([]string{key}); err != nil {
		t.Fatal(err)
	}
	h := strings.ToLower(pv.InfoHash)
	waitFor(t, func() bool { st, ok := e.WatchOf(h); return ok && st.Status == StSeeding })

	fq.mu.Lock()
	defer fq.mu.Unlock()
	if len(made) != 1 || made[0].Src != "/data/autre/Autre Copie/x.mkv" || made[0].Dst != "/data/liens/Occupe/X.mkv" || made[0].Size != 1_500_000_000 {
		t.Fatalf("liens : %+v", made)
	}
	if fq.list[h].SavePath != "/data/liens" || len(fq.renames) != 0 {
		t.Fatalf("ajouté dans %s, renommages %v", fq.list[h].SavePath, fq.renames)
	}
}

// Copie renommée dans le client (YataReborn, 24/11/26) : son .torrent d'origine
// dit « Film.2022.GRP », mais ses fichiers portent maintenant les noms de la
// fiche V3X. La ligne est un ajout ordinaire, sans aucun renommage.
func TestRenamedInClientIsAdd(t *testing.T) {
	isolatedConfig(t)

	copyRaw := mkTorrent("Film.2022.GRP", map[string]int64{"Film.2022.GRP.mkv": 3_000_000_000}, "https://autre.invalid/announce", "AUTRE")
	v3xRaw := mkTorrent("Film.2.2022.GRP", map[string]int64{"Film.2.2022.GRP.mkv": 3_000_000_000}, "https://announce.v3x.club/pk/announce", "")
	pc, _ := torrent.Parse(copyRaw)
	pv, _ := torrent.Parse(v3xRaw)
	h := strings.ToLower(pc.InfoHash)
	const id = "55555555-5555-4555-8555-555555555555"

	fq := &fakeQbit{
		list:     map[string]*fakeTorrent{},
		complete: map[string]float64{},
		names:    map[string]map[int]string{h: {0: "Film.2.2022.GRP/Film.2.2022.GRP.mkv"}},
		disk:     map[string]int64{"/data/autre/Film.2.2022.GRP/Film.2.2022.GRP.mkv": 3_000_000_000},
	}
	fq.list[h] = &fakeTorrent{Torrent: qbit.Torrent{
		Hash: h, Name: pc.Name, SavePath: "/data/autre", ContentPath: "/data/autre/Film.2.2022.GRP", Progress: 1, State: "stalledUP",
		Size: pc.Total, TotalSize: pc.Total, MagnetURI: "magnet:?xt=urn:btih:" + h + "&tr=https%3A%2F%2Fautre.invalid%2Fannounce",
	}, raw: copyRaw}
	qs := httptest.NewServer(fq.handler(t))
	defer qs.Close()

	var exactAsked int
	vs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/me":
			io.WriteString(w, `{"username":"test","ratio":2.5,"hitAndRun":0}`)
		case "/api/d3x/match":
			var in struct{ Items []v3x.MatchItem }
			json.NewDecoder(r.Body).Decode(&in)
			type res struct {
				Key   string     `json:"key"`
				Match *v3x.Match `json:"match"`
			}
			var out []res
			for _, it := range in.Items {
				var m *v3x.Match
				switch {
				case it.ContentHash == pv.ContentHash:
					exactAsked++
					m = &v3x.Match{Kind: "exact", ID: id, Name: pv.Name, InfoHash: pv.InfoHash, Seeders: 1, Size: pv.Total}
				case it.SizeHash == pv.SizeHash:
					m = &v3x.Match{Kind: "renamed", ID: id, Name: pv.Name, InfoHash: pv.InfoHash, Seeders: 1, Size: pv.Total}
				}
				out = append(out, res{Key: it.Key, Match: m})
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"results": out})
		case "/torznab/download":
			w.Write(v3xRaw)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer vs.Close()

	q, _ := client.New(client.Options{URL: qs.URL, User: "admin", Pass: "secret"})
	v := v3x.New(vs.URL, "cle", "D3X/test")
	e := New(oneClient(q, v, config.Defaults()))
	if err := e.StartScan(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return !e.Scanning() })
	var key string
	for _, r := range e.Rows() {
		if r.Name == pc.Name {
			if r.Status != StAdd || r.Match == nil || r.Match.ID != id {
				t.Fatalf("ligne %s : %s (%s), « À ajouter » attendu", r.Name, r.Status, r.Detail)
			}
			key = r.Key
		}
	}
	if key == "" || exactAsked == 0 {
		t.Fatalf("ligne absente ou noms actuels jamais comparés (%d)", exactAsked)
	}
	if n, err := e.Apply([]string{key}); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	hv := strings.ToLower(pv.InfoHash)
	waitFor(t, func() bool { st, ok := e.WatchOf(hv); return ok && st.Status == StSeeding })

	fq.mu.Lock()
	defer fq.mu.Unlock()
	if len(fq.renames) != 0 {
		t.Fatalf("renommages inattendus : %v", fq.renames)
	}
	if fq.list[hv] == nil || fq.list[hv].SavePath != "/data/autre" {
		t.Fatalf("release V3X absente ou mal rangée : %+v", fq.list[hv])
	}
}
