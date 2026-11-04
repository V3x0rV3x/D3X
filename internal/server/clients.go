package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/V3x0rV3x/D3X/internal/client"
	"github.com/V3x0rV3x/D3X/internal/config"
	"github.com/V3x0rV3x/D3X/internal/crossseed"
	"github.com/V3x0rV3x/D3X/internal/pathmap"
	"github.com/V3x0rV3x/D3X/internal/qbit"
)

// Plusieurs clients torrent (une seedbox et ce PC, par exemple). Chacun est
// joint à part : un client injoignable est signalé, les autres servent.

// clientConn : un client des réglages, joint (c non nil) ou non (err).
type clientConn struct {
	sig string // réglages de connexion utilisés (un changement oblige à refaire le test)
	c   client.Client
	ver string // « qBittorrent v5.0.2 »
	err string
	// at : fin de cette tentative. auth : refusé sur ses identifiants (ou
	// réglage illisible) : jamais retenté seul, qBittorrent bloquerait
	// l'adresse après quelques échecs. auto : tentatives automatiques faites
	// depuis le dernier geste du membre (clientRetryLoop).
	at   time.Time
	auth bool
	auto int
}

// connSig : ce qui, dans les réglages d'un client, change sa connexion.
func connSig(cc config.ClientConfig) string {
	return strings.Join([]string{cc.Kind, cc.URL, cc.User, cc.Pass, strconv.FormatBool(cc.Insecure)}, "\x00")
}

// dial ouvre un client et vérifie qu'il répond.
func dial(ctx context.Context, cc config.ClientConfig) *clientConn {
	k := &clientConn{sig: connSig(cc)}
	defer func() { k.at = time.Now() }()
	c, err := client.New(client.Options{Kind: cc.Kind, URL: cc.URL, User: cc.User, Pass: cc.Pass, Insecure: cc.Insecure})
	if err != nil {
		k.err, k.auth = err.Error(), true
		return k
	}
	ver, err := c.Version(ctx)
	if err != nil {
		k.err, k.auth = crossseed.Reason(err), authRefused(err)
		if ctx.Err() != nil {
			k.err, k.auth = crossseed.NoAnswer, false
		}
		return k
	}
	k.c, k.ver = c, strings.TrimSpace(c.Kind()+" "+ver)
	return k
}

// authRefused : le client a refusé les identifiants (ou bloqué l'adresse après
// trop d'échecs). Tous ces messages parlent du mot de passe.
func authRefused(err error) bool {
	if errors.Is(err, client.ErrAuth) || errors.Is(err, qbit.ErrAuth) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "mot de passe")
}

// dialAll essaie tous les clients donnés en même temps.
func dialAll(ctx context.Context, list []config.ClientConfig) map[string]*clientConn {
	out := map[string]*clientConn{}
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	for _, cc := range list {
		wg.Add(1)
		go func(cc config.ClientConfig) {
			defer wg.Done()
			k := dial(ctx, cc)
			mu.Lock()
			out[cc.ID] = k
			mu.Unlock()
		}(cc)
	}
	wg.Wait()
	return out
}

// Nouvelle tentative sur un client injoignable (Encrypted_Shards, 04/11/26 :
// client en rouge, à retester dans les Réglages avant chaque analyse ou
// publication). maxAutoRetries : tentatives automatiques avant de s'arrêter
// jusqu'au prochain geste du membre.
const maxAutoRetries = 3

// Délais en nanosecondes, atomiques comme v3xRetryEvery (raccourcis dans les
// tests). retryGap : un client qui vient d'échouer n'est pas redemandé avant
// ce délai par un geste du membre, un client éteint ne ralentit pas chaque
// publication. clientRetryEvery : entre deux tentatives automatiques.
var retryGap, clientRetryEvery atomic.Int64

func init() {
	retryGap.Store(int64(20 * time.Second))
	clientRetryEvery.Store(int64(time.Minute))
}

