// Package server sert la page locale de D3X (http://127.0.0.1 seulement).
//
// Protections, parce qu'une page web quelconque ouverte dans le même
// navigateur peut tenter d'appeler un serveur local :
//   - écoute sur 127.0.0.1 uniquement ;
//   - en-tête Host vérifié (127.0.0.1, localhost ou [::1] au bon port) :
//     bloque le rebond DNS, où un site pointe son propre domaine vers 127.0.0.1 ;
//   - toute route /ui/ exige le cookie de session, tiré au hasard à chaque
//     lancement (HttpOnly, SameSite=Strict) ;
//   - les requêtes POST exigent du JSON et une origine identique.
package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/V3x0rV3x/D3X/internal/batch"
	"github.com/V3x0rV3x/D3X/internal/client"
	"github.com/V3x0rV3x/D3X/internal/config"
	"github.com/V3x0rV3x/D3X/internal/crossseed"
	"github.com/V3x0rV3x/D3X/internal/desktop"
	"github.com/V3x0rV3x/D3X/internal/journal"
	"github.com/V3x0rV3x/D3X/internal/mediainfo"
	"github.com/V3x0rV3x/D3X/internal/musicbrainz"
	"github.com/V3x0rV3x/D3X/internal/pathmap"
	"github.com/V3x0rV3x/D3X/internal/publish"
	"github.com/V3x0rV3x/D3X/internal/remote"
	"github.com/V3x0rV3x/D3X/internal/v3x"
)

//go:embed ui
var uiFS embed.FS

const cookieName = "d3x_session"

// Server est la page locale et son état.
type Server struct {
	Version string
	Token   string
	Port    int
	opts    Options

	mu     sync.Mutex
	cfg    *config.Config
	conns  map[string]*clientConn // clients torrent, par identifiant
	v      *v3x.Client
	me     *v3x.Me
	v3xErr string
	// v3xRetry : la dernière connexion à V3X a échoué sur le réseau ou sur une
	// panne du site, et non sur un refus de la clé : elle est retentée seule.
	v3xRetry bool
	update   *Update
	engine   *crossseed.Engine
	pub      *publish.Manager
	mux      *http.ServeMux

	// Présence de la page : dernière requête de la page, et son « au revoir »
	// (fermeture de la fenêtre). En nanosecondes Unix.
	lastUpdateCheck atomic.Int64 // dernière recherche de version (UnixNano)
	updateStaging   atomic.Bool  // téléchargement et vérification d'une version en cours
	lastSeen        atomic.Int64
	byeAt           atomic.Int64
	pagesMu         sync.Mutex
	pages           map[string]bool // page → affichée à l'écran (ping ?id=&shown=)

	lot    *batch.Engine      // le lot (publication par lot), dans D3X lui-même
	mi     *mediainfo.Chooser // MediaInfo : binaire s'il existe, sinon une page
	miPage *mediainfo.Page

	ssh     *remote.Client // seedbox connectée (nil sinon)
	sshSys  string
	sshErr  string
	sshPend *sshPending

	mb    *musicbrainz.Client // recherche des albums
	seedC seedCache           // état du seed lu sur V3X
	reqC  requestsCache       // demandes ouvertes
	polC  policiesCache       // teams dont le réupload n'est pas libre
	sv    survey              // surveillance du client

	pubKey      ed25519.PublicKey // clé qui signe les versions de D3X
	restartMu   sync.Mutex
	restart     chan struct{}
	restartOnce *sync.Once

	pubCtx   []byte // contexte de publication du membre (gardé 5 min)
	pubCtxAt time.Time
	bbCSS    []byte // feuille de style BBCode du site (gardée 1 h)
	bbCSSAt  time.Time
}

// NewToken tire un jeton de session.
func NewToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// Options : mode serveur (Docker, NAS), où la page est ouverte depuis une
// autre machine.
type Options struct {
	// Remote : page jointe par le réseau (n'importe quel nom d'hôte, derrière
	// un proxy https compris). Le jeton reste exigé, le cookie dure.
	Remote bool
	// Managed : D3X mis à jour par autre chose que lui-même (« docker ») ;
	// il signale les nouvelles versions sans les installer.
	Managed string
}

// New prépare le serveur avec la configuration lue.
func New(version string, cfg *config.Config, token string, port int) *Server {
	return NewWith(version, cfg, token, port, Options{})
}

