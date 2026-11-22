// Package qbit parle à qBittorrent par son interface web (API v2, qBittorrent
// 4.5 ou plus pour l'export des .torrent).
//
// D3X ne touche jamais aux fichiers : il demande à qBittorrent de vérifier les
// données, et ne démarre un torrent que s'il est complet à 100 %.
package qbit

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client est une session ouverte sur l'interface web de qBittorrent.
type Client struct {
	base    string // sans identifiants, sans « / » final
	referer string
	user    string
	pass    string
	hc      *http.Client

	mu       sync.Mutex
	loggedIn bool
	v5       bool // API 2.11+ : stop/start au lieu de pause/resume
}

// Options règle la connexion.
type Options struct {
	URL      string
	User     string
	Pass     string
	Insecure bool // certificat non vérifié (seedbox en auto-signé)
}

// Torrent est un torrent tel que le liste qBittorrent.
type Torrent struct {
	Hash        string  `json:"hash"`
	InfoHashV1  string  `json:"infohash_v1"`
	Name        string  `json:"name"`
	SavePath    string  `json:"save_path"`
	ContentPath string  `json:"content_path"`
	Size        int64   `json:"size"`
	TotalSize   int64   `json:"total_size"`
	Progress    float64 `json:"progress"`
	State       string  `json:"state"`
	Tracker     string  `json:"tracker"`
	MagnetURI   string  `json:"magnet_uri"`
	Category    string  `json:"category"`
	Tags        string  `json:"tags"`
}

// Hashes renvoie les identifiants connus du torrent, en minuscules.
func (t Torrent) Hashes() []string {
	out := []string{strings.ToLower(t.Hash)}
	if v1 := strings.ToLower(t.InfoHashV1); v1 != "" && v1 != out[0] {
		out = append(out, v1)
	}
	return out
}

// TrackerHosts renvoie les hôtes des trackers du torrent (lus dans le lien
// magnet, qui les liste tous ; à défaut, le tracker en cours).
func (t Torrent) TrackerHosts() []string {
	var hosts []string
	if u, err := url.Parse(t.MagnetURI); err == nil {
		for _, tr := range u.Query()["tr"] {
			if h := hostOf(tr); h != "" {
				hosts = append(hosts, h)
			}
		}
	}
	if len(hosts) == 0 {
		if h := hostOf(t.Tracker); h != "" {
			hosts = append(hosts, h)
		}
	}
	return hosts
}

func hostOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// Checking dit si qBittorrent vérifie (ou s'apprête à vérifier) les données.
func (t Torrent) Checking() bool {
	switch t.State {
	case "checkingUP", "checkingDL", "checkingResumeData", "queuedForChecking", "moving", "allocating", "metaDL", "forcedMetaDL":
		return true
	}
	return false
}

// New prépare un client ; la connexion se fait au premier appel.
func New(o Options) (*Client, error) {
	raw := strings.TrimSpace(o.URL)
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, errors.New("adresse de qBittorrent invalide (exemple : http://127.0.0.1:8080)")
	}
	// Identifiants dans l'adresse (seedbox derrière une authentification HTTP) :
	// Go les envoie lui-même, mais ils ne doivent jamais partir en Referer.
	clean := *u
	clean.User = nil
	jar, _ := cookiejar.New(nil)
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if o.Insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // choix explicite du membre
	}
	c := &Client{
		base:    strings.TrimRight(u.String(), "/"),
		referer: strings.TrimRight(clean.String(), "/"),
		user:    o.User,
		pass:    o.Pass,
		hc:      &http.Client{Jar: jar, Timeout: 2 * time.Minute, Transport: &basicAuth{next: tr, user: o.User, pass: o.Pass}},
	}
	return c, nil
}

// ErrAuth signale des identifiants refusés.
var ErrAuth = errors.New("qBittorrent a répondu « Fails. » : utilisateur ou mot de passe de l'interface web refusé (après plusieurs échecs, qBittorrent bloque l'adresse quelques minutes). S'il faut le ressaisir après chaque redémarrage de qBittorrent, c'est son mot de passe temporaire (image Docker) : fixe-en un dans Outils ▸ Options ▸ Interface Web")