// retryClients retente les clients injoignables avant un travail lancé par le
// membre (analyse, Médiathèque, publication, lot) : le client d'une seedbox
// redémarré depuis le lancement de D3X revient sans passer par les réglages.
// Les tentatives automatiques repartent de zéro.
func (s *Server) retryClients(ctx context.Context) { s.retryDown(ctx, true) }

// clientRetryLoop : un client réglé mais injoignable (lancé après D3X, NAS ou
// seedbox qui redémarre) est retenté seul une fois par minute, trois fois au
// plus ; ensuite plus rien jusqu'au prochain geste du membre. Jamais un client
// qui a refusé ses identifiants.
func (s *Server) clientRetryLoop() {
	for {
		time.Sleep(time.Duration(clientRetryEvery.Load()))
		s.retryDown(context.Background(), false)
	}
}

// retryDown rejoint les clients injoignables. user : geste du membre (tout
// client pas joint, sauf un échec de moins de retryGap) ; sinon tentative
// automatique (échec réseau seulement, maxAutoRetries au plus). Une tentative
// à la fois : les autres attendent, puis trouvent l'échec tout frais.
func (s *Server) retryDown(ctx context.Context, user bool) {
	s.retryMu.Lock()
	defer s.retryMu.Unlock()
	now := time.Now()
	s.mu.Lock()
	var todo []config.ClientConfig
	was := map[string]*clientConn{}
	for _, cc := range s.cfg.Clients {
		k := s.conns[cc.ID]
		same := k != nil && k.sig == connSig(cc)
		switch {
		case same && k.c != nil: // joint
			continue
		case user && same:
			k.auto = 0
			if now.Sub(k.at) < time.Duration(retryGap.Load()) {
				continue
			}
		case user: // jamais tenté, ou réglages changés
		case !same || k.auth || k.auto >= maxAutoRetries:
			continue
		}
		todo = append(todo, cc)
		was[cc.ID] = k
	}
	s.mu.Unlock()
	if len(todo) == 0 {
		return
	}
	c, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	got := dialAll(c, todo)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conns == nil {
		s.conns = map[string]*clientConn{}
	}
	changed := false
	for id, k := range got {
		old := was[id]
		// Réglages changés, ou client rejoint autrement entre-temps (Réglages
		// enregistrés) : le résultat ne vaut plus.
		if cc := s.cfg.Client(id); cc == nil || connSig(*cc) != k.sig || s.conns[id] != old {
			continue
		}
		if !user && old != nil {
			k.auto = old.auto + 1
		}
		s.conns[id] = k
		changed = true
	}
	if changed {
		s.connGen.Add(1)
	}
}

// clientsDown : V3X est joint et au moins un client réglé ne l'est pas.
func (s *Server) clientsDown() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.v == nil || s.me == nil {
		return false
	}
	for _, cc := range s.cfg.Clients {
		if k := s.conns[cc.ID]; k == nil || k.c == nil || k.sig != connSig(cc) {
			return true
		}
	}
	return false
}

