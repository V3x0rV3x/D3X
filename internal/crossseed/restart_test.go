package crossseed

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/V3x0rV3x/D3X/internal/client"
	"github.com/V3x0rV3x/D3X/internal/config"
	"github.com/V3x0rV3x/D3X/internal/qbit"
	"github.com/V3x0rV3x/D3X/internal/torrent"
	"github.com/V3x0rV3x/D3X/internal/v3x"
)

// Un client qui redémarre pendant la vérification répond un moment sans ses
// torrents (exodius19, 21/10/26 : releases publiées restées « Terminé » dans
// Transmission). L'ajout n'est dit « retiré » qu'après une absence durable ;
// sinon il est démarré à 100 % comme d'habitude.
func TestClientRestartDuringCheck(t *testing.T) {
	for _, tc := range []struct {
		name  string
		blank time.Duration
		want  string
	}{{"redémarrage court", 60 * time.Millisecond, StSeeding}, {"vraiment retiré", 600 * time.Millisecond, StRemoved}} {
		t.Run(tc.name, func(t *testing.T) {
			isolatedConfig(t)
			files := map[string]int64{"R.mkv": 1_000_000_000}
			other := mkTorrent("Release.R", files, "https://autre.invalid/announce", "AUTRE")
			onV3X := mkTorrent("Release.R", files, "https://announce.v3x.club/pk/announce", "")
			po, _ := torrent.Parse(other)
			pv, _ := torrent.Parse(onV3X)
			h := strings.ToLower(po.InfoHash)
			fq := &fakeQbit{list: map[string]*fakeTorrent{}, complete: map[string]float64{strings.ToLower(pv.InfoHash): 1}}
			fq.list[h] = &fakeTorrent{Torrent: qbit.Torrent{
				Hash: h, Name: po.Name, SavePath: "/data", ContentPath: "/data/" + po.Name, Progress: 1, State: "stalledUP",
				Size: po.Total, TotalSize: po.Total, MagnetURI: "magnet:?xt=urn:btih:" + h + "&tr=https%3A%2F%2Fautre.invalid%2Fannounce",
			}, raw: other}
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
						out = append(out, res{Key: it.Key, Match: &v3x.Match{Kind: "exact", ID: "55555555-5555-5555-5555-555555555555", Name: "Release.R", InfoHash: pv.InfoHash, Seeders: 1, Size: pv.Total}})
					}
					json.NewEncoder(w).Encode(map[string]interface{}{"results": out})
				case "/torznab/download":
					w.Write(onV3X)
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
			var key string
			for _, r := range e.Rows() {
				if r.Status == StAdd {
					key = r.Key
				}
			}
			if _, err := e.Apply([]string{key}); err != nil {
				t.Fatal(err)
			}
			waitFor(t, func() bool {
				for _, r := range e.Rows() {
					if r.Key == key {
						return r.Status == StChecking
					}
				}
				return false
			})
			fq.mu.Lock()
			fq.blank = true
			fq.mu.Unlock()
			time.Sleep(tc.blank)
			fq.mu.Lock()
			fq.blank = false
			if tc.want == StRemoved {
				delete(fq.list, strings.ToLower(pv.InfoHash))
			}
			fq.mu.Unlock()
			waitFor(t, func() bool {
				for _, r := range e.Rows() {
					if r.Key == key {
						return r.Status == tc.want
					}
				}
				return false
			})
		})
	}
}

// Vu en vérification avant un arrêt (sawChecking gardé dans pending.json), puis
// arrêté au redémarrage sans avoir été vérifié : la file d'attente de
// vérification s'est vidée (client arrêté), les fichiers ne sont pas en cause.
// La vérification est relancée une fois avant de conclure « tes fichiers
// diffèrent » (bertho-zero, ticket n°3, 27/10/26 : 53 ajouts de la Médiathèque
// marqués « vérifié à 0,0 % », données bonnes).
func TestLostCheckAfterRestart(t *testing.T) {
	for _, tc := range []struct {
		name           string
		progress, from float64 // à l'arrêt, et au premier passage en vérification
		moved          bool    // la progression a bougé pendant la vérification
		after          float64 // une fois vraiment vérifié
		want           string
		rechecks       int
	}{
		{"sorti de la file à 0 %", 0, 0, false, 1, StSeeding, 1},
		{"arrêté sans avoir bougé", 0.4, 0.4, false, 1, StSeeding, 1},
		{"vraiment différents", 0, 0, false, 0, StFailed, 1},
		{"vérifié jusqu'au bout", 0.4, 0, true, 1, StFailed, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolatedConfig(t)
			raw := mkTorrent("Release.L", map[string]int64{"L.mkv": 1_000_000_000}, "https://announce.v3x.club/pk/announce", "")
			p, _ := torrent.Parse(raw)
			h := strings.ToLower(p.InfoHash)
			fq := &fakeQbit{list: map[string]*fakeTorrent{h: {Torrent: qbit.Torrent{
				Hash: h, Name: p.Name, SavePath: "/liens", ContentPath: "/liens/" + p.Name, State: "stoppedDL",
				Progress: tc.progress, Size: p.Total, TotalSize: p.Total,
			}, raw: raw, after: tc.after}}}
			qs := httptest.NewServer(fq.handler(t))
			defer qs.Close()
			// Fin de vérification vue un quart d'heure avant l'arrêt de D3X.
			before := time.Now().Add(-15 * time.Minute)
			pending, _ := json.Marshal([]*watch{{
				Client: "c1", Key: "lib:L", Name: p.Name, ID: "66666666-6666-6666-6666-666666666666", InfoHash: h,
				Since: before.Add(-5 * time.Minute), SawChecking: true, CheckFrom: tc.from, CheckMoved: tc.moved,
				EndAt: before, EndProgress: tc.progress, Status: StChecking, Progress: tc.progress,
			}})
			if err := config.WriteFileAtomic("pending.json", pending); err != nil {
				t.Fatal(err)
			}
			q, _ := client.New(client.Options{URL: qs.URL, User: "admin", Pass: "secret"})
			e := New(oneClient(q, v3x.New(qs.URL, "cle", "D3X/test"), config.Defaults()))
			waitFor(t, func() bool {
				st, ok := e.WatchOf(h)
				return ok && st.Status == tc.want
			})
			fq.mu.Lock()
			n := fq.rechecks[h]
			fq.mu.Unlock()
			if n != tc.rechecks {
				t.Fatalf("%d vérifications demandées, %d attendues", n, tc.rechecks)
			}
			if st, _ := e.WatchOf(h); tc.want == StFailed && !strings.Contains(st.Detail, "tes fichiers diffèrent") {
				t.Fatalf("échec : %q", st.Detail)
			}
		})
	}
}
