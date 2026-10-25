package crossseed

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/V3x0rV3x/D3X/internal/bencode"
	"github.com/V3x0rV3x/D3X/internal/client"
	"github.com/V3x0rV3x/D3X/internal/config"
	"github.com/V3x0rV3x/D3X/internal/qbit"
	"github.com/V3x0rV3x/D3X/internal/torrent"
	"github.com/V3x0rV3x/D3X/internal/v3x"
)

func TestSaveDir(t *testing.T) {
	cases := []struct {
		content, name, want string
		ok                  bool
	}{
		{"/data/films/Film.2020", "Film.2020", "/data/films", true},
		{`D:\Torrents\Film.2020\`, "Film.2020", `D:\Torrents`, true},
		{`D:\Film.mkv`, "Film.mkv", `D:\`, true},
		{"/Film.mkv", "Film.mkv", "/", true},
		{"/data/films/Renomme", "Film.2020", "", false},
		{"Film.2020", "Film.2020", "", false},
		// Un seul fichier dans un dossier : qBittorrent donne le chemin du fichier.
		{"/downloads/x/Film.2020/Film.2020.mp4", "Film.2020/Film.2020.mp4", "/downloads/x", true},
		{`D:\T\Film\Film.mkv`, "Film/Film.mkv", `D:\T`, true},
		{"/downloads/x/Autre/Film.2020.mp4", "Film.2020/Film.2020.mp4", "", false},
	}
	for _, c := range cases {
		got, ok := saveDir(c.content, c.name)
		if got != c.want || ok != c.ok {
			t.Errorf("saveDir(%q, %q) = %q, %v ; attendu %q, %v", c.content, c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestNormPath(t *testing.T) {
	if normPath(`D:\Films\X\`) != normPath("d:/films/x") {
		t.Error("un chemin Windows doit se comparer sans la casse ni le séparateur")
	}
	if normPath("/data/X") == normPath("/data/x") {
		t.Error("un chemin Unix garde la casse")
	}
}

// ---------- faux qBittorrent et faux V3X ----------

// mkTorrent : src non vide = torrent privé marqué de cette source (un tracker).
func mkTorrent(name string, files map[string]int64, tracker string, src string) []byte {
	const pl = int64(1 << 20)
	var list []bencode.Value
	var total int64
	for p, n := range files {
		parts := []bencode.Value{}
		for _, s := range strings.Split(p, "/") {
			parts = append(parts, []byte(s))
		}
		list = append(list, bencode.Dict{"length": n, "path": parts})
		total += n
	}
	info := bencode.Dict{"name": []byte(name), "piece length": pl, "files": list, "pieces": make([]byte, (total+pl-1)/pl*20)}
	if src != "" {
		info["private"] = int64(1)
		info["source"] = []byte(src)
	}
	b, _ := bencode.Encode(bencode.Dict{"announce": []byte(tracker), "info": info})
	return b
}

type fakeTorrent struct {
	qbit.Torrent
	raw       []byte
	checkLeft int     // tours de « checking » restants après un recheck
	after     float64 // progression une fois vérifié
}

type fakeQbit struct {
	mu       sync.Mutex
	list     map[string]*fakeTorrent
	adds     []map[string]string
	started  []string
	deleted  []string
	complete map[string]float64 // info-hash V3X → progression après vérification
	// Noms des fichiers dans le client (rang → chemin), renommages faits, et
	// le disque simulé (chemin complet → taille) : une vérification n'est
	// complète que si chaque fichier y est, sous son nom actuel.
	names   map[string]map[int]string
	renames []string
	disk    map[string]int64
	// Demandes de vérification ignorées (client pas encore prêt), par
	// info-hash : la vérification ne part qu'à la relance.
	dropRecheck map[string]int
	// blank : le client redémarre et répond sans ses torrents (exodius19).
	blank bool
}

func (f *fakeQbit) namesOf(h string, raw []byte) map[int]string {
	if f.names == nil {
		f.names = map[string]map[int]string{}
	}
	if n := f.names[h]; n != nil {
		return n
	}
	p, _ := torrent.Parse(raw)
	n := map[int]string{}
	for _, x := range p.Files {
		n[x.Index] = x.Path
	}
	f.names[h] = n
	return n
}

func (f *fakeQbit) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("username") != "admin" || r.Form.Get("password") != "secret" {
			io.WriteString(w, "Fails.")
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "x", Path: "/"})
		io.WriteString(w, "Ok.")
	})
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if c, err := r.Cookie("SID"); err != nil || c.Value != "x" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("/api/v2/app/version", auth(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "v5.0.2") }))
	mux.HandleFunc("/api/v2/app/webapiVersion", auth(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "2.11.2") }))
	mux.HandleFunc("/api/v2/torrents/info", auth(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		want := map[string]bool{}
		if h := r.URL.Query().Get("hashes"); h != "" {
			for _, x := range strings.Split(h, "|") {
				want[strings.ToLower(x)] = true
			}
		}
		out := []qbit.Torrent{}
		for h, ft := range f.list {
			if f.blank || len(want) > 0 && !want[h] {
				continue
			}
			if ft.checkLeft > 0 {
				ft.State = "checkingUP"
				ft.checkLeft--
				if ft.checkLeft == 0 {
					ft.Progress = ft.after
					ft.State = "stoppedDL"
					if ft.after >= 1 {
						ft.State = "stoppedUP"
					}
				}
			}
			out = append(out, ft.Torrent)
		}
		json.NewEncoder(w).Encode(out)
	}))
	mux.HandleFunc("/api/v2/torrents/export", auth(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		ft := f.list[strings.ToLower(r.URL.Query().Get("hash"))]
		if ft == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write(ft.raw)
	}))
	mux.HandleFunc("/api/v2/torrents/add", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("add : %v", err)
			return
		}
		fields := map[string]string{}
		for k, v := range r.MultipartForm.Value {
			fields[k] = v[0]
		}
		fh := r.MultipartForm.File["torrents"][0]
		fd, _ := fh.Open()
		raw, _ := io.ReadAll(fd)
		p, err := torrent.Parse(raw)
		if err != nil {
			t.Errorf("add : .torrent illisible : %v", err)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.adds = append(f.adds, fields)
		h := strings.ToLower(p.InfoHash)
		if f.list[h] != nil {
			io.WriteString(w, "Fails.")
			return
		}
		f.list[h] = &fakeTorrent{Torrent: qbit.Torrent{Hash: h, Name: p.Name, SavePath: fields["savepath"], ContentPath: fields["savepath"] + "/" + p.Name, State: "stoppedDL", TotalSize: p.Total, Size: p.Total}, raw: raw, after: f.complete[h]}
		io.WriteString(w, "Ok.")
	}))
	mux.HandleFunc("/api/v2/torrents/recheck", auth(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		h := strings.ToLower(r.Form.Get("hashes"))
		if f.dropRecheck[h] > 0 {
			f.dropRecheck[h]--
			return
		}
		if ft := f.list[h]; ft != nil {
			ft.checkLeft = 2
			if f.disk != nil {
				// Comme un vrai client : chaque morceau qui touche un fichier
				// absent du disque échoue, le reste est vérifié.
				p, _ := torrent.Parse(ft.raw)
				bad := map[int64]bool{}
				for _, x := range p.Files {
					if f.disk[ft.SavePath+"/"+f.namesOf(h, ft.raw)[x.Index]] != x.Size && x.Size > 0 {
						for i := x.Offset / p.PieceLength; i <= (x.Offset+x.Size-1)/p.PieceLength; i++ {
							bad[i] = true
						}
					}
				}
				var missing int64
				for i := range bad {
					missing += min(p.PieceLength, p.Total-i*p.PieceLength)
				}
				ft.after = 1 - float64(missing)/float64(p.Total)
			}
		}
	}))
	mux.HandleFunc("/api/v2/torrents/files", auth(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		h := strings.ToLower(r.URL.Query().Get("hash"))
		ft := f.list[h]
		if ft == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		p, _ := torrent.Parse(ft.raw)
		names := f.namesOf(h, ft.raw)
		type file struct {
			Index int    `json:"index"`
			Name  string `json:"name"`
			Size  int64  `json:"size"`
		}
		out := []file{}
		for _, x := range p.Files {
			out = append(out, file{x.Index, names[x.Index], x.Size})
		}
		json.NewEncoder(w).Encode(out)
	}))
	// Complétion des chemins de qBittorrent 5 : le contenu d'un dossier du
	// disque simulé.
	mux.HandleFunc("/api/v2/app/getDirectoryContent", auth(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		dir := strings.TrimRight(r.URL.Query().Get("dirPath"), "/") + "/"
		seen := map[string]bool{}
		out := []string{}
		for p := range f.disk {
			if !strings.HasPrefix(p, dir) {
				continue
			}
			name := strings.SplitN(p[len(dir):], "/", 2)[0]
			if !seen[name] {
				seen[name] = true
				out = append(out, dir+name)
			}
		}
		if len(out) == 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(out)
	}))
	mux.HandleFunc("/api/v2/torrents/renameFile", auth(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		h := strings.ToLower(r.Form.Get("hash"))
		ft := f.list[h]
		if ft == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if ft.State != "stoppedDL" || ft.checkLeft > 0 || ft.Progress > 0 {
			t.Errorf("renommage sur un torrent démarré ou déjà vérifié (%s)", ft.State)
		}
		// Le vrai client déplacerait un fichier existant : le test l'interdit.
		if _, exists := f.disk[ft.SavePath+"/"+r.Form.Get("oldPath")]; exists {
			t.Errorf("renommage de %q, qui existe sur le disque", r.Form.Get("oldPath"))
		}
		names := f.namesOf(h, ft.raw)
		for i, n := range names {
			if n == r.Form.Get("oldPath") {
				names[i] = r.Form.Get("newPath")
				f.renames = append(f.renames, r.Form.Get("oldPath")+" -> "+r.Form.Get("newPath"))
				return
			}
		}
		w.WriteHeader(http.StatusConflict)
	}))
	mux.HandleFunc("/api/v2/torrents/delete", auth(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("deleteFiles") != "false" {
			t.Errorf("suppression demandée avec deleteFiles=%q : les fichiers doivent TOUJOURS rester", r.Form.Get("deleteFiles"))
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		h := strings.ToLower(r.Form.Get("hashes"))
		f.deleted = append(f.deleted, h)
		delete(f.list, h)
	}))
	mux.HandleFunc("/api/v2/torrents/start", auth(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		h := strings.ToLower(r.Form.Get("hashes"))
		f.started = append(f.started, h)
		if ft := f.list[h]; ft != nil {
			ft.State = "stalledUP"
		}
	}))
	return mux
}

func TestScanAndApply(t *testing.T) {
	isolatedConfig(t)

	// Le client seede trois releases pour un autre tracker et une pour V3X.
	filesA := map[string]int64{"A.mkv": 3_000_000_000, "A.nfo": 4_000}
	filesB := map[string]int64{"B.mkv": 2_000_000_000}
	filesC := map[string]int64{"C.mkv": 1_000_000_000}
	otherA := mkTorrent("Release.A", filesA, "https://autre.invalid/announce", "AUTRE")
	otherB := mkTorrent("Release.B", filesB, "https://autre.invalid/announce", "AUTRE")
	otherC := mkTorrent("Release.C", filesC, "https://autre.invalid/announce", "AUTRE")
	mine := mkTorrent("Release.D", map[string]int64{"D.mkv": 5_000_000}, "https://announce.v3x.club/abc/announce", "V3X")

	// Côté V3X : A et B existent à l'identique (autre info-hash : source V3X).
	v3xA := mkTorrent("Release.A", filesA, "https://announce.v3x.club/pk/announce", "")
	v3xB := mkTorrent("Release.B", filesB, "https://announce.v3x.club/pk/announce", "")
	pa, _ := torrent.Parse(v3xA)
	pb, _ := torrent.Parse(v3xB)

	fq := &fakeQbit{list: map[string]*fakeTorrent{}, complete: map[string]float64{
		strings.ToLower(pa.InfoHash): 1,   // A : fichiers identiques, 100 %
		strings.ToLower(pb.InfoHash): 0.4, // B : fichiers abîmés, 40 %
	},
		// A : la première demande de vérification se perd (V3x0r, 21/10/26) ;
		// la relance doit quand même le mettre en seed, pas « vérifié à 0 % ».
		dropRecheck: map[string]int{strings.ToLower(pa.InfoHash): 1},
	}
	for _, x := range []struct {
		raw  []byte
		path string
		tr   string
	}{{otherA, "/data/autre", "autre.invalid"}, {otherB, "/data/autre", "autre.invalid"}, {otherC, "/data/autre", "autre.invalid"}, {mine, "/data/v3x", "announce.v3x.club"},
		// Deuxième copie de A, d'un troisième tracker, rangée ailleurs.
		{mkTorrent("Release.A", filesA, "https://troisieme.invalid/announce", "TROIS"), "/data/troisieme", "troisieme.invalid"}} {
		p, _ := torrent.Parse(x.raw)
		h := strings.ToLower(p.InfoHash)
		fq.list[h] = &fakeTorrent{Torrent: qbit.Torrent{
			Hash: h, Name: p.Name, SavePath: x.path, ContentPath: x.path + "/" + p.Name, Progress: 1, State: "stalledUP",
			Size: p.Total, TotalSize: p.Total, MagnetURI: "magnet:?xt=urn:btih:" + h + "&tr=https%3A%2F%2F" + x.tr + "%2Fannounce",
		}, raw: x.raw}
	}
	qs := httptest.NewServer(fq.handler(t))
	defer qs.Close()

	var sent []v3x.MatchItem
	vs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/me":
			io.WriteString(w, `{"username":"test","ratio":2.5,"hitAndRun":0}`)
		case "/api/d3x/match":
			var in struct{ Items []v3x.MatchItem }
			json.NewDecoder(r.Body).Decode(&in)
			sent = append(sent, in.Items...)
			type res struct {
				Key   string     `json:"key"`
				Match *v3x.Match `json:"match"`
			}
			var out []res
			for _, it := range in.Items {
				var m *v3x.Match
				switch it.ContentHash {
				case pa.ContentHash:
					m = &v3x.Match{Kind: "exact", ID: "11111111-1111-1111-1111-111111111111", Name: "Release.A", InfoHash: pa.InfoHash, Seeders: 1, Size: pa.Total}
				case pb.ContentHash:
					m = &v3x.Match{Kind: "exact", ID: "22222222-2222-2222-2222-222222222222", Name: "Release.B", InfoHash: pb.InfoHash, Seeders: 4, Size: pb.Total}
				}
				out = append(out, res{Key: it.Key, Match: m})
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"results": out})
		case "/torznab/download":
			switch r.URL.Query().Get("id") {
			case "11111111-1111-1111-1111-111111111111":
				w.Write(v3xA)
			case "22222222-2222-2222-2222-222222222222":
				w.Write(v3xB)
			default:
				w.WriteHeader(http.StatusNotFound)
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
	if st := e.State(); st.Scan.Phase != "done" {
		t.Fatalf("analyse : %+v", st.Scan)
	}
	if len(sent) != 4 {
		t.Fatalf("%d empreintes envoyées, 4 attendues (le torrent déjà sur V3X n'est pas envoyé)", len(sent))
	}
	nA := 0
	for _, r := range e.Rows() {
		if r.Name == "Release.A" {
			nA++
			if r.Copies != 2 {
				t.Fatalf("Release.A : %d copies comptées, 2 attendues", r.Copies)
			}
		}
	}
	if nA != 1 {
		t.Fatalf("Release.A sur %d lignes : une seule attendue pour deux copies identiques", nA)
	}
	for _, it := range sent {
		if len(it.Key) > 6 || it.ContentHash == "" || it.InfoHash != "" {
			t.Fatalf("empreinte inattendue : %+v", it)
		}
	}
	got := map[string]string{}
	for _, r := range e.Rows() {
		got[r.Name] = r.Status
	}
	want := map[string]string{"Release.A": StAdd, "Release.B": StAdd, "Release.C": StNone, "Release.D": StOnV3X}
	for k, s := range want {
		if got[k] != s {
			t.Fatalf("%s : %s, attendu %s (lignes : %v)", k, got[k], s, got)
		}
	}
	// Les moins seedées d'abord.
	if rows := e.Rows(); rows[0].Name != "Release.A" {
		t.Fatalf("première ligne %s, attendu Release.A (1 seeder)", rows[0].Name)
	}

	var keys []string
	for _, r := range e.Rows() {
		if r.Status == StAdd {
			keys = append(keys, r.Key)
		}
	}
	if n, err := e.Apply(keys); err != nil || n != 2 {
		t.Fatalf("Apply : %d, %v", n, err)
	}
	waitFor(t, func() bool {
		st := e.State()
		return !st.Applying && st.Counts[StChecking] == 0 && st.Counts[StQueued] == 0
	})
	got = map[string]string{}
	for _, r := range e.Rows() {
		got[r.Name] = r.Status
	}
	if got["Release.A"] != StSeeding || got["Release.B"] != StFailed {
		t.Fatalf("après ajout : %v", got)
	}

	// Retrait de l'ajout raté (B) : fichiers intacts.
	var keyB string
	for _, r := range e.Rows() {
		if r.Name == "Release.B" {
			keyB = r.Key
		}
	}
	if err := e.Remove(keyB); err != nil {
		t.Fatalf("Remove : %v", err)
	}
	for _, r := range e.Rows() {
		if r.Name == "Release.B" && r.Status != StRemoved {
			t.Fatalf("B après retrait : %s", r.Status)
		}
	}

	fq.mu.Lock()
	defer fq.mu.Unlock()
	if len(fq.deleted) != 1 || fq.deleted[0] != strings.ToLower(pb.InfoHash) {
		t.Fatalf("retirés : %v", fq.deleted)
	}
	if len(fq.adds) != 2 {
		t.Fatalf("%d ajouts, 2 attendus", len(fq.adds))
	}
	for _, f := range fq.adds {
		for k, v := range map[string]string{"savepath": "/data/autre", "autoTMM": "false", "contentLayout": "Original", "stopCondition": "None", "stopped": "true", "paused": "true", "skip_checking": "false", "tags": "D3X"} {
			if f[k] != v {
				t.Errorf("ajout : %s = %q, attendu %q", k, f[k], v)
			}
		}
	}
	if len(fq.started) != 1 || fq.started[0] != strings.ToLower(pa.InfoHash) {
		t.Fatalf("démarrés : %v ; seul A (vérifié à 100 %%) devait l'être", fq.started)
	}
}

// oneClient : un seul client, comme avant le multi-clients (sa correspondance
// de dossiers et son dossier de liens sont ceux des anciens réglages).
func oneClient(q client.Client, v *v3x.Client, cfg *config.Config) Clients {
	return func() (*Env, error) {
		c := *cfg
		if len(c.Clients) == 0 {
			c.Clients = []config.ClientConfig{{ID: "c1", URL: "test", PathMap: cfg.PathMap, LinksDir: cfg.Links.Dir}}
		}
		return &Env{V3X: v, Cfg: &c, Targets: []Target{{ID: "c1", Name: q.Kind(), Client: q, PathMap: cfg.PathMap, LinksDir: cfg.Links.Dir}}}, nil
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("délai dépassé")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Rythme rapide pour tous les tests, réglé une fois : les moteurs des tests
// précédents tournent encore et lisent ces valeurs.
func TestMain(m *testing.M) {
	watchEvery, downloadGap = 20*time.Millisecond, 0
	recheckGap, endStable, missGrace = 100*time.Millisecond, 60*time.Millisecond, 150*time.Millisecond
	failedEvery = 30 * time.Millisecond
	os.Exit(m.Run())
}

// Ajout laissé à l'arrêt puis retiré du client à la main : la ligne passe
// d'elle-même en « Retiré » au lieu de rester en erreur (Bobobobobo123,
// 25/10/26).
func TestFailedRemovedByHand(t *testing.T) {
	isolatedConfig(t)
	fq := &fakeQbit{list: map[string]*fakeTorrent{}, complete: map[string]float64{}}
	qs := httptest.NewServer(fq.handler(t))
	defer qs.Close()
	q, _ := client.New(client.Options{URL: qs.URL, User: "admin", Pass: "secret"})
	v := v3x.New(qs.URL, "cle", "D3X/test")
	e := New(oneClient(q, v, config.Defaults()))

	const h = "0123456789abcdef0123456789abcdef01234567"
	e.mu.Lock()
	e.rows["k1"] = &Row{Key: "k1", Name: "Release.X", Status: StFailed}
	e.watches[h] = &watch{Key: "k1", Name: "Release.X", Client: "c1", Status: StFailed, Detail: "vérifié à 40 %"}
	e.mu.Unlock()

	waitFor(t, func() bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.rows["k1"].Status == StRemoved
	})
	e.mu.Lock()
	_, still := e.watches[h]
	e.mu.Unlock()
	if still {
		t.Fatal("l'ajout retiré reste surveillé")
	}
}

// isolatedConfig : dossier de réglages propre au test. Le moteur continue
// d'écrire après la fin du test : nettoyage au mieux, sans faire échouer.
func isolatedConfig(t *testing.T) {
	tmp, err := os.MkdirTemp("", "d3x-crossseed-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	t.Setenv("AppData", tmp)
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)
}
