package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeHydranos imite l'API native d'Hydranos : deux moteurs (race, hoard), un
// torrent multi-fichiers, et les deux routes de la 4.6 selon la version.
type fakeHydranos struct {
	mu      sync.Mutex
	version string
	fsMode  string // « » : liste ; « 403 » : hors des racines
	hash    string
	held    map[string]bool // moteurs qui tiennent le torrent
	calls   []string
	queries []string
	// ce que le client a envoyé
	upload   map[string]string
	renames  []map[string]string
	paused   map[string]bool
	removed  []string
	statusN  int
	noHoard  bool   // aucun moteur n'a le rôle hoard
	legacy   bool   // /files d'avant la 4.6 : pas de champ folder, chemins sans le dossier
	mono     bool   // torrent d'un seul fichier
	content  string // content_path des lignes, s'il est donné
	tracker  string
	torrent  []byte
	pausedBy map[string]bool
	// content_folder des lignes : nil → null, comme un torrent que le magasin
	// d'Hydranos ne connaît pas encore.
	contentFolder *bool
	// Torrent retenu pour preuve (Hydranos après 4.6) : la ligne dit
	// « checking_files », un POST verify répond succès sans rien changer.
	hold    bool
	verifyN int // POST verify reçus
}

func (f *fakeHydranos) row(engine string) map[string]interface{} {
	paused := f.paused[engine]
	state := "queued"
	if !paused {
		state = "seeding"
	}
	if f.hold {
		state, paused = "checking_files", false
	}
	row := map[string]interface{}{
		"info_hash": f.hash, "name": "Release.Test", "state": state, "progress": 1.0,
		"total_size": 1000, "save_path": "/categorie", "engine_save_path": "/data", "tracker_host": f.tracker,
		"user_paused": paused, "multi_file": !f.mono, "agent": "local-" + engine,
		"content_folder": f.contentFolder,
	}
	if f.hold {
		row["progress"] = 0.0
	}
	if f.content != "" {
		row["content_path"] = f.content
	}
	return row
}