// envRetry : env pour la publication ; si aucun client ne répond, ils sont
// d'abord retentés, au lieu d'échouer sur un client passé en erreur depuis.
func (s *Server) envRetry() (*crossseed.Env, error) {
	env, err := s.env()
	if err == nil || !s.clientsDown() {
		return env, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s.retryClients(ctx)
	return s.env()
}

// kickClients : un geste du membre (Publier, lot) retente en tâche de fond un
// client injoignable, sans le faire attendre ; sa pastille suit.
func (s *Server) kickClients() {
	if s.clientsDown() {
		go s.retryClients(context.Background())
	}
}

// clientLabels : le nom affiché de chaque client (par identifiant) : celui
// choisi par le membre, sinon le type du client, numéroté s'il revient.
func clientLabels(list []config.ClientConfig) map[string]string {
	out := map[string]string{}
	seen := map[string]int{}
	for _, cc := range list {
		l := strings.TrimSpace(cc.Name)
		if l == "" {
			l = client.KindName(cc.Kind)
		}
		if l == "" {
			l = "Client torrent"
		}
		k := strings.ToLower(l)
		seen[k]++
		if seen[k] > 1 {
			l += " " + strconv.Itoa(seen[k])
		}
		out[cc.ID] = l
	}
	return out
}

// hostOf : le nom d'hôte de l'adresse d'un client (vide si illisible).
func hostOf(raw string) string {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// env fournit au moteur et à la publication V3X, les réglages et les clients
// joignables (les autres sont signalés). Erreur si V3X n'est pas joint ou si
// aucun client ne l'est.
func (s *Server) env() (*crossseed.Env, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.v == nil || s.me == nil {
		return nil, errors.New("D3X n'est pas connecté à V3X : vérifie ta clé API dans les réglages")
	}
	c := *s.cfg
	out := &crossseed.Env{V3X: s.v, Cfg: &c}
	out.Targets, out.Down = s.targetsLocked()
	if len(out.Targets) == 0 {
		if len(out.Down) == 0 {
			return nil, errors.New("D3X n'est pas connecté à ton client torrent : vérifie les réglages")
		}
		return nil, errors.New(crossseed.DownText(out.Down))
	}
	return out, nil
}

// targets : les clients des réglages, joignables (Targets) ou non (Down), sans
// exiger V3X : la liste du Lot lit leurs étiquettes même hors ligne.
func (s *Server) targets() (targets []crossseed.Target, down []crossseed.Down) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.targetsLocked()
}

// targetsLocked : targets, le verrou tenu.
func (s *Server) targetsLocked() (targets []crossseed.Target, down []crossseed.Down) {
	labels := clientLabels(s.cfg.Clients)
	for _, cc := range s.cfg.Clients {
		k := s.conns[cc.ID]
		switch {
		case k == nil || k.sig != connSig(cc):
			down = append(down, crossseed.Down{ID: cc.ID, Name: labels[cc.ID], Error: "connexion en cours"})
		case k.c == nil:
			down = append(down, crossseed.Down{ID: cc.ID, Name: labels[cc.ID], Error: k.err})
		default:
			targets = append(targets, crossseed.Target{ID: cc.ID, Name: labels[cc.ID], Client: k.c, PathMap: cc.PathMap, LinksDir: strings.TrimSpace(cc.LinksDir), Host: hostOf(cc.URL)})
		}
	}
	return targets, down
}

// ready : de quoi lancer une analyse (V3X joint, au moins un client réglé ;
// les clients injoignables sont retentés au début de l'analyse).
func (s *Server) ready() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.v == nil || s.me == nil {
		return errors.New("D3X n'est pas connecté à V3X : vérifie ta clé API dans les réglages")
	}
	if len(s.cfg.Clients) == 0 {
		return errors.New("D3X n'est pas connecté à ton client torrent : vérifie les réglages")
	}
	return nil
}