// NewWith prépare le serveur avec des options.
func NewWith(version string, cfg *config.Config, token string, port int, opts Options) *Server {
	s := &Server{Version: version, Token: token, Port: port, opts: opts, cfg: cfg, mux: http.NewServeMux(), restart: make(chan struct{}), restartOnce: &sync.Once{}}
	s.engine = crossseed.New(s.env)
	s.engine.SetRefresh(s.retryClients)
	s.mb = musicbrainz.New(version)
	s.pub = publish.New(s.env, s.engine, version)
	s.miPage = mediainfo.NewPage()
	s.mi = &mediainfo.Chooser{Exec: mediainfo.Find(), Page: s.miPage}
	if s.mi.Exec != nil {
		log.Println("MediaInfo : binaire", s.mi.Exec.Path)
	} else if p := strings.TrimSpace(os.Getenv("D3X_MEDIAINFO")); p != "" {
		log.Println("MediaInfo : attention, D3X_MEDIAINFO désigne", p, "qui est introuvable ; le lot demandera le MediaInfo à une page")
	}
	s.lot = batch.New(batchDeps{s})
	sub, _ := fs.Sub(uiFS, "ui")
	s.mux.Handle("GET /", http.FileServer(http.FS(sub)))
	s.mux.HandleFunc("GET /auth", s.auth)
	s.mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("D3X")) })
	s.mux.HandleFunc("GET /ui/state", s.state)
	s.mux.HandleFunc("POST /ui/settings", s.settings)
	s.mux.HandleFunc("POST /ui/reconnect", s.reconnect)
	s.mux.HandleFunc("POST /ui/scan", s.scan)
	s.mux.HandleFunc("POST /ui/stop", s.stop)
	s.mux.HandleFunc("GET /ui/status", s.status)
	s.mux.HandleFunc("GET /ui/rows", s.rows)
	s.mux.HandleFunc("GET /ui/rows/diag", s.rowDiag)
	s.mux.HandleFunc("POST /ui/apply", s.apply)
	s.mux.HandleFunc("POST /ui/remove", s.remove)
	s.mux.HandleFunc("POST /ui/recheck", s.recheck)
	s.mux.HandleFunc("GET /ui/history", s.history)
	s.mux.HandleFunc("GET /ui/fs", s.browse)
	s.mux.HandleFunc("GET /ui/client/detect", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]interface{}{"found": client.Detect()})
	})
	s.mux.HandleFunc("POST /ui/client/test", s.clientTest)
	s.routesPublish()
	s.routesBatch()
	s.routesSSH()
	s.routesMusic()
	s.routesSeed()
	s.routesCandidates()
	s.routesLibrary()
	s.routesSeries()
	s.mux.HandleFunc("POST /ui/update/restart", s.restartNow)
	s.mux.HandleFunc("POST /ui/update/check", s.checkNow)
	s.mux.HandleFunc("GET /ui/ping", s.ping)
	s.mux.HandleFunc("POST /ui/bye", func(w http.ResponseWriter, r *http.Request) {
		s.byeAt.Store(time.Now().UnixNano())
		w.WriteHeader(http.StatusNoContent)
	})
	go s.connect()
	go s.v3xRetryLoop()
	go s.connectSSH()
	go s.updateLoop()
	go s.surveyLoop()
	s.engine.SetLinker(s.linkFiles)
	return s
}

func (s *Server) hostAllowed(host string) bool {
	if s.opts.Remote {
		return host != ""
	}
	h, p, err := net.SplitHostPort(host)
	if err != nil || p != strconv.Itoa(s.Port) {
		return false
	}
	return h == "127.0.0.1" || h == "localhost" || h == "::1"
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.hostAllowed(r.Host) {
		http.Error(w, "hôte refusé", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host && o != "https://"+r.Host {
			http.Error(w, "origine refusée", http.StatusForbidden)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			http.Error(w, "JSON attendu", http.StatusUnsupportedMediaType)
			return
		}
	}
	if strings.HasPrefix(r.URL.Path, "/ui/") {
		if !s.sessionOK(r) {
			// remote : mode serveur (Docker, NAS), la page dit où trouver le
			// lien d'accès au lieu de « relance D3X » (sultar, 22/10/26).
			writeJSON(w, http.StatusUnauthorized, map[string]interface{}{"error": "session", "remote": s.opts.Remote})
			return
		}
		// Première venue de la page : notée dans le journal, pour distinguer une
		// fenêtre restée vide d'une page chargée mais pas affichée (Luci, 21/10/26).
		if s.lastSeen.Swap(time.Now().UnixNano()) == 0 {
			log.Println("page de D3X ouverte")
		}
	}
	h := w.Header()
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	// Aucun script venu d'ailleurs. WebAssembly pour MediaInfo ; images en https
	// (affiches TMDB, hébergeurs des présentations) ; styles en ligne pour le
	// rendu BBCode (couleurs, tailles).
	h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: https:; style-src 'self' 'unsafe-inline'; script-src 'self' 'wasm-unsafe-eval'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	// La page change à chaque version : jamais de copie gardée par le navigateur.
	h.Set("Cache-Control", "no-store")
	s.mux.ServeHTTP(w, r)
}

func (s *Server) sessionOK(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	return err == nil && subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.Token)) == 1
}

