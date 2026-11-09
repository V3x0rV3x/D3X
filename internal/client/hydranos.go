package client

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
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/V3x0rV3x/D3X/internal/torrent"
)

// Hydranos, par son API native (« /api/… », pas le shim qBittorrent). La clé
// API se met dans le champ « mot de passe » du client (comme Deluge, qui n'a
// que le sien) et part dans l'en-tête X-Api-Key ; l'utilisateur est ignoré.
//
// Un torrent peut être tenu par plusieurs moteurs d'Hydranos (race, hoard,
// une copie chacun). D3X en choisit une, toujours la même : celle qu'il a
// ajoutée, sinon la première que donne /api/engines. Les actions (arrêt,
// vérification, retrait) ne touchent que cette copie (?engine=), jamais les
// autres : le retrait sans ?engine= les emporterait toutes.
//
// Le renommage et la lecture du disque demandent Hydranos 4.6.
type hydranos struct {
	base string
	key  string
	hc   *http.Client

	mu    sync.Mutex
	ver   string            // version lue, gardée une fois connue
	added map[string]string // info-hash → moteur où D3X l'a ajouté
}

// hydranosRename : la première version d'Hydranos qui renomme les fichiers
// d'un torrent et liste un dossier de son disque.
var hydranosRename = [3]int{4, 6, 0}

// hydranosProof : la première version d'Hydranos qui dit « en vérification »
// pendant qu'elle prouve un ajout. Avant, la ligne se lit « arrêté », et la
// vérification de D3X part sur un dossier encore vide.
var hydranosProof = [3]int{4, 8, 1}

// hydranosPage : lignes demandées par page quand on liste tout un moteur
// (le plafond de l'API est de 5000).
const hydranosPage = 5000

func newHydranos(o Options) (Client, error) {
	u, err := normURL(o.URL)
	if err != nil {
		return nil, err
	}
	u.RawQuery, u.Fragment = "", ""
	u.Path = strings.TrimRight(u.Path, "/")
	u.Path = strings.TrimSuffix(u.Path, "/api")
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if o.Insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // choix du membre
	}
	return &hydranos{
		base: u.String(), key: strings.TrimSpace(o.Pass),
		hc:    &http.Client{Timeout: 2 * time.Minute, Transport: tr},
		added: map[string]string{},
	}, nil
}

func (h *hydranos) Kind() string { return "Hydranos" }

// do envoie une requête et rend le code et le corps. 401 : la clé est refusée.
func (h *hydranos) do(ctx context.Context, method, path string, q url.Values, ctype string, body io.Reader) (int, []byte, error) {
	u := h.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return 0, nil, err
	}
	if h.key != "" {
		req.Header.Set("X-Api-Key", h.key)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := h.hc.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("Hydranos injoignable : %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return resp.StatusCode, data, ErrAuth
	}
	return resp.StatusCode, data, nil
}

// hydranosError : le message {"error": "…"} d'une réponse, vide s'il n'y en a pas.
// ok dit si le corps en est un : c'est ce qui distingue une route qu'Hydranos
// ne connaît pas (404 sans corps JSON) d'un torrent ou d'un chemin absent (404
// avec {"error"}).
func hydranosError(data []byte) (msg string, ok bool) {
	var e struct {
		Error *string `json:"error"`
	}
	if json.Unmarshal(data, &e) != nil || e.Error == nil {
		return "", false
	}
	return *e.Error, true
}

func httpFailure(code int, data []byte) error {
	if msg, ok := hydranosError(data); ok && msg != "" {
		return fmt.Errorf("Hydranos : %s (HTTP %d)", msg, code)
	}
	return fmt.Errorf("Hydranos : HTTP %d", code)
}

// get lit une réponse JSON ; un code hors 2xx est une erreur.
func (h *hydranos) get(ctx context.Context, path string, q url.Values, out interface{}) error {
	code, data, err := h.do(ctx, http.MethodGet, path, q, "", nil)
	if err != nil {
		return err
	}
	if code/100 != 2 {
		return httpFailure(code, data)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return errors.New("ce n'est pas Hydranos qui répond à cette adresse")
	}
	return nil
}