// clientOut : un client pour la page (jamais son mot de passe).
type clientOut struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`  // choisi par le membre (vide : aucun)
	Label    string         `json:"label"` // nom affiché
	Kind     string         `json:"kind"`
	OK       bool           `json:"ok"`
	Pending  bool           `json:"pending,omitempty"` // connexion pas encore testée
	Error    string         `json:"error,omitempty"`
	Version  string         `json:"version,omitempty"`
	URL      string         `json:"url,omitempty"`
	User     string         `json:"user,omitempty"`
	Insecure bool           `json:"insecure"`
	HasPass  bool           `json:"hasPass"`
	PathMap  []pathmap.Rule `json:"pathMap"`
	LinksDir string         `json:"linksDir,omitempty"`
}

// clientsOut : les clients des réglages et leur état (verrou tenu).
func (s *Server) clientsOut() []clientOut {
	out := []clientOut{}
	labels := clientLabels(s.cfg.Clients)
	for _, cc := range s.cfg.Clients {
		x := clientOut{ID: cc.ID, Name: cc.Name, Label: labels[cc.ID], Kind: cc.Kind, URL: maskURL(cc.URL), User: cc.User, Insecure: cc.Insecure, HasPass: cc.Pass != "", PathMap: append([]pathmap.Rule{}, cc.PathMap...), LinksDir: cc.LinksDir}
		if x.Kind == "" {
			x.Kind = "qbittorrent"
		}
		if k := s.conns[cc.ID]; k != nil && k.sig == connSig(cc) {
			x.OK, x.Version, x.Error = k.c != nil, k.ver, k.err
		} else {
			x.Pending = true
		}
		out = append(out, x)
	}
	return out
}

// clientIn : un client tel que la page l'envoie. Mot de passe vide = celui
// enregistré, si l'adresse, l'utilisateur et le type n'ont pas changé.
type clientIn struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Kind     string         `json:"kind"`
	URL      string         `json:"url"`
	User     string         `json:"user"`
	Pass     string         `json:"pass"`
	Insecure bool           `json:"insecure"`
	PathMap  []pathmap.Rule `json:"pathMap"`
	LinksDir string         `json:"linksDir"`
}

// mergeClients forme la nouvelle liste des clients d'après la page. Un client
// sans adresse est retiré ; un client inconnu reçoit un identifiant neuf.
func mergeClients(old []config.ClientConfig, in []clientIn) ([]config.ClientConfig, error) {
	byID := map[string]config.ClientConfig{}
	for _, cc := range old {
		byID[cc.ID] = cc
	}
	out := []config.ClientConfig{}
	names := map[string]bool{}
	ids := map[string]bool{}
	for _, x := range in {
		u := strings.TrimSpace(x.URL)
		if u == "" {
			continue
		}
		kind := strings.TrimSpace(x.Kind)
		if kind == "qbittorrent" {
			kind = ""
		}
		if client.KindName(kind) == "" {
			return nil, errors.New("client inconnu : " + kind)
		}
		prev, known := byID[x.ID]
		cc := config.ClientConfig{ID: x.ID, Kind: kind, User: strings.TrimSpace(x.User), Insecure: x.Insecure, LinksDir: strings.TrimSpace(x.LinksDir)}
		if !known || ids[x.ID] {
			cc.ID = config.NewClientID()
		}
		ids[cc.ID] = true
		// Adresse renvoyée masquée (« user:***@ ») : inchangée, on garde la vraie.
		if known && u == maskURL(prev.URL) {
			u = prev.URL
		}
		cc.URL = u
		switch {
		case x.Pass != "":
			cc.Pass = x.Pass
		case known && u == prev.URL && cc.User == prev.User && kind == prev.Kind:
			cc.Pass = prev.Pass
		}
		// Sinon (autre adresse, autre client), l'ancien mot de passe ne le
		// concerne pas.
		cc.Name = strings.TrimSpace(x.Name)
		if utf8.RuneCountInString(cc.Name) > 40 {
			return nil, errors.New("nom de client trop long (40 caractères au plus) : " + cc.Name)
		}
		if cc.Name != "" {
			k := strings.ToLower(cc.Name)
			if names[k] {
				return nil, errors.New("deux clients s'appellent « " + cc.Name + " » : donne-leur des noms différents")
			}
			names[k] = true
		}
		rules, err := cleanRules(x.PathMap)
		if err != nil {
			if cc.Name != "" {
				err = errors.New(cc.Name + " : " + err.Error())
			}
			return nil, err
		}
		cc.PathMap = rules
		out = append(out, cc)
	}
	if len(out) > config.MaxClients {
		return nil, errors.New(strconv.Itoa(config.MaxClients) + " clients torrent au plus")
	}
	return out, nil
}

// clientTest : essai de connexion d'un client des réglages, sans rien
// enregistrer (pastille de ce client dans les réglages).
func (s *Server) clientTest(w http.ResponseWriter, r *http.Request) {
	var in clientIn
	if !decode(w, r, 1<<16, &in) {
		return
	}
	s.mu.Lock()
	old := append([]config.ClientConfig(nil), s.cfg.Clients...)
	s.mu.Unlock()
	list, err := mergeClients(old, []clientIn{in})
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if len(list) == 0 {
		fail(w, http.StatusBadRequest, errors.New("indique l'adresse du client"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	k := dial(ctx, list[0])
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": k.c != nil, "version": k.ver, "error": k.err})
}