// auth échange le jeton du lien d'ouverture contre le cookie de session.
func (s *Server) auth(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("t")), []byte(s.Token)) != 1 {
		if s.opts.Remote {
			http.Error(w, "Lien invalide : le bon lien est dans le journal de D3X (docker logs d3x).", http.StatusForbidden)
		} else {
			http.Error(w, "Lien périmé : relance D3X pour l'ouvrir.", http.StatusForbidden)
		}
		return
	}
	c := &http.Cookie{Name: cookieName, Value: s.Token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode}
	if s.opts.Remote {
		// Page ouverte d'une autre machine : le lien n'est pas redonné à chaque
		// fois, le cookie tient 400 jours (https derrière un proxy : Secure).
		c.MaxAge = 400 * 24 * 3600
		c.Secure = r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	}
	http.SetCookie(w, c)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// connect (re)crée les clients et vérifie les connexions : V3X et chaque
// client torrent, tous en même temps.
func (s *Server) connect() {
	s.mu.Lock()
	cfg := *s.cfg
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var (
		v        *v3x.Client
		me       *v3x.Me
		v3xErr   string
		v3xRetry bool
		done     = make(chan struct{})
	)
	go func() {
		defer close(done)
		v, me, v3xErr, v3xRetry = dialV3X(ctx, cfg, s.Version)
	}()
	conns := dialAll(ctx, cfg.Clients)
	<-done
	s.mu.Lock()
	s.v, s.me, s.v3xErr, s.v3xRetry = v, me, v3xErr, v3xRetry
	s.conns = conns
	s.mu.Unlock()
}

// dialV3X vérifie la clé auprès de V3X. retry : l'échec vient du réseau ou
// d'une panne du site (à retenter), pas d'un refus de la clé.
func dialV3X(ctx context.Context, cfg config.Config, version string) (v *v3x.Client, me *v3x.Me, msg string, retry bool) {
	if cfg.V3XKey == "" {
		return nil, nil, "", false
	}
	v = v3x.New(cfg.V3XBase, cfg.V3XKey, "D3X/"+version)
	m, err := v.Me(ctx)
	if err != nil {
		var ae *v3x.APIError
		return v, nil, err.Error(), !errors.As(err, &ae) || ae.Status >= 500
	}
	return v, m, "", false
}

// v3xRetryEvery : délai entre deux nouvelles tentatives de connexion à V3X,
// en nanosecondes. Atomique : un test le raccourcit pendant que les boucles
// d'autres serveurs le lisent.
var v3xRetryEvery atomic.Int64

func init() { v3xRetryEvery.Store(int64(30 * time.Second)) }