func (h *hydranos) Version(ctx context.Context) (string, error) {
	var st struct {
		Version string `json:"version"`
	}
	if err := h.get(ctx, "/api/status", nil, &st); err != nil {
		return "", err
	}
	if st.Version == "" {
		return "", errors.New("ce n'est pas Hydranos qui répond à cette adresse")
	}
	h.mu.Lock()
	h.ver = st.Version
	h.mu.Unlock()
	return st.Version, nil
}

// parseVersion lit « 4.6.0 », « v4.6.1-rc1 » ou « 4.6 » en trois nombres.
func parseVersion(s string) ([3]int, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	var out [3]int
	parts := strings.SplitN(s, ".", 3)
	if len(parts) < 2 {
		return out, false
	}
	for i, p := range parts {
		end := 0
		for end < len(p) && p[end] >= '0' && p[end] <= '9' {
			end++
		}
		n, err := strconv.Atoi(p[:end])
		if err != nil {
			return out, false
		}
		out[i] = n
		if end < len(p) {
			break // « 0-rc1 » : le reste n'est pas un nombre
		}
	}
	return out, true
}

func versionAtLeast(have, want [3]int) bool {
	for i := range want {
		if have[i] != want[i] {
			return have[i] > want[i]
		}
	}
	return true
}

// supportsRename : Hydranos 4.6 ou plus. La version est lue une fois et
// gardée ; si Hydranos ne répond pas, on dit non sans rien garder, le
// prochain appel réessaie.
func (h *hydranos) supportsRename() bool {
	h.mu.Lock()
	v := h.ver
	h.mu.Unlock()
	if v == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var err error
		if v, err = h.Version(ctx); err != nil {
			return false
		}
	}
	n, ok := parseVersion(v)
	return ok && versionAtLeast(n, hydranosRename)
}

func (h *hydranos) CanRename() bool { return h.supportsRename() }

// ---------- trouver un torrent ----------

// hRow : une ligne d'Hydranos (liste ou détail d'un torrent).
type hRow struct {
	Hash       string  `json:"info_hash"`
	Name       string  `json:"name"`
	State      string  `json:"state"`
	Progress   float64 `json:"progress"`
	TotalSize  int64   `json:"total_size"`
	SavePath   string  `json:"save_path"`
	EngineSave string  `json:"engine_save_path"`
	Tracker    string  `json:"tracker_host"`
	UserPaused bool    `json:"user_paused"`
	MultiFile  bool    `json:"multi_file"`
	Agent      string  `json:"agent"`
	Category   string  `json:"category"`
	// Tags : null pour un torrent sans étiquette.
	Tags []string `json:"tags"`
	// ContentPath : le dossier commun des fichiers (celui du renommage), quand
	// Hydranos le donne.
	ContentPath string `json:"content_path"`
	// ContentFolder : le contenu est rangé dans un dossier à son nom (true) ou
	// posé directement dans le dossier de sauvegarde (false). null tant
	// qu'Hydranos ne le sait pas (torrent d'avant le réglage) : on suppose le
	// dossier, comme lui.
	ContentFolder *bool `json:"content_folder"`
}

func (r hRow) torrent() Torrent {
	hash := strings.ToLower(r.Hash)
	dir := r.EngineSave
	if dir == "" {
		dir = r.SavePath
	}
	stopped := r.UserPaused || r.State == "stopped"
	content := r.ContentPath
	switch {
	case content != "":
	case r.MultiFile && r.ContentFolder != nil && !*r.ContentFolder:
		// Plusieurs fichiers sans dossier racine : ils sont dans dir même,
		// pas sous le nom du torrent. (Pour un fichier seul, le contenu est
		// le fichier, dir/nom, avec ou sans dossier autour.)
		content = dir
	default:
		content = joinPath(dir, r.Name)
	}
	t := Torrent{
		Hash: hash, Hashes: []string{hash}, Name: r.Name, ContentPath: content,
		Size: r.TotalSize, TotalSize: r.TotalSize, Progress: r.Progress,
		Checking:    r.State == "checking_files" || r.State == "checking",
		Downloading: !stopped && r.Progress < 1 && (r.State == "downloading" || r.State == "queued"),
		Stopped:     stopped,
		Category:    r.Category,
		Tags:        tagList(r.Tags),
		SavePath:    dir,
	}
	if r.Tracker != "" {
		t.Trackers = []string{strings.ToLower(r.Tracker)}
	}
	return t
}

type hEngine struct {
	ID   string `json:"id"`
	Role string `json:"role"`
}