func (c *Client) login(ctx context.Context) error {
	form := url.Values{"username": {c.user}, "password": {c.pass}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/v2/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", c.referer)
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("qBittorrent injoignable à %s : %w", c.referer, err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	text := strings.TrimSpace(string(body))
	if resp.StatusCode == http.StatusOK && strings.HasPrefix(text, "Ok") {
		return nil
	}
	if resp.StatusCode == http.StatusForbidden && strings.Contains(strings.ToLower(text), "banned") {
		return errors.New("qBittorrent a bloqué cette adresse après trop d'échecs de connexion : attends quelques minutes, puis vérifie le mot de passe")
	}
	// Identification du serveur (seedbox) refusée : D3X y a déjà présenté le
	// pseudo et le mot de passe des champs (basicAuth).
	if resp.StatusCode == http.StatusUnauthorized {
		return errors.New("l'adresse demande d'abord une identification du serveur (la fenêtre « identifiez-vous » d'une seedbox) et refuse le pseudo et le mot de passe saisis : mets ceux que ton hébergeur t'a donnés dans les champs de ce client")
	}
	// Accès libre (adresse en liste blanche, « Contourner l'authentification ») :
	// l'API répond sans cookie, on continue.
	if v, err := c.rawGet(ctx, "/api/v2/app/version", nil); err == nil && strings.HasPrefix(strings.TrimSpace(string(v)), "v") {
		return nil
	}
	if strings.HasPrefix(text, "Fails") {
		return ErrAuth
	}
	if len(text) > 80 {
		text = text[:80] + "…"
	}
	return fmt.Errorf("ce n'est pas qBittorrent qui répond à %s (HTTP %d, « %s ») : vérifie l'adresse et le port de l'interface web, ou un proxy devant", c.referer, resp.StatusCode, text)
}

func (c *Client) ensure(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loggedIn {
		return nil
	}
	if err := c.login(ctx); err != nil {
		return err
	}
	if v, err := c.rawGet(ctx, "/api/v2/app/webapiVersion", nil); err == nil {
		c.v5 = apiAtLeast(strings.TrimSpace(string(v)), 2, 11)
	}
	c.loggedIn = true
	return nil
}

func apiAtLeast(v string, major, minor int) bool {
	var a, b int
	fmt.Sscanf(v, "%d.%d", &a, &b)
	return a > major || (a == major && b >= minor)
}

func (c *Client) rawGet(ctx context.Context, path string, q url.Values) ([]byte, error) {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("Referer", c.referer)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return data, &HTTPError{Path: path, Status: resp.StatusCode}
	}
	return data, nil
}

// HTTPError est une réponse inattendue de qBittorrent.
type HTTPError struct {
	Path   string
	Status int
}

func (e *HTTPError) Error() string { return fmt.Sprintf("qBittorrent %s : HTTP %d", e.Path, e.Status) }

// call fait un appel authentifié ; une session expirée (403) est rouverte une fois.
func (c *Client) call(ctx context.Context, method, path string, q url.Values, body io.Reader, ctype string) ([]byte, error) {
	if err := c.ensure(ctx); err != nil {
		return nil, err
	}
	var buf []byte
	if body != nil {
		buf, _ = io.ReadAll(body)
	}
	for attempt := 0; attempt < 2; attempt++ {
		u := c.base + path
		if method == http.MethodGet && len(q) > 0 {
			u += "?" + q.Encode()
		}
		var rd io.Reader
		if method == http.MethodPost {
			if buf != nil {
				rd = bytes.NewReader(buf)
			} else {
				rd = strings.NewReader(q.Encode())
				ctype = "application/x-www-form-urlencoded"
			}
		}
		req, _ := http.NewRequestWithContext(ctx, method, u, rd)
		req.Header.Set("Referer", c.referer)
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			return nil, fmt.Errorf("qBittorrent injoignable : %w", err)
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusForbidden && attempt == 0 {
			c.mu.Lock()
			c.loggedIn = false
			c.mu.Unlock()
			if err := c.ensure(ctx); err != nil {
				return nil, err
			}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return data, &HTTPError{Path: path, Status: resp.StatusCode}
		}
		return data, nil
	}
	return nil, &HTTPError{Path: path, Status: http.StatusForbidden}
}

// Raw fait un appel authentifié quelconque à l'API (GET avec q en requête,
// POST avec q en formulaire).
func (c *Client) Raw(ctx context.Context, method, path string, q url.Values) ([]byte, error) {
	return c.call(ctx, method, path, q, nil, "")
}

// Version renvoie la version de qBittorrent (ex. « v5.0.2 »).
func (c *Client) Version(ctx context.Context) (string, error) {
	b, err := c.call(ctx, http.MethodGet, "/api/v2/app/version", nil, nil, "")
	return strings.TrimSpace(string(b)), err
}

// List liste les torrents (filtre qBittorrent : all, completed…), ou ceux des
// hashes donnés.
func (c *Client) List(ctx context.Context, filter string, hashes []string) ([]Torrent, error) {
	q := url.Values{}
	if filter != "" {
		q.Set("filter", filter)
	}
	if len(hashes) > 0 {
		q.Set("hashes", strings.Join(hashes, "|"))
	}
	b, err := c.call(ctx, http.MethodGet, "/api/v2/torrents/info", q, nil, "")
	if err != nil {
		return nil, err
	}
	var list []Torrent
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("liste de qBittorrent illisible : %w", err)
	}
	return list, nil
}

