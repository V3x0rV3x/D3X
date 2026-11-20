package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/V3x0rV3x/D3X/internal/torrent"
)

// Transmission, par son RPC JSON (« /transmission/rpc »). Pas d'export du
// .torrent : les empreintes se calculent sur la liste de fichiers.
type transmission struct {
	rpc  string
	user string
	pass string
	hc   *http.Client

	mu  sync.Mutex
	sid string // X-Transmission-Session-Id, négocié au premier refus 409
}

func newTransmission(o Options) (Client, error) {
	u, err := normURL(o.URL)
	if err != nil {
		return nil, err
	}
	u.Path, u.RawPath, u.RawQuery, u.Fragment = trRPCPath(u.Path), "", "", ""
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if o.Insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // choix du membre
	}
	return &transmission{rpc: u.String(), user: o.User, pass: o.Pass, hc: &http.Client{Timeout: 2 * time.Minute, Transport: tr}}, nil
}

// trRPCPath : le chemin du RPC d'après l'adresse saisie. L'adresse de
// l'interface web, copiée telle quelle depuis le navigateur
// (« :9091/transmission/web/ »), visait « /transmission/web/transmission/rpc »
// et Transmission répondait 404 (neogamaes, 19/11/26). Un chemin qui finit
// par /rpc (RPC déplacé, proxy) est gardé tel quel.
func trRPCPath(p string) string {
	p = strings.TrimRight(p, "/")
	if strings.HasSuffix(p, "/rpc") {
		return p
	}
	// Interface web (…/transmission/web/…) ou dossier /transmission : on
	// remonte au-dessus. Tout autre chemin (proxy) est gardé comme avant.
	if i := strings.Index(p, "/transmission/web"); i >= 0 {
		p = p[:i]
	} else {
		p = strings.TrimSuffix(p, "/transmission")
	}
	return p + "/transmission/rpc"
}

// trHTTPError : un refus de Transmission, dit en clair. Ses deux listes
// blanches répondent tout de suite, avec un code que le membre ne sait pas
// lire : 403 pour l'adresse IP de D3X (un conteneur Docker a sa propre
// adresse, souvent 172.x), 421 pour le nom d'hôte (nom du conteneur).
func trHTTPError(status int, body []byte, host string) error {
	low := strings.ToLower(string(body))
	switch {
	case status == http.StatusForbidden && strings.Contains(low, "too many unsuccessful login"):
		return errors.New("Transmission bloque l'accès après trop d'échecs de mot de passe : redémarre Transmission, puis vérifie l'utilisateur et le mot de passe")
	case status == http.StatusForbidden && strings.Contains(low, "whitelist"):
		return errors.New("Transmission refuse l'adresse IP de D3X (liste « rpc-whitelist ») : ajoute l'adresse de la machine ou du conteneur de D3X à rpc-whitelist dans le settings.json de Transmission (Transmission arrêté), ou mets rpc-whitelist-enabled à false ; image linuxserver : variable WHITELIST")
	case status == http.StatusForbidden:
		return errors.New("Transmission refuse l'accès (HTTP 403)")
	case status == http.StatusMisdirectedRequest:
		return fmt.Errorf("Transmission ne reconnaît pas le nom « %s » (liste « rpc-host-whitelist ») : mets plutôt l'adresse IP de la machine, ou ajoute ce nom à rpc-host-whitelist dans le settings.json de Transmission ; image linuxserver : variable HOST_WHITELIST", host)
	case status == http.StatusNotFound:
		return errors.New("rien de Transmission à cette adresse (HTTP 404) : vérifie le port (9091 par défaut) et le chemin (/transmission/rpc)")
	}
	return fmt.Errorf("Transmission : HTTP %d", status)
}

func (t *transmission) Kind() string { return "Transmission" }