// v3xRetryLoop : une connexion à V3X ratée sur le réseau (pas encore prêt au
// démarrage de la machine ou après une mise à jour, site un instant
// injoignable) est retentée seule. Avant, l'erreur restait affichée jusqu'au
// prochain enregistrement des Réglages (MasterMan, 22/10/26).
func (s *Server) v3xRetryLoop() {
	for {
		time.Sleep(time.Duration(v3xRetryEvery.Load()))
		s.mu.Lock()
		cfg, retry := *s.cfg, s.v3xRetry && s.me == nil
		s.mu.Unlock()
		if !retry {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		v, me, msg, again := dialV3X(ctx, cfg, s.Version)
		cancel()
		s.mu.Lock()
		// Réglages changés entre-temps : leur propre connexion fait foi.
		if s.cfg.V3XKey == cfg.V3XKey && s.cfg.V3XBase == cfg.V3XBase && s.me == nil {
			s.v, s.me, s.v3xErr, s.v3xRetry = v, me, msg, again
		}
		s.mu.Unlock()
	}
}

type stateOut struct {
	Version string  `json:"version"`
	Mode    string  `json:"mode,omitempty"` // server (page jointe par le réseau), docker
	Update  *Update `json:"update,omitempty"`
	V3X     struct {
		Configured bool     `json:"configured"`
		OK         bool     `json:"ok"`
		Error      string   `json:"error,omitempty"`
		Username   string   `json:"username,omitempty"`
		Ratio      *float64 `json:"ratio,omitempty"`
		HitAndRun  int      `json:"hitAndRun"`
	} `json:"v3x"`
	// Clients : les clients torrent, dans l'ordre des réglages, chacun avec
	// son état et ses dossiers (où D3X les voit : « Z:\data » ici, « /data »
	// dans le client).
	Clients      []clientOut `json:"clients"`
	Category     string      `json:"category"`
	PubCategory  string      `json:"pubCategory"`
	Discogs      bool        `json:"discogs"` // jeton Discogs enregistré
	Tag          string      `json:"tag"`
	CheckUpdates bool        `json:"checkUpdates"`
	LastDir      string      `json:"lastDir,omitempty"`
	Watch        surveyOut   `json:"watch"`
	Links        struct {
		On       bool `json:"on"`
		Symbolic bool `json:"symbolic"`
	} `json:"links"`
	SSH struct {
		Configured bool   `json:"configured"`
		OK         bool   `json:"ok"`
		Label      string `json:"label,omitempty"`
		System     string `json:"system,omitempty"`
		Error      string `json:"error,omitempty"`
		Host       string `json:"host,omitempty"`
		Port       int    `json:"port,omitempty"`
		User       string `json:"user,omitempty"`
		KeyPath    string `json:"keyPath,omitempty"`
		// Seedboxes déjà connectées, l'actuelle en tête.
		Saved []sshSavedOut `json:"saved,omitempty"`
	} `json:"ssh"`
	Appearance struct {
		Theme         string `json:"theme"`   // appliqué
		Accent        string `json:"accent"`  // appliqué
		UITheme       string `json:"uiTheme"` // choisi dans D3X ("" = comme sur V3X)
		UIAccnt       string `json:"uiAccent"`
		ProfileTheme  string `json:"profileTheme"` // celui du profil V3X
		ProfileAccent string `json:"profileAccent"`
	} `json:"appearance"`
}

var (
	themes  = map[string]bool{"dark": true, "light": true, "blue": true}
	accents = map[string]bool{"bleu": true, "rose": true, "vert": true, "violet": true, "cyan": true, "ambre": true, "braise": true, "lime": true}
)

// state ne renvoie JAMAIS la clé ni le mot de passe : seulement leur présence.
func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var o stateOut
	o.Version, o.Update = s.Version, s.update
	o.V3X.Configured = s.cfg.V3XKey != ""
	o.V3X.OK = s.me != nil
	o.V3X.Error = s.v3xErr
	if s.me != nil {
		o.V3X.Username, o.V3X.Ratio, o.V3X.HitAndRun = s.me.Username, s.me.Ratio, s.me.HitAndRun
	}
	o.Clients = s.clientsOut()
	o.Category, o.Tag, o.CheckUpdates = s.cfg.Category, s.cfg.Tag, s.cfg.CheckUpdates
	o.PubCategory = s.cfg.PubCategory
	o.Discogs = s.cfg.DiscogsToken != ""
	o.LastDir = s.cfg.LastDir
	o.Watch = s.surveyState(s.cfg.Watch)
	o.Links.On, o.Links.Symbolic = s.cfg.Links.On, s.cfg.Links.Symbolic
	if s.opts.Remote {
		o.Mode = "server"
		if s.opts.Managed == "docker" {
			o.Mode = "docker"
		}
	}
	if c := s.cfg.SSH; c != nil {
		o.SSH.Configured, o.SSH.Label, o.SSH.Host, o.SSH.Port, o.SSH.User, o.SSH.KeyPath = true, c.Label(), c.Host, c.Port, c.User, c.KeyPath
		o.SSH.OK, o.SSH.System, o.SSH.Error = s.ssh != nil, s.sshSys, s.sshErr
		o.SSH.Saved = append(o.SSH.Saved, sshSavedOut{c.Label(), c.Host, c.Port, c.User, c.KeyPath})
	}
	for _, c := range s.cfg.SSHSaved {
		if s.cfg.SSH != nil && c.Same(*s.cfg.SSH) {
			continue
		}
		o.SSH.Saved = append(o.SSH.Saved, sshSavedOut{c.Label(), c.Host, c.Port, c.User, c.KeyPath})
	}
	// Apparence : choix fait dans D3X, sinon celui du profil V3X, sinon le
	// défaut du site (sombre, bleu).
	o.Appearance.Theme, o.Appearance.Accent = "dark", "bleu"
	if s.me != nil {
		if t := s.me.Appearance.Theme; t != nil && themes[*t] {
			o.Appearance.Theme = *t
		}
		if a := s.me.Appearance.Accent; a != nil && accents[*a] {
			o.Appearance.Accent = *a
		}
	}
	o.Appearance.ProfileTheme, o.Appearance.ProfileAccent = o.Appearance.Theme, o.Appearance.Accent
	if themes[s.cfg.UITheme] {
		o.Appearance.Theme = s.cfg.UITheme
	}
	if accents[s.cfg.UIAccent] {
		o.Appearance.Accent = s.cfg.UIAccent
	}
	o.Appearance.UITheme, o.Appearance.UIAccnt = s.cfg.UITheme, s.cfg.UIAccent
	writeJSON(w, http.StatusOK, o)
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		V3XKey      string `json:"v3xKey"`
		Category    string `json:"category"`
		PubCategory string `json:"pubCategory"`
		// DiscogsToken : vide = inchangé ; DiscogsClear le retire.
		DiscogsToken string `json:"discogsToken"`
		DiscogsClear bool   `json:"discogsClear"`
		Tag          string `json:"tag"`
		CheckUpdates bool   `json:"checkUpdates"`
		UITheme      string `json:"uiTheme"`
		UIAccent     string `json:"uiAccent"`
		// Clients : la liste complète, dans l'ordre ; absente = inchangée.
		Clients *[]clientIn `json:"clients"`
		// Watch : absent = inchangé.
		Watch *struct {
			On        bool  `json:"on"`
			Minutes   int   `json:"minutes"`
			Notify    bool  `json:"notify"`
			AutoAdd   bool  `json:"autoAdd"`
			Autostart *bool `json:"autostart"`
		} `json:"watch"`
		// Links : absent = inchangé ; le dossier des liens est celui de
		// chaque client.
		Links *struct {
			On       bool `json:"on"`
			Symbolic bool `json:"symbolic"`
		} `json:"links"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<18)).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, errors.New("réglages illisibles"))
		return
	}
	if s.engine.Scanning() {
		fail(w, http.StatusConflict, errors.New("attends la fin de l'analyse pour changer les réglages"))
		return
	}
	s.mu.Lock()
	c := *s.cfg
	// Champ secret laissé vide = on garde l'ancien (la page ne les connaît pas).
	if k := strings.TrimSpace(in.V3XKey); k != "" {
		c.V3XKey = k
	}
	if in.Clients != nil {
		list, err := mergeClients(c.Clients, *in.Clients)
		if err != nil {
			s.mu.Unlock()
			fail(w, http.StatusBadRequest, err)
			return
		}
		c.Clients = list
	}
	c.Category = strings.TrimSpace(in.Category)
	c.PubCategory = strings.TrimSpace(in.PubCategory)
	if t := strings.TrimSpace(in.DiscogsToken); t != "" {
		c.DiscogsToken = t
	} else if in.DiscogsClear {
		c.DiscogsToken = ""
	}
	c.Tag = strings.TrimSpace(in.Tag)
	c.CheckUpdates = in.CheckUpdates
	c.UITheme, c.UIAccent = "", ""
	if themes[in.UITheme] {
		c.UITheme = in.UITheme
	}
	if accents[in.UIAccent] {
		c.UIAccent = in.UIAccent
	}
	if in.Links != nil {
		dir := false
		for _, cc := range c.Clients {
			dir = dir || cc.LinksDir != ""
		}
		if in.Links.On && !dir {
			s.mu.Unlock()
			fail(w, http.StatusBadRequest, errors.New("liens : indique le dossier des liens, tel que ton client le voit"))
			return
		}
		c.Links.On, c.Links.Symbolic = in.Links.On, in.Links.Symbolic
	}
	restartWatch := false
	if in.Watch != nil {
		restartWatch = in.Watch.On && !c.Watch.On
		c.Watch.On, c.Watch.Minutes, c.Watch.Notify, c.Watch.AutoAdd = in.Watch.On, in.Watch.Minutes, in.Watch.Notify, in.Watch.AutoAdd
		c.Watch.Minutes = c.Watch.Every()
	}
	s.cfg = &c
	s.mu.Unlock()
	if err := config.Save(&c); err != nil {
		fail(w, http.StatusInternalServerError, errors.New("réglages non enregistrés : "+withPermHint(err).Error()))
		return
	}
	if restartWatch {
		s.surveyReset()
	}
	if in.Watch != nil && in.Watch.Autostart != nil && desktop.HasAutostart && !s.opts.Remote && *in.Watch.Autostart != desktop.Autostart() {
		if err := desktop.SetAutostart(*in.Watch.Autostart); err != nil {
			fail(w, http.StatusInternalServerError, errors.New("lancement avec Windows : "+err.Error()))
			return
		}
	}
	s.connect()
	s.state(w, r)
}

func (s *Server) reconnect(w http.ResponseWriter, r *http.Request) {
	s.connect()
	s.state(w, r)
}

func (s *Server) scan(w http.ResponseWriter, r *http.Request) {
	if err := s.ready(); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if err := s.engine.StartScan(); err != nil {
		fail(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, s.engine.State())
}

func (s *Server) stop(w http.ResponseWriter, r *http.Request) {
	s.engine.Stop()
	writeJSON(w, http.StatusOK, s.engine.State())
}

// lotSummary : ce que toute page doit savoir du lot (état, MediaInfo demandé).
type lotSummary struct {
	State           string             `json:"state"`
	MediaInfoWanted *mediainfo.Request `json:"mediainfoWanted,omitempty"`
}

func (s *Server) lotSummary() *lotSummary {
	state, ok := s.lot.Summary()
	if !ok {
		return nil
	}
	out := &lotSummary{State: state}
	if state == batch.StRunning {
		if p := s.miPage.Pending(); p != nil {
			out.State, out.MediaInfoWanted = "paused", p
		}
	}
	return out
}

// status : l'état de l'analyse, et celui du lot pour que la page de
// n'importe quel onglet voie qu'une demande de MediaInfo attend.
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, struct {
		crossseed.Status
		Lot *lotSummary `json:"lot,omitempty"`
	}{s.engine.State(), s.lotSummary()})
}

func (s *Server) rows(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{"version": s.engine.State().Version, "rows": s.engine.Rows()})
}

// rowDiag : le diagnostic d'une ligne de l'analyse, à copier pour le staff.
func (s *Server) rowDiag(w http.ResponseWriter, r *http.Request) {
	text, ok := s.engine.Diag(r.Context(), r.URL.Query().Get("key"), s.Version)
	if !ok {
		fail(w, http.StatusNotFound, errors.New("cette ligne n'est plus dans l'analyse, relance-la"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": text})
}

func (s *Server) apply(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Keys []string `json:"keys"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&in); err != nil || len(in.Keys) == 0 {
		fail(w, http.StatusBadRequest, errors.New("aucune release choisie"))
		return
	}
	n, err := s.engine.Apply(in.Keys)
	if err != nil {
		fail(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"queued": n})
}