// ErrNoExport : qBittorrent trop ancien pour rendre le .torrent d'origine.
var ErrNoExport = errors.New("ce qBittorrent ne sait pas exporter les .torrent : il faut la version 4.5 ou plus")

// Export renvoie le .torrent d'origine d'un torrent.
func (c *Client) Export(ctx context.Context, hash string) ([]byte, error) {
	b, err := c.call(ctx, http.MethodGet, "/api/v2/torrents/export", url.Values{"hash": {hash}}, nil, "")
	var he *HTTPError
	if errors.As(err, &he) && he.Status == http.StatusNotFound {
		// 404 = route absente (avant 4.5) ou torrent inconnu : on tranche par la version.
		if v, verr := c.call(ctx, http.MethodGet, "/api/v2/app/webapiVersion", nil, nil, ""); verr == nil && !apiAtLeast(strings.TrimSpace(string(v)), 2, 8) {
			return nil, ErrNoExport
		}
	}
	return b, err
}

// AddOptions règle l'ajout d'un .torrent.
type AddOptions struct {
	SavePath string
	Category string
	Tags     string
	Stopped  bool
	// NoSubfolder : sans le dossier racine du .torrent (disposition « Ne pas
	// créer de sous-dossier ») ; sinon disposition d'origine.
	NoSubfolder bool
}

// ErrAlreadyThere : qBittorrent a refusé l'ajout, ce torrent y est déjà.
var ErrAlreadyThere = errors.New("qBittorrent a refusé le .torrent : il est déjà dans le client (même info-hash)")

