package client

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/V3x0rV3x/D3X/internal/bencode"
	"github.com/V3x0rV3x/D3X/internal/torrent"
)

func sampleTorrent(t *testing.T) ([]byte, string) {
	t.Helper()
	info := bencode.Dict{"name": []byte("Release.Test"), "piece length": int64(1 << 20), "pieces": make([]byte, 20),
		"files": []bencode.Value{bencode.Dict{"length": int64(1000), "path": []bencode.Value{[]byte("a.mkv")}}}}
	raw, _ := bencode.Encode(bencode.Dict{"announce": []byte("https://x.invalid/a"), "info": info})
	p, err := torrent.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return raw, strings.ToLower(p.InfoHash)
}

// ---------- Transmission ----------

func TestTransmission(t *testing.T) {
	raw, hash := sampleTorrent(t)
	var calls []string
	var addArgs, removeArgs map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/transmission/rpc" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if u, p, _ := r.BasicAuth(); u != "moi" || p != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("X-Transmission-Session-Id") != "sid42" {
			w.Header().Set("X-Transmission-Session-Id", "sid42")
			w.WriteHeader(http.StatusConflict)
			return
		}
		var req struct {
			Method    string                 `json:"method"`
			Arguments map[string]interface{} `json:"arguments"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		calls = append(calls, req.Method)
		res := map[string]interface{}{}
		switch req.Method {
		case "session-get":
			res["version"] = "4.0.6"
		case "torrent-get":
			res["torrents"] = []map[string]interface{}{{"hashString": strings.ToUpper(hash), "name": "Release.Test", "totalSize": 1000, "sizeWhenDone": 1000,
				"downloadDir": "/data", "percentDone": 1.0, "status": 2, "trackers": []map[string]string{{"announce": "https://announce.v3x.club/pk/announce"}},
				"files": []map[string]interface{}{{"name": "Release.Test/a.mkv", "length": 1000}}}}
		case "torrent-add":
			addArgs = req.Arguments
			res["torrent-added"] = map[string]interface{}{"hashString": hash}
		case "torrent-remove":
			removeArgs = req.Arguments
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"result": "success", "arguments": res})
	}))
	defer srv.Close()

	c, err := New(Options{Kind: "transmission", URL: srv.URL, User: "moi", Pass: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if v, err := c.Version(ctx); err != nil || v != "4.0.6" {
		t.Fatalf("version : %q %v", v, err)
	}
	list, err := c.List(ctx, nil)
	if err != nil || len(list) != 1 {
		t.Fatalf("liste : %v %v", list, err)
	}
	tor := list[0]
	if tor.Hash != hash || tor.ContentPath != "/data/Release.Test" || !tor.Checking || tor.Trackers[0] != "announce.v3x.club" {
		t.Fatalf("torrent : %+v", tor)
	}
	files, total, err := c.Files(ctx, hash)
	if err != nil || total != 1000 || files[0].Path != "Release.Test/a.mkv" {
		t.Fatalf("fichiers : %v %d %v", files, total, err)
	}
	if _, err := c.Export(ctx, hash); err != ErrNoExport {
		t.Fatal("Transmission n'exporte pas")
	}
	if err := c.Add(ctx, "x.torrent", raw, AddOptions{SavePath: "/data", Tag: "D3X"}); err != nil {
		t.Fatal(err)
	}
	if addArgs["paused"] != true || addArgs["download-dir"] != "/data" {
		t.Fatalf("ajout : %v", addArgs)
	}
	if b, _ := base64.StdEncoding.DecodeString(addArgs["metainfo"].(string)); string(b) != string(raw) {
		t.Fatal("le .torrent envoyé n'est pas le bon")
	}
	for _, f := range []func(context.Context, string) error{c.Recheck, c.Start, c.Stop, c.RemoveKeepFiles} {
		if err := f(ctx, hash); err != nil {
			t.Fatal(err)
		}
	}
	if removeArgs["delete-local-data"] != false {
		t.Fatalf("retrait : %v ; les fichiers doivent rester", removeArgs)
	}

	bad, _ := New(Options{Kind: "transmission", URL: srv.URL, User: "moi", Pass: "faux"})
	if _, err := bad.Version(ctx); err != ErrAuth {
		t.Fatalf("mauvais mot de passe : %v", err)
	}
}

// ---------- Deluge ----------

func TestDeluge(t *testing.T) {
	raw, hash := sampleTorrent(t)
	logged, connected := false, false
	var addOpts map[string]interface{}
	var removeParams []interface{}
	var resumeMethod string
	var prioBeforeResume interface{}
	var lastPrio interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string        `json:"method"`
			Params []interface{} `json:"params"`
			ID     int           `json:"id"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		reply := func(res interface{}, errMsg string) {
			out := map[string]interface{}{"id": req.ID, "result": res, "error": nil}
			if errMsg != "" {
				out["error"] = map[string]interface{}{"message": errMsg, "code": 2}
			}
			json.NewEncoder(w).Encode(out)
		}
		if req.Method == "auth.login" {
			logged = req.Params[0] == "deluge"
			if logged {
				http.SetCookie(w, &http.Cookie{Name: "_session_id", Value: "s", Path: "/"})
			}
			reply(logged, "")
			return
		}
		if c, err := r.Cookie("_session_id"); err != nil || c.Value != "s" {
			json.NewEncoder(w).Encode(map[string]interface{}{"id": req.ID, "result": nil, "error": map[string]interface{}{"message": "Not authenticated", "code": 1}})
			return
		}
		switch req.Method {
		case "web.connected":
			reply(connected, "")
		case "web.get_hosts":
			reply([][]interface{}{{"host1", "127.0.0.1", 58846, "Online"}}, "")
		case "web.connect":
			connected = true
			reply(nil, "")
		case "daemon.get_version":
			reply("2.1.1", "") // Deluge 2 : daemon.info n'existe plus
		case "daemon.info":
			reply(nil, "Unknown method")
		case "core.get_torrents_status":
			reply(map[string]interface{}{hash: map[string]interface{}{"name": "Release.Test", "save_path": "/dl", "total_size": 1000, "total_wanted": 1000,
				"progress": 100.0, "state": "Paused", "trackers": []map[string]string{{"url": "https://autre.invalid/a"}},
				"files": []map[string]interface{}{{"path": "Release.Test/a.mkv", "size": 1000}}}}, "")
		case "core.add_torrent_file":
			addOpts = req.Params[2].(map[string]interface{})
			reply(hash, "")
		case "core.resume_torrents":
			reply(nil, "Unknown method") // simule Deluge 1.3
		case "core.resume_torrent":
			resumeMethod = req.Method
			prioBeforeResume = lastPrio
			reply(nil, "")
		case "core.set_torrent_options":
			lastPrio = req.Params[1].(map[string]interface{})["file_priorities"]
			reply(nil, "")
		case "core.remove_torrent":
			removeParams = req.Params
			reply(true, "")
		default:
			reply(nil, "")
		}
	}))
	defer srv.Close()

	c, _ := New(Options{Kind: "deluge", URL: srv.URL, Pass: "deluge"})
	ctx := context.Background()
	if v, err := c.Version(ctx); err != nil || v != "2.1.1" || !connected {
		t.Fatalf("version : %q %v (démon relié : %v)", v, err, connected)
	}
	list, err := c.List(ctx, []string{hash})
	if err != nil || len(list) != 1 || list[0].ContentPath != "/dl/Release.Test" || list[0].Progress != 1 || list[0].Checking {
		t.Fatalf("liste : %+v %v", list, err)
	}
	files, total, err := c.Files(ctx, hash)
	if err != nil || total != 1000 || files[0].Path != "Release.Test/a.mkv" {
		t.Fatalf("fichiers : %v %v", files, err)
	}
	if err := c.Add(ctx, "x.torrent", raw, AddOptions{SavePath: "/dl"}); err != nil {
		t.Fatal(err)
	}
	if addOpts["add_paused"] != true || addOpts["download_location"] != "/dl" || addOpts["move_completed"] != false {
		t.Fatalf("ajout : %v", addOpts)
	}
	// Rien ne peut se télécharger avant 100 % : tous les fichiers à 0.
	if pr, ok := addOpts["file_priorities"].([]interface{}); !ok || len(pr) == 0 || pr[0] != 0.0 {
		t.Fatalf("priorités à l'ajout : %v", addOpts["file_priorities"])
	}
	if err := c.Start(ctx, hash); err != nil || resumeMethod != "core.resume_torrent" {
		t.Fatalf("démarrage (repli 1.3) : %v %q", err, resumeMethod)
	}
	if pr, ok := prioBeforeResume.([]interface{}); !ok || len(pr) != 1 || pr[0] != 4.0 {
		t.Fatalf("priorités rendues avant le démarrage : %v", prioBeforeResume)
	}
	if err := c.RemoveKeepFiles(ctx, hash); err != nil || removeParams[1] != false {
		t.Fatalf("retrait : %v %v ; les fichiers doivent rester", removeParams, err)
	}
	bad, _ := New(Options{Kind: "deluge", URL: srv.URL, Pass: "faux"})
	if _, err := bad.Version(ctx); err == nil {
		t.Fatal("mauvais mot de passe accepté")
	}
}