func (t *transmission) call(ctx context.Context, method string, args interface{}, out interface{}) error {
	body, _ := json.Marshal(map[string]interface{}{"method": method, "arguments": args})
	for attempt := 0; attempt < 2; attempt++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, t.rpc, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		t.mu.Lock()
		req.Header.Set("X-Transmission-Session-Id", t.sid)
		t.mu.Unlock()
		if t.user != "" || t.pass != "" {
			req.SetBasicAuth(t.user, t.pass)
		}
		resp, err := t.hc.Do(req)
		if err != nil {
			return fmt.Errorf("Transmission injoignable : %w", err)
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusConflict:
			t.mu.Lock()
			t.sid = resp.Header.Get("X-Transmission-Session-Id")
			t.mu.Unlock()
			continue
		case http.StatusUnauthorized:
			return ErrAuth
		}
		if resp.StatusCode != http.StatusOK {
			return trHTTPError(resp.StatusCode, data, req.URL.Hostname())
		}
		var env struct {
			Result    string          `json:"result"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(data, &env); err != nil {
			return errors.New("ce n'est pas Transmission qui répond à cette adresse")
		}
		if env.Result != "success" {
			if strings.Contains(env.Result, "duplicate") {
				return ErrAlreadyThere
			}
			return errors.New("Transmission : " + env.Result)
		}
		if out != nil {
			return json.Unmarshal(env.Arguments, out)
		}
		return nil
	}
	return errors.New("Transmission : session non négociée")
}

func (t *transmission) Version(ctx context.Context) (string, error) {
	var out struct {
		Version string `json:"version"`
	}
	err := t.call(ctx, "session-get", map[string]interface{}{"fields": []string{"version"}}, &out)
	return out.Version, err
}

type trTorrent struct {
	Hash        string  `json:"hashString"`
	Name        string  `json:"name"`
	TotalSize   int64   `json:"totalSize"`
	SizeWhen    int64   `json:"sizeWhenDone"`
	DownloadDir string  `json:"downloadDir"`
	PercentDone float64 `json:"percentDone"`
	Status      int     `json:"status"` // 0 arrêté, 1-2 vérification, 3-4 téléchargement, 5-6 seed
	Trackers    []struct {
		Announce string `json:"announce"`
	} `json:"trackers"`
	Labels []string `json:"labels"`
	Files  []struct {
		Name   string `json:"name"`
		Length int64  `json:"length"`
	} `json:"files"`
}

func (t *transmission) get(ctx context.Context, hashes []string, fields []string) ([]trTorrent, error) {
	args := map[string]interface{}{"fields": fields}
	if len(hashes) > 0 {
		args["ids"] = hashes
	}
	var out struct {
		Torrents []trTorrent `json:"torrents"`
	}
	err := t.call(ctx, "torrent-get", args, &out)
	return out.Torrents, err
}

func (t *transmission) List(ctx context.Context, hashes []string) ([]Torrent, error) {
	list, err := t.get(ctx, hashes, []string{"hashString", "name", "totalSize", "sizeWhenDone", "downloadDir", "percentDone", "status", "trackers", "labels"})
	if err != nil {
		return nil, err
	}
	out := make([]Torrent, 0, len(list))
	for _, x := range list {
		var urls []string
		for _, tr := range x.Trackers {
			urls = append(urls, tr.Announce)
		}
		h := strings.ToLower(x.Hash)
		out = append(out, Torrent{
			Hash: h, Hashes: []string{h}, Name: x.Name, ContentPath: joinPath(x.DownloadDir, x.Name),
			Size: x.SizeWhen, TotalSize: x.TotalSize, Progress: x.PercentDone,
			Checking:    x.Status == 1 || x.Status == 2,
			Downloading: (x.Status == 3 || x.Status == 4) && x.PercentDone < 1,
			Stopped:     x.Status == 0,
			Trackers:    TrackerHosts(urls),
			Tags:        tagList(x.Labels),
		})
	}
	return out, nil
}

func (t *transmission) Export(context.Context, string) ([]byte, error) { return nil, ErrNoExport }

func (t *transmission) Files(ctx context.Context, hash string) ([]torrent.File, int64, error) {
	list, err := t.get(ctx, []string{hash}, []string{"hashString", "totalSize", "files"})
	if err != nil {
		return nil, 0, err
	}
	if len(list) == 0 {
		return nil, 0, errors.New("torrent introuvable")
	}
	var out []torrent.File
	for i, f := range list[0].Files {
		out = append(out, torrent.File{Path: f.Name, Size: f.Length, Index: i})
	}
	return out, list[0].TotalSize, nil
}

func (t *transmission) Add(ctx context.Context, name string, raw []byte, o AddOptions) error {
	args := map[string]interface{}{"metainfo": base64.StdEncoding.EncodeToString(raw), "paused": true}
	if o.SavePath != "" {
		args["download-dir"] = o.SavePath
	}
	if o.Tag != "" {
		args["labels"] = []string{o.Tag}
	}
	var out map[string]json.RawMessage
	if err := t.call(ctx, "torrent-add", args, &out); err != nil {
		return err
	}
	if _, dup := out["torrent-duplicate"]; dup {
		return ErrAlreadyThere
	}
	return nil
}

func (t *transmission) ids(ctx context.Context, method, hash string, extra map[string]interface{}) error {
	args := map[string]interface{}{"ids": []string{hash}}
	for k, v := range extra {
		args[k] = v
	}
	return t.call(ctx, method, args, nil)
}

func (t *transmission) Recheck(ctx context.Context, h string) error {
	return t.ids(ctx, "torrent-verify", h, nil)
}
func (t *transmission) Start(ctx context.Context, h string) error {
	return t.ids(ctx, "torrent-start", h, nil)
}
func (t *transmission) Stop(ctx context.Context, h string) error {
	return t.ids(ctx, "torrent-stop", h, nil)
}
func (t *transmission) RemoveKeepFiles(ctx context.Context, h string) error {
	return t.ids(ctx, "torrent-remove", h, map[string]interface{}{"delete-local-data": false})
}