// recheck relance la vérification d'un ajout marqué en échec (bouton
// « Revérifier ») : il démarrera à 100 %.
func (s *Server) recheck(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&in); err != nil || in.Key == "" {
		fail(w, http.StatusBadRequest, errors.New("release manquante"))
		return
	}
	if err := s.engine.Recheck(in.Key); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) remove(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&in); err != nil || in.Key == "" {
		fail(w, http.StatusBadRequest, errors.New("release manquante"))
		return
	}
	if err := s.engine.Remove(in.Key); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{"events": journal.Last(300)})
}

// cleanRules : règles de dossiers complètes (les deux côtés), sans doublon.
func cleanRules(in []pathmap.Rule) ([]pathmap.Rule, error) {
	out := []pathmap.Rule{}
	seen := map[string]bool{}
	for _, r := range in {
		r.From, r.To = strings.TrimSpace(r.From), strings.TrimSpace(r.To)
		if r.From == "" && r.To == "" {
			continue
		}
		if r.From == "" || r.To == "" {
			return nil, errors.New("dossiers du client : remplis les deux côtés de chaque ligne (ou vide-la)")
		}
		if len(out) >= 50 {
			return nil, errors.New("dossiers du client : 50 lignes au plus")
		}
		k := strings.ToLower(r.From + "|" + r.To)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, r)
	}
	return out, nil
}

// maskURL cache le mot de passe écrit dans une adresse (« user:pass@hôte ») :
// la page n'en a jamais besoin.
func maskURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	if _, has := u.User.Password(); !has {
		return raw
	}
	u.User = url.UserPassword(u.User.Username(), "***")
	return strings.Replace(u.String(), "%2A%2A%2A", "***", 1)
}