func (h *hydranos) engines(ctx context.Context) ([]hEngine, error) {
	var all []hEngine
	if err := h.get(ctx, "/api/engines", nil, &all); err != nil {
		return nil, err
	}
	out := all[:0]
	for _, e := range all {
		if e.Role == "race" || e.Role == "hoard" {
			out = append(out, e)
		}
	}
	return out, nil
}

// hCopy : la copie d'un torrent dans un moteur.
type hCopy struct {
	engine string
	role   string // race ou hoard
	row    hRow
}

// copies rend les copies d'un torrent, une par moteur qui le tient. Une
// requête ciblée par moteur (le détail d'un torrent), jamais la liste : une
// instance peut tenir plus d'un million de torrents.
func (h *hydranos) copies(ctx context.Context, hash string) ([]hCopy, error) {
	engines, err := h.engines(ctx)
	if err != nil {
		return nil, err
	}
	return h.copiesIn(ctx, engines, hash)
}

// copiesIn : comme copies, avec la liste des moteurs déjà lue (un lot de
// hashes ne la relit pas pour chacun).
func (h *hydranos) copiesIn(ctx context.Context, engines []hEngine, hash string) ([]hCopy, error) {
	hash = strings.ToLower(hash)
	var out []hCopy
	for _, e := range engines {
		code, data, err := h.do(ctx, http.MethodGet, "/api/"+e.Role+"/torrents/"+url.PathEscape(hash), url.Values{"engine": {e.ID}}, "", nil)
		if err != nil {
			return nil, err
		}
		if code == http.StatusNotFound {
			continue // pas dans ce moteur
		}
		if code/100 != 2 {
			return nil, httpFailure(code, data)
		}
		var row hRow
		if json.Unmarshal(data, &row) != nil {
			return nil, errors.New("ce n'est pas Hydranos qui répond à cette adresse")
		}
		row.Hash = hash
		out = append(out, hCopy{engine: e.ID, role: e.Role, row: row})
	}
	return out, nil
}

// pick choisit la copie dont D3X s'occupe : celle qu'il a ajoutée ; sinon
// (D3X redémarré, la mémoire des ajouts est perdue) la première du moteur
// hoard, où D3X ajoute toujours (mode=hoard) ; sinon la première. Sans ce
// repli, un arrêt, une vérification ou un retrait après un redémarrage
// pouvait viser la copie race du membre au lieu de celle de D3X.
func (h *hydranos) pick(hash string, cs []hCopy) (hCopy, bool) {
	if len(cs) == 0 {
		return hCopy{}, false
	}
	h.mu.Lock()
	want := h.added[strings.ToLower(hash)]
	h.mu.Unlock()
	for _, c := range cs {
		if want != "" && c.engine == want {
			return c, true
		}
	}
	for _, c := range cs {
		if c.role == "hoard" {
			return c, true
		}
	}
	return cs[0], true
}

var errNoTorrent = errors.New("torrent introuvable dans Hydranos")

func (h *hydranos) find(ctx context.Context, hash string) (hCopy, error) {
	engines, err := h.engines(ctx)
	if err != nil {
		return hCopy{}, err
	}
	return h.findIn(ctx, engines, hash)
}

func (h *hydranos) findIn(ctx context.Context, engines []hEngine, hash string) (hCopy, error) {
	cs, err := h.copiesIn(ctx, engines, hash)
	if err != nil {
		return hCopy{}, err
	}
	c, ok := h.pick(hash, cs)
	if !ok {
		return hCopy{}, errNoTorrent
	}
	return c, nil
}