func (f *fakeHydranos) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		f.queries = append(f.queries, r.URL.RawQuery)
		if r.URL.Path != "/health" && r.Header.Get("X-Api-Key") != "cle-secrete" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"Invalid or missing API key"}`)
			return
		}
		reply := func(code int, v interface{}) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			json.NewEncoder(w).Encode(v)
		}
		engine := r.URL.Query().Get("engine")
		p := r.URL.Path
		switch {
		case p == "/api/status":
			reply(200, map[string]interface{}{"version": f.version})
		case p == "/api/engines":
			reply(200, []map[string]interface{}{{"id": "race", "role": "race"}, {"id": "hoard", "role": "hoard"}})
		case p == "/api/torrents/upload" && r.Method == http.MethodPost:
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				reply(400, map[string]string{"error": "multipart"})
				return
			}
			f.upload = map[string]string{}
			for k, v := range r.MultipartForm.Value {
				f.upload[k] = v[0]
			}
			file, _, err := r.FormFile("torrents")
			if err != nil {
				reply(400, map[string]string{"error": "no torrent file in request"})
				return
			}
			f.torrent, _ = io.ReadAll(file)
			if f.held["hoard"] {
				reply(409, map[string]string{"error": "already added"})
				return
			}
			if f.noHoard && r.MultipartForm.Value["mode"] != nil {
				reply(400, map[string]string{"error": "no engine has the hoard role"})
				return
			}
			f.held["hoard"] = true
			reply(200, map[string]string{"info_hash": f.hash, "name": "Release.Test", "engine": "hoard", "mode": "hoard"})
		case strings.HasPrefix(p, "/api/hoard/torrents/"+f.hash) || strings.HasPrefix(p, "/api/race/torrents/"+f.hash):
			role := strings.Split(p, "/")[2]
			if !f.held[engine] || engine != role {
				reply(404, map[string]string{"error": "torrent not in " + role})
				return
			}
			if strings.HasSuffix(p, "/verify") && r.Method == http.MethodPost {
				f.verifyN++
				if f.hold {
					reply(200, map[string]interface{}{"status": "ok"}) // succès sans effet
					return
				}
				reply(200, map[string]interface{}{"status": "ok", "checking": true, "paused": true})
				return
			}
			reply(200, f.row(engine))
		case p == "/api/torrents/"+f.hash+"/torrent":
			w.Header().Set("Content-Type", "application/x-bittorrent")
			w.Write(f.torrent)
		case p == "/api/torrents/"+f.hash+"/files":
			files := []map[string]interface{}{
				{"index": 0, "folder": "Release.Test", "path": "a.mkv", "size": 600},
				{"index": 1, "folder": "Release.Test", "path": "sub/b.mkv", "size": 400}}
			if f.mono {
				files = []map[string]interface{}{{"index": 0, "folder": nil, "path": "Release.Test.mkv", "size": 1000}}
			}
			if f.legacy { // avant la 4.6 : ni folder, ni dossier racine dans path
				for _, e := range files {
					delete(e, "folder")
				}
			}
			reply(200, map[string]interface{}{"files": files})
		case p == "/api/torrents/"+f.hash+"/files/rename":
			if f.version < "4.6" {
				w.WriteHeader(404) // route inconnue : axum répond 404 sans corps
				return
			}
			var b map[string]string
			json.NewDecoder(r.Body).Decode(&b)
			if _, has := b["folder"]; has && f.mono {
				reply(400, map[string]string{"error": "folder is for multi-file torrents"})
				return
			}
			if _, has := b["folder"]; !has && !f.mono {
				reply(400, map[string]string{"error": "folder missing"})
				return
			}
			if b["to"] == "occupe.mkv" {
				reply(409, map[string]string{"error": "file already exists on disk"})
				return
			}
			f.renames = append(f.renames, b)
			reply(200, map[string]interface{}{"ok": true, "files": []string{}})
		case p == "/api/engines/hoard/pause" || p == "/api/engines/race/pause":
			var b struct {
				Hashes []string `json:"hashes"`
				Paused bool     `json:"paused"`
			}
			json.NewDecoder(r.Body).Decode(&b)
			e := strings.Split(p, "/")[3]
			if len(b.Hashes) != 1 || b.Hashes[0] != f.hash || !f.held[e] {
				reply(200, map[string]interface{}{"status": "ok", "applied": 0})
				return
			}
			f.paused[e] = b.Paused
			reply(200, map[string]interface{}{"status": "ok", "applied": 1, "paused": b.Paused})
		case p == "/api/torrents/"+f.hash && r.Method == http.MethodDelete:
			f.removed = append(f.removed, engine+"|"+r.URL.RawQuery)
			delete(f.held, engine)
			reply(200, map[string]string{"status": "ok"})
		case p == "/api/fs/list":
			if f.version < "4.6" {
				w.WriteHeader(404)
				return
			}
			if f.fsMode == "403" {
				reply(403, map[string]string{"error": "outside the download folders"})
				return
			}
			switch r.URL.Query().Get("path") {
			case "/data":
				reply(200, map[string]interface{}{"path": "/data", "entries": []map[string]interface{}{{"name": "Release.Test", "dir": true}, {"name": "f.mkv", "dir": false}}})
			case "/gros":
				reply(200, map[string]interface{}{"path": "/gros", "truncated": true, "entries": []map[string]interface{}{{"name": "present.mkv", "dir": false}}})
			default:
				reply(404, map[string]string{"error": "no such directory"})
			}
		default:
			w.WriteHeader(404)
		}
	})
}

func newFake(t *testing.T, version string) (*fakeHydranos, Client, string) {
	t.Helper()
	_, hash := sampleTorrent(t)
	f := &fakeHydranos{version: version, hash: hash, held: map[string]bool{}, paused: map[string]bool{}, tracker: "announce.v3x.club"}
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	c, err := New(Options{Kind: "hydranos", URL: srv.URL, Pass: "cle-secrete"})
	if err != nil {
		t.Fatal(err)
	}
	return f, c, hash
}

func TestHydranos(t *testing.T) {
	raw, _ := sampleTorrent(t)
	f, c, hash := newFake(t, "4.8.1")
	ctx := context.Background()

	if c.Kind() != "Hydranos" || KindName("hydranos") != "Hydranos" {
		t.Fatal("nom du client")
	}
	if v, err := c.Version(ctx); err != nil || v != "4.8.1" {
		t.Fatalf("version : %q %v", v, err)
	}

	// Ajout : à l'arrêt, sans sauter la vérification, fichier seul sans dossier en plus.
	if err := c.Add(ctx, "x.torrent", raw, AddOptions{SavePath: "/data", Category: "films", Tag: "D3X"}); err != nil {
		t.Fatal(err)
	}
	u := f.upload
	if u["mode"] != "hoard" {
		t.Fatalf("un cross-seed est un seed long : mode=hoard attendu, reçu %q", u["mode"])
	}
	if u["paused"] != "true" || u["savepath"] != "/data" || u["category"] != "films" || u["tags"] != "D3X" || u["create_subfolder"] != "false" {
		t.Fatalf("ajout : %v", u)
	}
	for _, k := range []string{"skip_checking", "seed_mode", "skip_recheck"} {
		if _, ok := u[k]; ok {
			t.Fatalf("l'ajout ne doit jamais sauter la vérification (%s)", k)
		}
	}
	if string(f.torrent) != string(raw) {
		t.Fatal("le .torrent envoyé n'est pas le bon")
	}
	if err := c.Add(ctx, "x.torrent", raw, AddOptions{SavePath: "/data"}); err != ErrAlreadyThere {
		t.Fatalf("doublon : %v", err)
	}

	// Liste ciblée : une seule requête par moteur, jamais la liste entière.
	f.calls = nil
	list, err := c.List(ctx, []string{strings.ToUpper(hash)})
	if err != nil || len(list) != 1 {
		t.Fatalf("liste : %v %v", list, err)
	}
	tor := list[0]
	if tor.Hash != hash || tor.ContentPath != "/data/Release.Test" || tor.Size != 1000 || tor.Progress != 1 || tor.Trackers[0] != "announce.v3x.club" || tor.Stopped || tor.Checking {
		t.Fatalf("torrent : %+v", tor)
	}
	for _, call := range f.calls {
		if strings.Contains(call, "/page") || strings.HasSuffix(call, "/torrents") {
			t.Fatalf("une liste entière pour un info-hash : %v", f.calls)
		}
	}
	if none, err := c.List(ctx, []string{"0000000000000000000000000000000000000000"}); err != nil || len(none) != 0 {
		t.Fatalf("torrent absent : %v %v", none, err)
	}

	// Export et fichiers (le dossier racine est ajouté aux chemins du .torrent).
	if b, err := c.Export(ctx, hash); err != nil || string(b) != string(raw) {
		t.Fatalf("export : %v", err)
	}
	files, total, err := c.Files(ctx, hash)
	if err != nil || total != 1000 || len(files) != 2 || files[0].Path != "Release.Test/a.mkv" || files[1].Path != "Release.Test/sub/b.mkv" || files[1].Index != 1 {
		t.Fatalf("fichiers : %v %d %v", files, total, err)
	}
	// Hydranos d'avant la 4.6 : pas de folder, le dossier est ajouté ici.
	f.legacy = true
	if files, _, _ := c.Files(ctx, hash); files[0].Path != "Release.Test/a.mkv" || files[1].Path != "Release.Test/sub/b.mkv" {
		t.Fatalf("fichiers sans folder : %v", files)
	}
	f.legacy = false
	// Un torrent d'un seul fichier : folder est null, le chemin est entier.
	f.mono = true
	if files, total, _ := c.Files(ctx, hash); len(files) != 1 || files[0].Path != "Release.Test.mkv" || total != 1000 {
		t.Fatalf("fichier seul : %v %d", files, total)
	}
	f.mono = false
	// content_path de la ligne, quand Hydranos le donne.
	f.content = "/data/Autre"
	if l, _ := c.List(ctx, []string{hash}); l[0].ContentPath != "/data/Autre" {
		t.Fatalf("content_path : %+v", l)
	}
	f.content = ""

	// Arrêt, reprise, vérification : sur la copie ajoutée par D3X.
	if err := c.Start(ctx, hash); err != nil || f.paused["hoard"] {
		t.Fatalf("reprise : %v", err)
	}
	if err := c.Stop(ctx, hash); err != nil || !f.paused["hoard"] {
		t.Fatalf("arrêt : %v", err)
	}
	if l, _ := c.List(ctx, []string{hash}); !l[0].Stopped {
		t.Fatal("un torrent arrêté doit l'être dans la liste")
	}
	if err := c.Recheck(ctx, hash); err != nil || f.calls[len(f.calls)-1] != "POST /api/hoard/torrents/"+hash+"/verify" || !strings.Contains(f.queries[len(f.queries)-1], "engine=hoard") {
		t.Fatalf("vérification : %v %v", err, f.calls)
	}

	// Retrait : la copie de D3X seulement, jamais les fichiers.
	f.held["race"] = true // le même torrent tenu aussi par race
	if err := c.RemoveKeepFiles(ctx, hash); err != nil {
		t.Fatal(err)
	}
	if len(f.removed) != 1 || f.removed[0] != "hoard|engine=hoard" {
		t.Fatalf("retrait : %v ; une seule copie, fichiers gardés", f.removed)
	}
	for _, q := range f.queries {
		if strings.Contains(q, "delete_files") {
			t.Fatalf("delete_files envoyé : %s", q)
		}
	}
	if !f.held["race"] {
		t.Fatal("l'autre copie ne doit pas être retirée")
	}
	if err := c.RemoveKeepFiles(ctx, "0000000000000000000000000000000000000000"); err != nil {
		t.Fatalf("retirer un torrent absent : %v", err)
	}
}

func TestHydranosListAll(t *testing.T) {
	var offsets []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/engines":
			io.WriteString(w, `[{"id":"race","role":"race"},{"id":"vpn1","role":"race"},{"id":"hoard","role":"hoard"}]`)
		case "/api/engines/race/page", "/api/engines/hoard/page":
			offsets = append(offsets, r.URL.Path+"?"+r.URL.Query().Get("offset")+"&"+r.URL.Query().Get("limit"))
			row := func(h, agent string) string {
				return `{"info_hash":"` + h + `","name":"n","state":"seeding","progress":1,"total_size":5,"engine_save_path":"/d","agent":"` + agent + `"}`
			}
			if r.URL.Path == "/api/engines/hoard/page" {
				io.WriteString(w, `{"filtered":1,"rows":[`+row("bb", "local-hoard")+`]}`)
			} else if r.URL.Query().Get("offset") == "0" {
				io.WriteString(w, `{"filtered":3,"rows":[`+row("aa", "local-race")+`,`+row("cc", "node2-race")+`]}`)
			} else {
				io.WriteString(w, `{"filtered":3,"rows":[`+row("dd", "local-vpn1")+`]}`)
			}
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c, _ := New(Options{Kind: "hydranos", URL: srv.URL + "/", Pass: "k"})
	list, err := c.List(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, x := range list {
		got = append(got, x.Hash)
	}
	// Une page par rôle (le moteur vpn1 est dans celle de race), paginée ; les
	// lignes d'un autre nœud ne sont pas d'ici.
	if strings.Join(got, ",") != "aa,dd,bb" || len(offsets) != 3 || offsets[1] != "/api/engines/race/page?2&5000" {
		t.Fatalf("liste complète : %v %v", got, offsets)
	}
}

func TestHydranosAuth(t *testing.T) {
	f, _, _ := newFake(t, "4.8.1")
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	bad, _ := New(Options{Kind: "hydranos", URL: srv.URL, Pass: "faux"})
	if _, err := bad.Version(context.Background()); err != ErrAuth {
		t.Fatalf("mauvaise clé : %v", err)
	}
	// Adresse saisie sans http:// ni /api : acceptée.
	ok, _ := New(Options{Kind: "hydranos", URL: strings.TrimPrefix(srv.URL, "http://") + "/api/", Pass: "cle-secrete"})
	if v, err := ok.Version(context.Background()); err != nil || v != "4.8.1" {
		t.Fatalf("adresse nue : %q %v", v, err)
	}
}

func TestHydranosRename(t *testing.T) {
	f, c, hash := newFake(t, "4.8.1")
	ctx := context.Background()
	f.held["hoard"] = true
	if !c.CanRename() {
		t.Fatal("Hydranos 4.6 renomme")
	}
	// Multi-fichiers : le dossier part à part (from_folder, folder).
	err := c.RenameFiles(ctx, hash, []Rename{
		{0, "Release.Test/a.mkv", "Release.Test/mon film.mkv"},
		{1, "Release.Test/sub/b.mkv", "Release.Test/sub/b.mkv"}, // inchangé : rien à envoyer
	})
	want := map[string]string{"from": "a.mkv", "from_folder": "Release.Test", "to": "mon film.mkv", "folder": "Release.Test"}
	if err != nil || len(f.renames) != 1 || fmt.Sprint(f.renames[0]) != fmt.Sprint(want) {
		t.Fatalf("renommage : %v %v", err, f.renames)
	}
	// Le dossier change, fichier par fichier.
	f.renames = nil
	err = c.RenameFiles(ctx, hash, []Rename{{1, "Release.Test/sub/b.mkv", "Autre/sub/b.mkv"}})
	want = map[string]string{"from": "sub/b.mkv", "from_folder": "Release.Test", "to": "sub/b.mkv", "folder": "Autre"}
	if err != nil || len(f.renames) != 1 || fmt.Sprint(f.renames[0]) != fmt.Sprint(want) {
		t.Fatalf("changement de dossier : %v %v", err, f.renames)
	}
	// Fichier seul : chemin entier, sans folder (Hydranos le refuse).
	f.mono, f.renames = true, nil
	err = c.RenameFiles(ctx, hash, []Rename{{0, "Release.Test.mkv", "Autre.mkv"}})
	want = map[string]string{"from": "Release.Test.mkv", "to": "Autre.mkv"}
	if err != nil || len(f.renames) != 1 || fmt.Sprint(f.renames[0]) != fmt.Sprint(want) {
		t.Fatalf("fichier seul : %v %v", err, f.renames)
	}
	f.mono = false
	// Collision : le 409 dit pourquoi.
	err = c.RenameFiles(ctx, hash, []Rename{{0, "Release.Test/a.mkv", "Release.Test/occupe.mkv"}})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("collision : %v", err)
	}
}

func TestHydranosOld(t *testing.T) {
	f, c, hash := newFake(t, "4.4.0")
	ctx := context.Background()
	f.held["hoard"] = true
	if c.CanRename() {
		t.Fatal("Hydranos 4.4 ne renomme pas")
	}
	err := c.RenameFiles(ctx, hash, []Rename{{0, "Release.Test/a.mkv", "Release.Test/b.mkv"}})
	if err == nil || err.Error() != "Hydranos 4.6 ou plus requis pour renommer les fichiers d'un torrent" {
		t.Fatalf("renommage sur une route absente : %v", err)
	}
	// Route absente : on ne sait pas, ce n'est ni « existe » ni « n'existe pas ».
	if exists, supported, err := c.Lookup(ctx, "/data/Release.Test"); err != nil || supported || exists {
		t.Fatalf("lookup sur Hydranos ancien : %v %v %v", exists, supported, err)
	}
	// La version est lue une fois.
	n := 0
	for _, call := range f.calls {
		if call == "GET /api/status" {
			n++
		}
	}
	c.CanRename()
	c.CanRename()
	m := 0
	for _, call := range f.calls {
		if call == "GET /api/status" {
			m++
		}
	}
	if n != 1 || m != 1 {
		t.Fatalf("la version doit être sondée une fois : %d puis %d", n, m)
	}
}

func TestHydranosLookup(t *testing.T) {
	f, c, _ := newFake(t, "4.6.1")
	ctx := context.Background()
	for _, tc := range []struct {
		path              string
		exists, supported bool
	}{
		{"/data/Release.Test", true, true},        // présent
		{"/data/Release.Test/", true, true},       // barre finale
		{"/data/Absent", false, true},             // dossier listé, nom absent
		{"/nulle/part/Release.Test", false, true}, // dossier parent absent : 404 avec {"error"}
	} {
		exists, supported, err := c.Lookup(ctx, tc.path)
		if err != nil || exists != tc.exists || supported != tc.supported {
			t.Fatalf("%s : %v %v %v", tc.path, exists, supported, err)
		}
	}
	// Liste tronquée : un nom qui n'y figure pas n'est pas pour autant libre.
	if exists, supported, err := c.Lookup(ctx, "/gros/absent.mkv"); err != nil || supported || exists {
		t.Fatalf("liste tronquée, nom absent : %v %v %v", exists, supported, err)
	}
	if exists, supported, err := c.Lookup(ctx, "/gros/present.mkv"); err != nil || !supported || !exists {
		t.Fatalf("liste tronquée, nom présent : %v %v %v", exists, supported, err)
	}
	f.fsMode = "403"
	if exists, supported, err := c.Lookup(ctx, "/data/Release.Test"); err != nil || supported || exists {
		t.Fatalf("hors des racines : %v %v %v", exists, supported, err)
	}
	if _, supported, _ := c.Lookup(ctx, "relatif"); supported {
		t.Fatal("un chemin sans dossier n'est pas vérifiable")
	}
}

func TestHydranosVersion(t *testing.T) {
	for v, want := range map[string]bool{"4.8.1": true, "4.6": true, "v4.6.1-rc1": true, "4.17.2": true, "5.0.0": true, "4.5.9": false, "4.4.0": false, "3.9.9": false, "n'importe quoi": false, "": false} {
		n, ok := parseVersion(v)
		if got := ok && versionAtLeast(n, hydranosRename); got != want {
			t.Errorf("%q : %v, attendu %v", v, got, want)
		}
	}
	// Hydranos injoignable : pas de renommage, et rien de gardé.
	c, _ := New(Options{Kind: "hydranos", URL: "http://127.0.0.1:1", Pass: "k"})
	if c.CanRename() {
		t.Fatal("injoignable : pas de renommage")
	}
	if _, err := c.Version(context.Background()); err == nil || errors.Is(err, ErrAuth) {
		t.Fatalf("injoignable : %v", err)
	}
}

func TestHydranosAddWithoutHoard(t *testing.T) {
	raw, _ := sampleTorrent(t)
	f, c, _ := newFake(t, "4.8.1")
	f.noHoard = true
	err := c.Add(context.Background(), "x.torrent", raw, AddOptions{SavePath: "/data"})
	if err == nil || !strings.Contains(err.Error(), "no engine has the hoard role") {
		t.Fatalf("le refus d'Hydranos doit remonter tel quel : %v", err)
	}
}

// Le Lot écarte les torrents d'une étiquette à ne jamais publier (noHL) et le
// modèle {cat} reprend la catégorie : une ligne d'Hydranos doit les donner.
func TestHydranosRowCarriesTagsCategoryAndSavePath(t *testing.T) {
	var r hRow
	raw := `{"info_hash":"AB","name":"n","state":"seeding","progress":1,"category":"films","tags":["noHL"," ",""],"save_path":"/categorie","engine_save_path":"/data"}`
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatal(err)
	}
	got := r.torrent()
	if len(got.Tags) != 1 || got.Tags[0] != "noHL" {
		t.Fatalf("étiquettes : %q", got.Tags)
	}
	if got.Category != "films" {
		t.Fatalf("catégorie : %q", got.Category)
	}
	if got.SavePath != "/data" {
		t.Fatalf("dossier : %q", got.SavePath)
	}
	// Hydranos envoie null pour un torrent sans étiquette.
	var bare hRow
	if err := json.Unmarshal([]byte(`{"info_hash":"AB","name":"n","tags":null}`), &bare); err != nil {
		t.Fatal(err)
	}
	if bare.torrent().Tags != nil {
		t.Fatalf("sans étiquette : %q", bare.torrent().Tags)
	}
}

// Un Hydranos tient un moment l'ajout pour le prouver : D3X le voit en
// vérification, jamais à l'arrêt, et revérifier ne lui est pas refusé.
func TestHydranosHeldForProof(t *testing.T) {
	raw, _ := sampleTorrent(t)
	f, c, hash := newFake(t, "4.8.1")
	ctx := context.Background()
	if err := c.Add(ctx, "x.torrent", raw, AddOptions{SavePath: "/data"}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.hold = true
	f.mu.Unlock()

	read := func() Torrent {
		t.Helper()
		l, err := c.List(ctx, []string{hash})
		if err != nil || len(l) != 1 {
			t.Fatalf("liste : %v %v", l, err)
		}
		return l[0]
	}
	if x := read(); !x.Checking || x.Stopped || x.Progress >= 1 {
		t.Fatalf("pendant la retenue : Checking=%v Stopped=%v Progress=%v", x.Checking, x.Stopped, x.Progress)
	}
	if err := c.Recheck(ctx, hash); err != nil {
		t.Fatalf("revérifier pendant la retenue : %v", err)
	}
	if x := read(); !x.Checking || x.Stopped {
		t.Fatalf("la vérification sans effet ne doit rien changer : Checking=%v Stopped=%v", x.Checking, x.Stopped)
	}
	f.mu.Lock()
	n := f.verifyN
	f.hold = false
	f.paused["hoard"] = true // ajouté à l'arrêt : il y reste jusqu'à ce que D3X le démarre
	f.mu.Unlock()
	if n != 1 {
		t.Fatalf("verify reçus : %d", n)
	}
	if x := read(); x.Checking || !x.Stopped || x.Progress < 1 {
		t.Fatalf("retenue finie : Checking=%v Stopped=%v Progress=%v", x.Checking, x.Stopped, x.Progress)
	}
}

// List d'un lot lit /api/engines une seule fois, pas une fois par hash : la
// surveillance de D3X attend toute la liste, d'une minute au plus.
func TestHydranosListReadsEnginesOnce(t *testing.T) {
	raw, _ := sampleTorrent(t)
	f, c, hash := newFake(t, "4.8.1")
	ctx := context.Background()
	if err := c.Add(ctx, "x.torrent", raw, AddOptions{SavePath: "/data"}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
	l, err := c.List(ctx, []string{hash, strings.Repeat("0", 40), strings.Repeat("1", 40), hash})
	if err != nil {
		t.Fatal(err)
	}
	if len(l) != 2 || l[0].Hash != hash {
		t.Fatalf("lignes : %v", l)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, call := range f.calls {
		if call == "GET /api/engines" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("lectures de /api/engines : %d (%v)", n, f.calls)
	}
}

// content_folder (booléen, null tant qu'Hydranos ne le sait pas) dit si le
// contenu est rangé dans un dossier à son nom. Hydranos ne donne pas de
// content_path : un multi-fichiers dont le flag est faux a ses fichiers
// directement dans le dossier de sauvegarde, pas sous le nom du torrent.
func TestHydranosContentFolder(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name   string
		folder *bool
		multi  bool
		want   string
	}{
		{"multi avec dossier racine", &yes, true, "/data/Release.Test"},
		{"multi sans dossier racine", &no, true, "/data"},
		{"multi, flag inconnu : repli", nil, true, "/data/Release.Test"},
		{"fichier seul rangé dans un dossier", &yes, false, "/data/Release.Test"},
		{"fichier seul sans dossier", &no, false, "/data/Release.Test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, c, hash := newFake(t, "4.8.1")
			f.contentFolder, f.mono = tc.folder, !tc.multi
			f.held["hoard"] = true
			l, err := c.List(context.Background(), []string{hash})
			if err != nil || len(l) != 1 {
				t.Fatalf("liste : %v %v", l, err)
			}
			if l[0].ContentPath != tc.want {
				t.Fatalf("ContentPath = %q, attendu %q", l[0].ContentPath, tc.want)
			}
		})
	}
	// Un content_path donné par Hydranos garde la priorité.
	var r hRow
	if err := json.Unmarshal([]byte(`{"info_hash":"AB","name":"n","multi_file":true,"content_folder":false,"engine_save_path":"/d","content_path":"/d/x"}`), &r); err != nil {
		t.Fatal(err)
	}
	if r.torrent().ContentPath != "/d/x" {
		t.Fatalf("content_path : %q", r.torrent().ContentPath)
	}
}

// Avant 4.8.1, Hydranos montre « arrêté » un ajout qu'il est en train de
// prouver : la vérification de D3X y part sur un dossier vide et conclut à tort
// que les fichiers diffèrent. L'ajout est donc refusé, avec le message à suivre,
// et rien n'est envoyé.
func TestHydranosAddRequiresTheProofState(t *testing.T) {
	for _, v := range []string{"4.6.0", "4.8.0"} {
		f, c, _ := newFake(t, v)
		err := c.Add(context.Background(), "x.torrent", []byte("d"), AddOptions{SavePath: "/data"})
		if err == nil || !strings.Contains(err.Error(), "4.8.1") {
			t.Fatalf("%s : %v", v, err)
		}
		if f.held["hoard"] {
			t.Fatalf("%s : l'ajout est parti", v)
		}
	}
	_, c, _ := newFake(t, "4.8.1")
	if err := c.Add(context.Background(), "x.torrent", []byte("d"), AddOptions{SavePath: "/data"}); err != nil {
		t.Fatalf("4.8.1 refusée : %v", err)
	}
}

// D3X redémarré : il ne sait plus dans quel moteur il a ajouté le torrent.
// Tenu par race (la copie du membre) et par hoard (celle de D3X, qui ajoute
// toujours en mode hoard), l'arrêt et le retrait visent hoard, jamais race.
func TestHydranosPicksHoardAfterRestart(t *testing.T) {
	f, c, hash := newFake(t, "4.8.1")
	ctx := context.Background()
	f.held["race"], f.held["hoard"] = true, true
	if err := c.Stop(ctx, hash); err != nil || !f.paused["hoard"] || f.paused["race"] {
		t.Fatalf("arrêt : %v, moteurs arrêtés %v", err, f.paused)
	}
	if err := c.RemoveKeepFiles(ctx, hash); err != nil {
		t.Fatal(err)
	}
	if len(f.removed) != 1 || f.removed[0] != "hoard|engine=hoard" || !f.held["race"] {
		t.Fatalf("retrait : %v, race encore là : %v", f.removed, f.held["race"])
	}
}