// ---------- rTorrent ----------

type xmlCall struct {
	Method string `xml:"methodName"`
	Params []struct {
		Value node `xml:"value"`
	} `xml:"params>param"`
}

func xmlResp(v interface{}) string {
	var b bytes.Buffer
	xmlValue(&b, v)
	return `<?xml version="1.0"?><methodResponse><params><param>` + b.String() + `</param></params></methodResponse>`
}

func TestRTorrent(t *testing.T) {
	raw, hash := sampleTorrent(t)
	var loaded []interface{}
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, _ := r.BasicAuth(); u != "box" || p != "pw" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var c xmlCall
		if err := xml.Unmarshal(body, &c); err != nil {
			t.Errorf("XML illisible : %v", err)
			return
		}
		calls = append(calls, c.Method)
		params := []interface{}{}
		for _, p := range c.Params {
			params = append(params, decodeValue(p.Value))
		}
		switch c.Method {
		case "system.client_version":
			io.WriteString(w, xmlResp("0.9.8"))
		case "d.multicall2":
			row := []interface{}{strings.ToUpper(hash), "Release.Test", "/home/box/dl/Release.Test", 1000, 1000, 0, 1, 1, 1}
			io.WriteString(w, xmlResp([]interface{}{row}))
		case "system.multicall":
			io.WriteString(w, xmlResp([]interface{}{[]interface{}{[]interface{}{[]interface{}{"https://announce.v3x.club/pk/announce"}}}}))
		case "f.multicall":
			io.WriteString(w, xmlResp([]interface{}{[]interface{}{"a.mkv", 1000}}))
		case "d.is_multi_file":
			io.WriteString(w, xmlResp(1))
		case "load.raw":
			loaded = params
			io.WriteString(w, xmlResp(0))
		case "d.erase", "d.check_hash", "d.start", "d.stop":
			io.WriteString(w, xmlResp(0))
		default:
			io.WriteString(w, `<?xml version="1.0"?><methodResponse><fault><value><struct><member><name>faultCode</name><value><i4>-506</i4></value></member><member><name>faultString</name><value><string>Method not defined</string></value></member></struct></value></fault></methodResponse>`)
		}
	}))
	defer srv.Close()

	u := strings.Replace(srv.URL, "http://", "http://box:pw@", 1) + "/RPC2"
	c, _ := New(Options{Kind: "rtorrent", URL: u})
	ctx := context.Background()
	if v, err := c.Version(ctx); err != nil || v != "0.9.8" {
		t.Fatalf("version : %q %v", v, err)
	}
	list, err := c.List(ctx, nil)
	if err != nil || len(list) != 1 {
		t.Fatalf("liste : %v %v", list, err)
	}
	tor := list[0]
	if tor.Hash != hash || tor.ContentPath != "/home/box/dl/Release.Test" || !tor.Checking || len(tor.Trackers) != 1 || tor.Trackers[0] != "announce.v3x.club" {
		t.Fatalf("torrent : %+v", tor)
	}
	files, total, err := c.Files(ctx, hash)
	if err != nil || total != 1000 || len(files) != 1 || files[0].Path != "Release.Test/a.mkv" {
		t.Fatalf("fichiers : %v %d %v", files, total, err)
	}
	// Déjà dans le client (même info-hash) : refusé sans rien envoyer.
	if err := c.Add(ctx, "x.torrent", raw, AddOptions{SavePath: "/home/box/dl"}); err != ErrAlreadyThere {
		t.Fatalf("doublon : %v", err)
	}
	for _, f := range []func(context.Context, string) error{c.Recheck, c.Start, c.Stop} {
		if err := f(ctx, hash); err != nil {
			t.Fatal(err)
		}
	}
	// Retrait : jamais avec rTorrent (un réglage peut effacer les données) ;
	// arrêté, fermé, et le membre est prévenu.
	if err := c.RemoveKeepFiles(ctx, hash); err == nil || !strings.Contains(err.Error(), "ruTorrent") {
		t.Fatalf("retrait : %v", err)
	}
	if _, err := New(Options{Kind: "rtorrent", URL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	noauth, _ := New(Options{Kind: "rtorrent", URL: srv.URL})
	if _, err := noauth.Version(ctx); err != ErrAuth {
		t.Fatalf("sans identifiants : %v", err)
	}
	_ = loaded
}

func TestRTorrentAddAndFault(t *testing.T) {
	raw, _ := sampleTorrent(t)
	var loaded []interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var c xmlCall
		xml.Unmarshal(body, &c)
		switch c.Method {
		case "d.multicall2":
			io.WriteString(w, xmlResp([]interface{}{}))
		case "load.raw":
			for _, p := range c.Params {
				loaded = append(loaded, decodeValue(p.Value))
			}
			io.WriteString(w, xmlResp(0))
		default:
			io.WriteString(w, `<?xml version="1.0"?><methodResponse><fault><value><struct><member><name>faultString</name><value><string>Could not find info-hash.</string></value></member></struct></value></fault></methodResponse>`)
		}
	}))
	defer srv.Close()
	c, _ := New(Options{Kind: "rtorrent", URL: srv.URL})
	ctx := context.Background()
	if err := c.Add(ctx, "x.torrent", raw, AddOptions{SavePath: `/home/box/dl "a"`, Tag: "D3X"}); err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 4 || loaded[0] != "" || loaded[1] != string(raw) || loaded[2] != `d.directory.set="/home/box/dl \"a\""` || loaded[3] != `d.custom1.set="D3X"` {
		t.Fatalf("load.raw : %q", loaded)
	}
	if err := c.Start(ctx, "abc"); err == nil || !strings.Contains(err.Error(), "Could not find info-hash") {
		t.Fatalf("erreur XML-RPC : %v", err)
	}
}

// tagList : les étiquettes telles que les clients les donnent (chaîne « a, b »
// de qBittorrent coupée sur la virgule, labels de Transmission, label de
// Deluge, custom1 de rTorrent), sans les blancs ni les vides.
func TestTagList(t *testing.T) {
	got := tagList(strings.Split("noHL, V3X,, cross-seed ", ","))
	if len(got) != 3 || got[0] != "noHL" || got[1] != "V3X" || got[2] != "cross-seed" {
		t.Fatalf("qBittorrent : %q", got)
	}
	if got := tagList([]string{" ", ""}); got != nil {
		t.Fatalf("rien : %q, attendu nil", got)
	}
	if got := tagList(nil); got != nil {
		t.Fatalf("nil : %q", got)
	}
}