func (h *hydranos) List(ctx context.Context, hashes []string) ([]Torrent, error) {
	if len(hashes) > 0 {
		// Les moteurs une seule fois pour tout le lot.
		engines, err := h.engines(ctx)
		if err != nil {
			return nil, err
		}
		var out []Torrent
		for _, hash := range hashes {
			c, err := h.findIn(ctx, engines, hash)
			if errors.Is(err, errNoTorrent) {
				continue
			}
			if err != nil {
				return nil, err
			}
			out = append(out, c.row.torrent())
		}
		return out, nil
	}
	// Tout le client : page par page, un moteur par rôle (la page d'un moteur
	// reprend les autres moteurs du même rôle, chaque ligne dit le sien).
	engines, err := h.engines(ctx)
	if err != nil {
		return nil, err
	}
	var out []Torrent
	seen := map[string]bool{}
	roles := map[string]bool{}
	for _, e := range engines {
		if roles[e.Role] {
			continue
		}
		roles[e.Role] = true
		for offset := 0; ; {
			var page struct {
				Filtered int    `json:"filtered"`
				Rows     []hRow `json:"rows"`
			}
			q := url.Values{"offset": {strconv.Itoa(offset)}, "limit": {strconv.Itoa(hydranosPage)}, "sort": {"added_time"}, "order": {"asc"}}
			if err := h.get(ctx, "/api/engines/"+url.PathEscape(e.ID)+"/page", q, &page); err != nil {
				return nil, err
			}
			for _, r := range page.Rows {
				// Les lignes d'un autre Hydranos déclaré comme nœud ne sont pas d'ici.
				if r.Agent != "" && !strings.HasPrefix(r.Agent, "local-") {
					continue
				}
				if k := strings.ToLower(r.Hash); !seen[k] {
					seen[k] = true
					out = append(out, r.torrent())
				}
			}
			offset += len(page.Rows)
			if len(page.Rows) == 0 || offset >= page.Filtered {
				break
			}
		}
	}
	return out, nil
}

// ---------- .torrent et fichiers ----------

func (h *hydranos) Export(ctx context.Context, hash string) ([]byte, error) {
	code, data, err := h.do(ctx, http.MethodGet, "/api/torrents/"+url.PathEscape(strings.ToLower(hash))+"/torrent", nil, "", nil)
	if err != nil {
		return nil, err
	}
	if code == http.StatusNotFound {
		return nil, errNoTorrent
	}
	if code/100 != 2 {
		return nil, httpFailure(code, data)
	}
	return data, nil
}

// hFile : un fichier de /files. Depuis Hydranos 4.6, Folder est le premier
// composant du chemin réel (null pour un fichier seul) et Path le reste ;
// avant, le champ n'existe pas et Path est celui du .torrent, sans dossier.
type hFile struct {
	Index  int             `json:"index"`
	Folder json.RawMessage `json:"folder"`
	Path   string          `json:"path"`
	Size   int64           `json:"size"`
}

