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
	"net/http/cookiejar"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/V3x0rV3x/D3X/internal/torrent"
)

// Deluge, par le JSON-RPC de son interface web (« /json », mot de passe de
// l'interface web). Pas d'export du .torrent.
type deluge struct {
	url  string
	pass string
	hc   *http.Client
	id   atomic.Int64

	mu    sync.Mutex
	ready bool
}

func newDeluge(o Options) (Client, error) {
	u, err := normURL(o.URL)
	if err != nil {
		return nil, err
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(u.Path, "/json") {
		u.Path += "/json"
	}
	jar, _ := cookiejar.New(nil)
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if o.Insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // choix du membre
	}
	return &deluge{url: u.String(), pass: o.Pass, hc: &http.Client{Jar: jar, Timeout: 2 * time.Minute, Transport: tr}}, nil
}

func (d *deluge) Kind() string { return "Deluge" }

func (d *deluge) raw(ctx context.Context, method string, params []interface{}, out interface{}) error {
	if params == nil {
		params = []interface{}{}
	}
	body, _ := json.Marshal(map[string]interface{}{"method": method, "params": params, "id": d.id.Add(1)})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, d.url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.hc.Do(req)
	if err != nil {
		return fmt.Errorf("Deluge injoignable : %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
			Code    int    `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return errors.New("ce n'est pas Deluge (interface web) qui répond à cette adresse")
	}
	if env.Error != nil {
		if env.Error.Code == 1 || strings.Contains(strings.ToLower(env.Error.Message), "not authenticated") {
			d.mu.Lock()
			d.ready = false
			d.mu.Unlock()
			return ErrAuth
		}
		if strings.Contains(strings.ToLower(env.Error.Message), "already") {
			return ErrAlreadyThere
		}
		return errors.New("Deluge : " + env.Error.Message)
	}
	if out != nil {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

// ensure : connexion à l'interface web, puis au démon si elle ne l'est pas.
func (d *deluge) ensure(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ready {
		return nil
	}
	var ok bool
	if err := d.raw(ctx, "auth.login", []interface{}{d.pass}, &ok); err != nil {
		return err
	}
	if !ok {
		return errors.New("Deluge a refusé le mot de passe de l'interface web")
	}
	var connected bool
	if err := d.raw(ctx, "web.connected", nil, &connected); err != nil {
		return err
	}
	if !connected {
		var hosts [][]interface{}
		if err := d.raw(ctx, "web.get_hosts", nil, &hosts); err != nil {
			return err
		}
		if len(hosts) == 0 || len(hosts[0]) == 0 {
			return errors.New("l'interface web de Deluge n'est reliée à aucun démon")
		}
		if err := d.raw(ctx, "web.connect", []interface{}{hosts[0][0]}, nil); err != nil {
			return err
		}
	}
	d.ready = true
	return nil
}

func (d *deluge) call(ctx context.Context, method string, params []interface{}, out interface{}) error {
	if err := d.ensure(ctx); err != nil {
		return err
	}
	err := d.raw(ctx, method, params, out)
	if errors.Is(err, ErrAuth) { // session expirée : une reconnexion
		if err := d.ensure(ctx); err != nil {
			return err
		}
		err = d.raw(ctx, method, params, out)
	}
	return err
}

// Deluge 2 : daemon.get_version ; Deluge 1.3 : daemon.info. Le test de
// connexion échouait sur « Unknown method » avec Deluge 2.1.1 (retour
// CoprinusComatus, 20/10/26).
func (d *deluge) Version(ctx context.Context) (string, error) {
	var v string
	err := d.call(ctx, "daemon.get_version", nil, &v)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unknown method") {
		err = d.call(ctx, "daemon.info", nil, &v)
	}
	return v, err
}

type dlTorrent struct {
	Name        string  `json:"name"`
	SavePath    string  `json:"save_path"`
	TotalSize   int64   `json:"total_size"`
	TotalWanted int64   `json:"total_wanted"`
	Progress    float64 `json:"progress"` // en pourcentage
	State       string  `json:"state"`    // Checking, Downloading, Seeding, Paused, Queued, Error, Moving
	Trackers    []struct {
		URL string `json:"url"`
	} `json:"trackers"`
	Label string `json:"label"` // plugin Label ; absent sans lui
	Files []struct {
		Index int    `json:"index"`
		Path  string `json:"path"`
		Size  int64  `json:"size"`
	} `json:"files"`
}

func (d *deluge) status(ctx context.Context, hashes []string, fields []string) (map[string]dlTorrent, error) {
	filter := map[string]interface{}{}
	if len(hashes) > 0 {
		filter["id"] = hashes
	}
	out := map[string]dlTorrent{}
	err := d.call(ctx, "core.get_torrents_status", []interface{}{filter, fields}, &out)
	return out, err
}

func (d *deluge) List(ctx context.Context, hashes []string) ([]Torrent, error) {
	m, err := d.status(ctx, hashes, []string{"name", "save_path", "total_size", "total_wanted", "progress", "state", "trackers", "label"})
	if err != nil {
		return nil, err
	}
	out := make([]Torrent, 0, len(m))
	for h, x := range m {
		var urls []string
		for _, tr := range x.Trackers {
			urls = append(urls, tr.URL)
		}
		h = strings.ToLower(h)
		p := x.Progress / 100
		out = append(out, Torrent{
			Hash: h, Hashes: []string{h}, Name: x.Name, ContentPath: joinPath(x.SavePath, x.Name),
			Size: x.TotalWanted, TotalSize: x.TotalSize, Progress: p,
			Checking:    x.State == "Checking" || x.State == "Allocating" || x.State == "Moving",
			Downloading: (x.State == "Downloading" || x.State == "Queued") && p < 1,
			Stopped:     x.State == "Paused",
			Trackers:    TrackerHosts(urls),
			Tags:        tagList([]string{x.Label}),
		})
	}
	return out, nil
}

func (d *deluge) Export(context.Context, string) ([]byte, error) { return nil, ErrNoExport }

func (d *deluge) Files(ctx context.Context, hash string) ([]torrent.File, int64, error) {
	m, err := d.status(ctx, []string{hash}, []string{"name", "total_size", "files"})
	if err != nil {
		return nil, 0, err
	}
	x, ok := m[strings.ToLower(hash)]
	if !ok {
		for _, v := range m {
			x, ok = v, true
		}
	}
	if !ok {
		return nil, 0, errors.New("torrent introuvable")
	}
	var out []torrent.File
	for _, f := range x.Files {
		out = append(out, torrent.File{Path: strings.ReplaceAll(f.Path, `\`, "/"), Size: f.Size, Index: f.Index})
	}
	return out, x.TotalSize, nil
}

func (d *deluge) Add(ctx context.Context, name string, raw []byte, o AddOptions) error {
	// Deluge relance un instant le torrent pour le vérifier : rien ne doit
	// pouvoir se télécharger avant 100 % (fichiers à « ne pas télécharger »,
	// rendus normaux au démarrage), et rien ne doit être déplacé.
	opts := map[string]interface{}{"add_paused": true, "seed_mode": false, "move_completed": false}
	if t, err := torrent.Parse(raw); err == nil && t.FileCount > 0 {
		opts["file_priorities"] = make([]int, t.FileCount)
	}
	if o.SavePath != "" {
		opts["download_location"] = o.SavePath
	}
	var id *string
	if err := d.call(ctx, "core.add_torrent_file", []interface{}{name, base64.StdEncoding.EncodeToString(raw), opts}, &id); err != nil {
		return err
	}
	if id == nil {
		return ErrAlreadyThere
	}
	return nil
}

func (d *deluge) Recheck(ctx context.Context, h string) error {
	return d.call(ctx, "core.force_recheck", []interface{}{[]string{h}}, nil)
}

// Deluge 2 : resume_torrents / pause_torrents (liste) ; Deluge 1.3 :
// resume_torrent / pause_torrent (liste aussi).
func (d *deluge) both(ctx context.Context, v2, v1, h string) error {
	err := d.call(ctx, v2, []interface{}{[]string{h}}, nil)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unknown method") {
		err = d.call(ctx, v1, []interface{}{[]string{h}}, nil)
	}
	return err
}

// Start rend aux fichiers leur priorité normale (bloqués à l'ajout), puis
// démarre : seulement après une vérification à 100 %.
func (d *deluge) Start(ctx context.Context, h string) error {
	if m, err := d.status(ctx, []string{h}, []string{"files"}); err == nil {
		for _, x := range m {
			if n := len(x.Files); n > 0 {
				prio := make([]int, n)
				for i := range prio {
					prio[i] = 4
				}
				if err := d.call(ctx, "core.set_torrent_options", []interface{}{[]string{h}, map[string]interface{}{"file_priorities": prio}}, nil); err != nil {
					return err
				}
			}
		}
	}
	return d.both(ctx, "core.resume_torrents", "core.resume_torrent", h)
}
func (d *deluge) Stop(ctx context.Context, h string) error {
	return d.both(ctx, "core.pause_torrents", "core.pause_torrent", h)
}
func (d *deluge) RemoveKeepFiles(ctx context.Context, h string) error {
	return d.call(ctx, "core.remove_torrent", []interface{}{h, false}, nil)
}