// Add ajoute un .torrent sur des données existantes.
//
// Tout ce qui dépend des réglages par défaut du membre est imposé, sinon
// qBittorrent cherche les fichiers ailleurs ou arrête le torrent :
// disposition d'origine (pas de sous-dossier créé), gestion automatique
// désactivée (sinon savepath est ignoré au profit de la catégorie), aucune
// condition d'arrêt. Les versions anciennes ignorent les champs qu'elles ne
// connaissent pas.
func (c *Client) Add(ctx context.Context, name string, data []byte, o AddOptions) error {
	layout, rootFolder := "Original", "true"
	if o.NoSubfolder {
		layout, rootFolder = "NoSubfolder", "false"
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, _ := w.CreateFormFile("torrents", name)
	fw.Write(data)
	fields := [][2]string{
		{"savepath", o.SavePath},
		{"category", o.Category},
		{"tags", o.Tags},
		{"autoTMM", "false"},
		// Jamais le dossier des torrents incomplets : les données sont déjà là.
		{"useDownloadPath", "false"},
		{"contentLayout", layout},
		{"root_folder", rootFolder}, // équivalent avant 4.3.2
		{"stopCondition", "None"},
		{"skip_checking", "false"},
		{"paused", fmt.Sprint(o.Stopped)},
		{"stopped", fmt.Sprint(o.Stopped)},
	}
	for _, f := range fields {
		if f[1] != "" {
			w.WriteField(f[0], f[1])
		}
	}
	w.Close()
	b, err := c.call(ctx, http.MethodPost, "/api/v2/torrents/add", nil, &buf, w.FormDataContentType())
	if err != nil {
		var he *HTTPError
		if errors.As(err, &he) && he.Status == http.StatusConflict {
			return ErrAlreadyThere
		}
		return err
	}
	if strings.HasPrefix(strings.TrimSpace(string(b)), "Fails") {
		return ErrAlreadyThere
	}
	return nil
}

func (c *Client) post(ctx context.Context, path string, form url.Values) error {
	_, err := c.call(ctx, http.MethodPost, path, form, nil, "")
	return err
}

// Recheck lance une vérification complète des données.
func (c *Client) Recheck(ctx context.Context, hashes ...string) error {
	return c.post(ctx, "/api/v2/torrents/recheck", url.Values{"hashes": {strings.Join(hashes, "|")}})
}

// Start démarre des torrents (start en 5.x, resume avant).
func (c *Client) Start(ctx context.Context, hashes ...string) error {
	path := "/api/v2/torrents/resume"
	if c.v5 {
		path = "/api/v2/torrents/start"
	}
	return c.post(ctx, path, url.Values{"hashes": {strings.Join(hashes, "|")}})
}

// SetAutoManagement active ou coupe la gestion automatique de torrents.
func (c *Client) SetAutoManagement(ctx context.Context, enable bool, hashes ...string) error {
	return c.post(ctx, "/api/v2/torrents/setAutoManagement", url.Values{
		"hashes": {strings.Join(hashes, "|")},
		"enable": {strconv.FormatBool(enable)},
	})
}

// CategoryPath : le dossier où qBittorrent range, en gestion automatique, un
// torrent de la catégorie name : celui de la catégorie, sinon le dossier par
// défaut suivi du nom de la catégorie. Un dossier relatif part du dossier par
// défaut. "" si la catégorie n'existe pas.
func (c *Client) CategoryPath(ctx context.Context, name string) (string, error) {
	b, err := c.call(ctx, http.MethodGet, "/api/v2/torrents/categories", nil, nil, "")
	if err != nil {
		return "", err
	}
	var cats map[string]struct {
		SavePath string `json:"savePath"`
	}
	if err := json.Unmarshal(b, &cats); err != nil {
		return "", fmt.Errorf("catégories de qBittorrent illisibles : %w", err)
	}
	cat, ok := cats[name]
	if !ok {
		return "", nil
	}
	dir := strings.TrimSpace(cat.SavePath)
	if absClientPath(dir) {
		return dir, nil
	}
	b, err = c.call(ctx, http.MethodGet, "/api/v2/app/preferences", nil, nil, "")
	if err != nil {
		return "", err
	}
	var pref struct {
		SavePath string `json:"save_path"`
	}
	if err := json.Unmarshal(b, &pref); err != nil || strings.TrimSpace(pref.SavePath) == "" {
		return "", errors.New("dossier par défaut de qBittorrent illisible")
	}
	if dir == "" {
		dir = name
	}
	return joinClientPath(pref.SavePath, dir), nil
}

// absClientPath : chemin complet, à la manière de Linux ou de Windows.
func absClientPath(p string) bool {
	return strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\\`) || (len(p) >= 2 && p[1] == ':')
}

// joinClientPath ajoute rel à base, avec le séparateur de base.
func joinClientPath(base, rel string) string {
	sep := "/"
	if strings.Contains(base, `\`) || (len(base) >= 2 && base[1] == ':') {
		sep = `\`
		rel = strings.ReplaceAll(rel, "/", `\`)
	}
	return strings.TrimRight(base, `/\`) + sep + strings.Trim(rel, `/\`)
}

// Stop arrête des torrents (stop en 5.x, pause avant).
func (c *Client) Stop(ctx context.Context, hashes ...string) error {
	path := "/api/v2/torrents/pause"
	if c.v5 {
		path = "/api/v2/torrents/stop"
	}
	return c.post(ctx, path, url.Values{"hashes": {strings.Join(hashes, "|")}})
}

// Downloading dit si le torrent cherche à recevoir des données.
func (t Torrent) Downloading() bool {
	switch t.State {
	case "downloading", "stalledDL", "queuedDL", "forcedDL", "metaDL", "forcedMetaDL":
		return true
	}
	return false
}

// Stopped : arrêté par le membre ou par qBittorrent (« paused » avant la v5).
func (t Torrent) Stopped() bool {
	switch t.State {
	case "pausedUP", "pausedDL", "stoppedUP", "stoppedDL":
		return true
	}
	return false
}

// RemoveKeepFiles retire des torrents du client SANS toucher aux fichiers.
func (c *Client) RemoveKeepFiles(ctx context.Context, hashes ...string) error {
	return c.post(ctx, "/api/v2/torrents/delete", url.Values{"hashes": {strings.Join(hashes, "|")}, "deleteFiles": {"false"}})
}