// full rend le chemin complet que D3X attend (avec le dossier racine).
func (f hFile) full(c hCopy, rooted bool) string {
	p := strings.ReplaceAll(f.Path, `\`, "/")
	if len(f.Folder) > 0 { // 4.6 ou plus
		var folder *string
		if json.Unmarshal(f.Folder, &folder) == nil && folder != nil {
			return *folder + "/" + p
		}
		return p
	}
	if c.row.MultiFile && !rooted {
		return c.row.Name + "/" + p
	}
	return p
}

// rawFiles rend les fichiers tels que /files les donne.
func (h *hydranos) rawFiles(ctx context.Context, c hCopy) ([]hFile, error) {
	var res struct {
		Files []hFile `json:"files"`
	}
	code, data, err := h.do(ctx, http.MethodGet, "/api/torrents/"+url.PathEscape(c.row.Hash)+"/files", url.Values{"engine": {c.engine}}, "", nil)
	if err != nil {
		return nil, err
	}
	if code == http.StatusNotFound {
		return nil, errNoTorrent
	}
	if code/100 != 2 {
		return nil, httpFailure(code, data)
	}
	if json.Unmarshal(data, &res) != nil {
		return nil, errors.New("ce n'est pas Hydranos qui répond à cette adresse")
	}
	return res.Files, nil
}

func (h *hydranos) Files(ctx context.Context, hash string) ([]torrent.File, int64, error) {
	c, err := h.find(ctx, hash)
	if err != nil {
		return nil, 0, err
	}
	files, err := h.rawFiles(ctx, c)
	if err != nil {
		return nil, 0, err
	}
	// Avant la 4.6 (pas de folder), un chemin peut déjà porter le dossier.
	rooted := c.row.MultiFile && c.row.Name != "" && len(files) > 0
	for _, f := range files {
		if len(f.Folder) > 0 || !strings.HasPrefix(f.Path, c.row.Name+"/") {
			rooted = false
		}
	}
	out := make([]torrent.File, 0, len(files))
	var total int64
	for _, f := range files {
		out = append(out, torrent.File{Path: f.full(c, rooted), Size: f.Size, Index: f.Index})
		total += f.Size
	}
	return out, total, nil
}

// ---------- ajout et actions ----------

func (h *hydranos) Add(ctx context.Context, name string, raw []byte, o AddOptions) error {
	v, err := h.Version(ctx)
	if err != nil {
		return err
	}
	if n, ok := parseVersion(v); !ok || !versionAtLeast(n, hydranosProof) {
		return fmt.Errorf("Hydranos 4.8.1 ou plus requis pour ajouter un torrent (version %s) : mets Hydranos à jour", v)
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if name == "" {
		name = "d3x.torrent"
	}
	fw, err := w.CreateFormFile("torrents", name)
	if err != nil {
		return err
	}
	fw.Write(raw)
	// Toujours à l'arrêt, jamais sans vérification (pas de skip_checking), et
	// sans dossier en plus pour un fichier seul : la release est dans savepath.
	// mode=hoard : sans moteur ni mode, Hydranos place l'ajout selon la
	// catégorie, et une catégorie inconnue tombe sur le moteur race, fait pour
	// la course et pas pour seeder longtemps. Un cross-seed est un seed long.
	// Si aucun moteur n'a le rôle hoard, Hydranos refuse (400) avec un message
	// que l'ajout remonte tel quel.
	fields := [][2]string{{"paused", "true"}, {"create_subfolder", "false"}, {"mode", "hoard"}}
	if o.SavePath != "" {
		fields = append(fields, [2]string{"savepath", o.SavePath})
	}
	if o.Category != "" {
		fields = append(fields, [2]string{"category", o.Category})
	}
	if o.Tag != "" {
		fields = append(fields, [2]string{"tags", o.Tag})
	}
	for _, f := range fields {
		w.WriteField(f[0], f[1])
	}
	if err := w.Close(); err != nil {
		return err
	}
	code, data, err := h.do(ctx, http.MethodPost, "/api/torrents/upload", nil, w.FormDataContentType(), &buf)
	if err != nil {
		return err
	}
	switch {
	case code == http.StatusConflict:
		return ErrAlreadyThere
	case code/100 != 2:
		return httpFailure(code, data)
	}
	var res struct {
		Hash   string `json:"info_hash"`
		Engine string `json:"engine"`
	}
	if json.Unmarshal(data, &res) == nil && res.Hash != "" && res.Engine != "" {
		h.mu.Lock()
		h.added[strings.ToLower(res.Hash)] = res.Engine
		h.mu.Unlock()
	}
	return nil
}

func (h *hydranos) Recheck(ctx context.Context, hash string) error {
	c, err := h.find(ctx, hash)
	if err != nil {
		return err
	}
	code, data, err := h.do(ctx, http.MethodPost, "/api/hoard/torrents/"+url.PathEscape(c.row.Hash)+"/verify", url.Values{"engine": {c.engine}}, "", nil)
	if err != nil {
		return err
	}
	if code/100 != 2 {
		return httpFailure(code, data)
	}
	return nil
}

func (h *hydranos) pause(ctx context.Context, hash string, paused bool) error {
	c, err := h.find(ctx, hash)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]interface{}{"hashes": []string{c.row.Hash}, "paused": paused})
	code, data, err := h.do(ctx, http.MethodPost, "/api/engines/"+url.PathEscape(c.engine)+"/pause", nil, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	if code/100 != 2 {
		return httpFailure(code, data)
	}
	var res struct {
		Applied *int `json:"applied"`
	}
	if json.Unmarshal(data, &res) == nil && res.Applied != nil && *res.Applied == 0 {
		return errors.New("Hydranos n'a pas pu changer l'état du torrent")
	}
	return nil
}

func (h *hydranos) Start(ctx context.Context, hash string) error { return h.pause(ctx, hash, false) }
func (h *hydranos) Stop(ctx context.Context, hash string) error  { return h.pause(ctx, hash, true) }

// RemoveKeepFiles retire la copie de D3X, jamais les fichiers : delete_files
// n'est pas envoyé (absent, Hydranos les garde). Un torrent qui n'y est plus
// est déjà retiré.
func (h *hydranos) RemoveKeepFiles(ctx context.Context, hash string) error {
	c, err := h.find(ctx, hash)
	if errors.Is(err, errNoTorrent) {
		return nil
	}
	if err != nil {
		return err
	}
	code, data, err := h.do(ctx, http.MethodDelete, "/api/torrents/"+url.PathEscape(c.row.Hash), url.Values{"engine": {c.engine}}, "", nil)
	if err != nil {
		return err
	}
	if code == http.StatusNotFound {
		return nil
	}
	if code/100 != 2 {
		return httpFailure(code, data)
	}
	h.mu.Lock()
	delete(h.added, c.row.Hash)
	h.mu.Unlock()
	return nil
}

// ---------- renommage et disque (Hydranos 4.6) ----------

var errNeedsRename = errors.New("Hydranos 4.6 ou plus requis pour renommer les fichiers d'un torrent")

// RenameFiles renomme les fichiers d'un torrent arrêté, un par un, sans rien
// bouger sur le disque (voir rename.go). Pour un torrent de plusieurs
// fichiers, Hydranos veut le dossier à part du chemin : from_folder et from
// (le chemin sous le dossier), folder et to. Le dossier peut changer d'un
// fichier à l'autre. Pour un fichier seul : from et to, chemins entiers, et
// pas de dossier.
func (h *hydranos) RenameFiles(ctx context.Context, hash string, renames []Rename) error {
	c, err := h.find(ctx, hash)
	if err != nil {
		return err
	}
	for _, r := range renames {
		if r.From == r.To {
			continue
		}
		req := map[string]string{"from": r.From, "to": r.To}
		if c.row.MultiFile {
			fromFolder, from, ok1 := strings.Cut(r.From, "/")
			folder, to, ok2 := strings.Cut(r.To, "/")
			if !ok1 || !ok2 {
				return errors.New("renommage impossible (" + r.From + ") : un torrent de plusieurs fichiers garde ses fichiers dans un dossier")
			}
			req = map[string]string{"from": from, "from_folder": fromFolder, "to": to, "folder": folder}
		}
		body, _ := json.Marshal(req)
		code, data, err := h.do(ctx, http.MethodPost, "/api/torrents/"+url.PathEscape(c.row.Hash)+"/files/rename", url.Values{"engine": {c.engine}}, "application/json", bytes.NewReader(body))
		if err != nil {
			return err
		}
		if code/100 == 2 {
			continue
		}
		msg, known := hydranosError(data)
		switch {
		case code == http.StatusNotFound && !known:
			return errNeedsRename
		case code == http.StatusNotFound:
			return errNoTorrent
		case msg == "":
			msg = "HTTP " + strconv.Itoa(code)
		}
		return errors.New("renommage refusé par Hydranos (" + r.From + ") : " + msg)
	}
	return nil
}

// Lookup : Hydranos 4.6 liste un dossier de son disque (/api/fs/list, limité
// à ses dossiers de téléchargement). On liste le dossier du chemin cherché et
// on y cherche son nom ; un dossier qui n'existe pas dit que le chemin non
// plus. Une liste tronquée où le nom ne figure pas : on ne sait pas non plus. Une route absente (Hydranos plus ancien), un dossier hors des
// racines (403) ou refusé (400) : on ne sait pas.
func (h *hydranos) Lookup(ctx context.Context, p string) (bool, bool, error) {
	clean := strings.TrimRight(p, `/\`)
	parent, name := splitClientPath(clean)
	if parent == "" || parent == clean || name == "" {
		return false, false, nil
	}
	code, data, err := h.do(ctx, http.MethodGet, "/api/fs/list", url.Values{"path": {parent}}, "", nil)
	if err != nil {
		return false, false, err
	}
	switch {
	case code/100 == 2:
		var res struct {
			Truncated bool `json:"truncated"`
			Entries   []struct {
				Name string `json:"name"`
			} `json:"entries"`
		}
		if json.Unmarshal(data, &res) != nil {
			return false, false, nil
		}
		for _, e := range res.Entries {
			if e.Name == name {
				return true, true, nil
			}
		}
		// Liste coupée à la limite d'entrées d'Hydranos : le nom a pu rester
		// de l'autre côté, on ne sait pas.
		if res.Truncated {
			return false, false, nil
		}
		return false, true, nil
	case code == http.StatusNotFound:
		if _, known := hydranosError(data); known {
			return false, true, nil // le dossier n'existe pas : le chemin non plus
		}
		return false, false, nil // route absente
	}
	return false, false, nil
}
