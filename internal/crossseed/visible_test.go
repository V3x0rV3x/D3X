package crossseed

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/V3x0rV3x/D3X/internal/client"
	"github.com/V3x0rV3x/D3X/internal/config"
	"github.com/V3x0rV3x/D3X/internal/links"
	"github.com/V3x0rV3x/D3X/internal/pathmap"
	"github.com/V3x0rV3x/D3X/internal/torrent"
	"github.com/V3x0rV3x/D3X/internal/v3x"
)

// Le client dit ce qu'il voit sur son disque : un « non » net est retenu, un
// client qui ne sait pas répondre laisse passer.
func TestUnseen(t *testing.T) {
	fq := &fakeQbit{list: map[string]*fakeTorrent{}, complete: map[string]float64{}, disk: map[string]int64{
		"/data/films/A/A.mkv": 1, "/data/films/A/A.nfo": 1,
	}}
	qs := httptest.NewServer(fq.handler(t))
	defer qs.Close()
	q, _ := client.New(client.Options{URL: qs.URL, User: "admin", Pass: "secret"})
	ctx := context.Background()
	if p := Unseen(ctx, q, []string{"/data/films/A/A.mkv", "/data/films/A/A.nfo"}); p != "" {
		t.Fatalf("vu : %q", p)
	}
	if p := Unseen(ctx, q, []string{"/data/films/B/B.mkv"}); p != "/data/films/B/B.mkv" {
		t.Fatalf("absent : %q", p)
	}
	// Le dernier fichier aussi est demandé.
	if p := Unseen(ctx, q, []string{"/data/films/A/A.mkv", "/data/films/A/A.nfo", "/data/films/A/B.srt"}); p != "/data/films/A/B.srt" {
		t.Fatalf("dernier : %q", p)
	}
	// Disque qui ne se liste pas : on ne sait pas, on laisse passer.
	fq.mu.Lock()
	fq.disk = map[string]int64{}
	fq.mu.Unlock()
	if p := Unseen(ctx, q, []string{"/data/films/B/B.mkv"}); p != "" {
		t.Fatalf("sans réponse : %q", p)
	}
}

// meptalon, 9/11/26 : la Médiathèque voit le fichier (D3X l'a lu, morceaux
// identiques), le client ne le voit pas (autre conteneur, dossier monté
// ailleurs). Rien n'est ajouté, la ligne reste à ajouter et dit quoi vérifier.
func TestLibraryClientDoesNotSee(t *testing.T) {
	isolatedConfig(t)
	old := libMinSize
	libMinSize = 100_000
	t.Cleanup(func() { libMinSize = old })

	work := t.TempDir()
	lib := filepath.Join(work, "medias")
	film := fill(3_000_000, 1)
	write := func(p string, b []byte) {
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(lib, "Film (2020)", "Film (2020).mkv"), film)
	rel := filepath.Join(work, "v3x")
	write(filepath.Join(rel, "Film.2020.1080p.WEB-GRP", "Film.2020.1080p.WEB-GRP.mkv"), film)
	raw := hashed(t, filepath.Join(rel, "Film.2020.1080p.WEB-GRP"))
	const id = "11111111-1111-4111-8111-111111111111"
	pf, _ := torrent.Parse(raw)

	// Le disque du client : /lib existe, mais pas le dossier du film.
	fq := &fakeQbit{list: map[string]*fakeTorrent{}, complete: map[string]float64{}, disk: map[string]int64{
		"/lib/Autre (2019)/Autre (2019).mkv": 2_000_000,
	}}
	qs := httptest.NewServer(fq.handler(t))
	defer qs.Close()
	vs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/me":
			io.WriteString(w, `{"username":"test","ratio":2.5,"hitAndRun":0}`)
		case "/api/d3x/match-files":
			var in struct{ Sizes []int64 }
			json.NewDecoder(r.Body).Decode(&in)
			out := map[string][]v3x.FileMatch{}
			for _, s := range in.Sizes {
				if s == pf.Total {
					k := strconv.FormatInt(s, 10)
					out[k] = append(out[k], v3x.FileMatch{ID: id, Name: pf.Name, InfoHash: pf.InfoHash, Size: pf.Total, FileCount: len(pf.Files)})
				}
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"matches": out})
		case "/torznab/download":
			w.Write(raw)
		case "/api/d3x/match":
			io.WriteString(w, `{"results":[]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer vs.Close()

	q, _ := client.New(client.Options{URL: qs.URL, User: "admin", Pass: "secret"})
	e := New(oneClient(q, v3x.New(vs.URL, "cle", "D3X/test"), config.Defaults()))
	e.SetLibraryIO(testLib{root: lib})
	if err := e.StartLibrary([]config.LibFolder{{Path: lib, Source: "local"}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { st := e.Library(); return st.Scan.Phase == "done" || st.Scan.Phase == "error" })
	rows := e.LibraryRows()
	if len(rows) != 1 || rows[0].Status != StAdd {
		t.Fatalf("analyse : %+v", rows)
	}
	key := rows[0].Key
	if n, err := e.ApplyLibrary([]string{key}); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	var got LibRow
	waitFor(t, func() bool {
		for _, r := range e.LibraryRows() {
			if r.Key == key && strings.Contains(r.Detail, "ne trouve pas") {
				got = r
				return true
			}
		}
		return false
	})
	if got.Status != StAdd || !strings.Contains(got.Detail, "/lib/Film (2020)/Film (2020).mkv") {
		t.Fatalf("ligne : %s %s", got.Status, got.Detail)
	}
	fq.mu.Lock()
	defer fq.mu.Unlock()
	if len(fq.list) != 0 {
		t.Fatalf("%d torrent(s) ajouté(s) malgré tout", len(fq.list))
	}
}

// Par liens : D3X pose les liens sur son disque, mais le client ne voit pas
// le dossier de liens. Les liens sont retirés, rien n'entre dans le client, et
// l'erreur est un LinkError (Publier se replie sur l'origine).
func TestAddLinkedClientDoesNotSee(t *testing.T) {
	r := newLinkedRig(t, true)
	r.e.SetLinker(func(_ context.Context, _ []pathmap.Rule, _ string, pairs []links.Pair, remove bool) error {
		r.fq.mu.Lock()
		defer r.fq.mu.Unlock()
		if remove {
			r.gone = append(r.gone, pairs...)
		} else {
			r.made = append(r.made, pairs...) // sur le disque de D3X seulement
		}
		return nil
	})
	_, err := r.e.AddLinked(context.Background(), r.tg, r.cfg, "pub:1", "fiche-1", r.raw, "/data/films")
	var le *LinkError
	if !errors.As(err, &le) || !strings.Contains(le.Reason, "ne trouve pas") || !strings.Contains(le.Reason, "/data/liens/Film.2024/") {
		t.Fatalf("erreur : %v", err)
	}
	r.fq.mu.Lock()
	defer r.fq.mu.Unlock()
	if len(r.made) != 2 || len(r.gone) != 2 || len(r.fq.list) != 0 {
		t.Fatalf("liens %d posés, %d retirés, %d dans le client", len(r.made), len(r.gone), len(r.fq.list))
	}
}
