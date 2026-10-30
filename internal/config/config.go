// Package config garde les réglages de D3X sur la machine du membre.
//
// Dossier : <configuration de l'utilisateur>/D3X (config.json en 0600, cache
// des empreintes, journal). Rien de tout ça n'est envoyé ailleurs.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/V3x0rV3x/D3X/internal/pathmap"
)

// Config regroupe les réglages.
type Config struct {
	V3XBase string `json:"v3xBase,omitempty"` // vide = https://api.v3x.club
	V3XKey  string `json:"v3xKey"`

	// Clients torrent, dans l'ordre choisi par le membre : le premier qui a
	// les fichiers d'une release reçoit son cross-seed.
	Clients []ClientConfig `json:"clients,omitempty"`

	// Anciens réglages d'un seul client (D3X 1.0) : relus une fois pour
	// former la liste, puis tenus égaux au premier client à chaque
	// enregistrement, pour qu'un retour en 1.0 retrouve son client.
	ClientKind   string `json:"clientKind,omitempty"`
	QbitURL      string `json:"qbitUrl"`
	QbitUser     string `json:"qbitUser"`
	QbitPass     string `json:"qbitPass"`
	QbitInsecure bool   `json:"qbitInsecure,omitempty"`

	// Catégorie et étiquette posées sur les torrents ajoutés par D3X.
	Category string `json:"category"`
	Tag      string `json:"tag"`
	// Catégorie des releases publiées avec D3X (Publier, Lot), si elle doit
	// différer de celle des cross-seeds (demande d'Ekhstaz). Vide = Category.
	PubCategory string `json:"pubCategory,omitempty"`
	// DiscogsToken : jeton personnel Discogs (facultatif), pour chercher une
	// pochette quand MusicBrainz n'en a pas. Jamais renvoyé à la page.
	DiscogsToken string `json:"discogsToken,omitempty"`

	// HashLimitMB : vitesse de lecture des fichiers de ce PC en Mo/s, pour
	// fabriquer un .torrent (Publier, Lot) ou vérifier la Médiathèque ; 0 =
	// sans limite. Utile quand les fichiers sont sur un NAS joint par le
	// réseau (Mamaiii, 30/10/26).
	HashLimitMB int `json:"hashLimitMB,omitempty"`

	// Vérifier au lancement qu'une nouvelle version existe (GitHub).
	CheckUpdates bool `json:"checkUpdates"`

	// Fichiers et dossiers écartés des releases (motifs comparés au nom seul).
	// nil = motifs par défaut ; liste vide = rien d'écarté (hors fichiers cachés).
	Exclude []string `json:"exclude,omitempty"`

	// Correspondance des chemins du premier client (D3X 1.0) : chaque client
	// a désormais la sienne, celle-ci n'est plus qu'un reflet.
	PathMap []pathmap.Rule `json:"pathMap,omitempty"`

	// Dernier dossier ouvert dans le sélecteur.
	LastDir string `json:"lastDir,omitempty"`

	// Seedbox jointe par SSH (sans secret : le mot de passe n'est jamais écrit).
	SSH *SSH `json:"ssh,omitempty"`
	// Seedboxes déjà connectées (sans secret), pour passer de l'une à
	// l'autre d'un clic (GoodMoviesForBadPeople, 21/10/26). SSH est l'actuelle.
	SSHSaved []SSH `json:"sshSaved,omitempty"`

	// Apparence : fond (dark, light, blue) et accent ; vide = comme sur V3X.
	UITheme  string `json:"uiTheme,omitempty"`
	UIAccent string `json:"uiAccent,omitempty"`

	// Surveillance : nouvelle analyse régulière du client, notifications.
	Watch Watch `json:"watch"`

	// Liens physiques : quand renommer dans le client est impossible, la
	// release V3X est rangée dans Links.Dir (vu par le client), ses fichiers
	// étant des liens vers ceux de ta copie.
	Links Links `json:"links"`

	// Médiathèque : dossiers où chercher des releases V3X (films et séries rangés).
	Library []LibFolder `json:"library,omitempty"`
	// LibraryClient : le client qui reçoit les ajouts de la médiathèque
	// (identifiant ; vide ou inconnu = le premier).
	LibraryClient string `json:"libraryClient,omitempty"`
}

// MaxClients : clients torrent au plus.
const MaxClients = 8

// ClientConfig : un client torrent et sa façon de voir les dossiers.
type ClientConfig struct {
	// ID : identifiant stable (les ajouts en cours et la médiathèque s'y
	// réfèrent), jamais montré au membre.
	ID string `json:"id"`
	// Name : le nom choisi par le membre (« Seedbox », « PC »).
	Name string `json:"name,omitempty"`
	// Kind : qbittorrent (défaut), transmission, deluge, rtorrent.
	Kind     string `json:"kind,omitempty"`
	URL      string `json:"url"`
	User     string `json:"user,omitempty"`
	Pass     string `json:"pass,omitempty"`
	Insecure bool   `json:"insecure,omitempty"`
	// PathMap : où D3X voit les dossiers de CE client.
	PathMap []pathmap.Rule `json:"pathMap,omitempty"`
	// LinksDir : le dossier des liens physiques, tel que ce client le voit.
	LinksDir string `json:"linksDir,omitempty"`
}

// NewClientID tire l'identifiant d'un nouveau client.
func NewClientID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// Client renvoie le client d'identifiant id (nil s'il n'existe pas).
func (c *Config) Client(id string) *ClientConfig {
	for i := range c.Clients {
		if c.Clients[i].ID == id {
			return &c.Clients[i]
		}
	}
	return nil
}

// migrate forme la liste des clients d'une configuration de D3X 1.0 (un seul
// client, champs « qbit ») sans rien perdre, et donne un identifiant à chaque
// client qui n'en a pas. Toujours le même résultat pour le même fichier :
// deux lectures donnent les mêmes identifiants.
func (c *Config) migrate() {
	if len(c.Clients) == 0 && strings.TrimSpace(c.QbitURL) != "" {
		c.Clients = []ClientConfig{{
			ID: "c1", Kind: c.ClientKind, URL: c.QbitURL, User: c.QbitUser, Pass: c.QbitPass, Insecure: c.QbitInsecure,
			PathMap: c.PathMap, LinksDir: c.Links.Dir,
		}}
	}
	seen := map[string]bool{}
	for i := range c.Clients {
		id := c.Clients[i].ID
		for n := i + 1; id == "" || seen[id]; n++ {
			id = "c" + strconv.Itoa(n)
		}
		c.Clients[i].ID = id
		seen[id] = true
	}
}

// mirror tient les anciens champs égaux au premier client (rien s'il n'y en
// a plus : un client retiré ne doit pas revenir à la lecture suivante).
func (c *Config) mirror() {
	if len(c.Clients) == 0 {
		c.ClientKind, c.QbitURL, c.QbitUser, c.QbitPass, c.QbitInsecure = "", "", "", "", false
		c.PathMap = nil
		return
	}
	f := c.Clients[0]
	c.ClientKind, c.QbitURL, c.QbitUser, c.QbitPass, c.QbitInsecure = f.Kind, f.URL, f.User, f.Pass, f.Insecure
	c.PathMap = f.PathMap
	c.Links.Dir = f.LinksDir
}

// LibFolder : un dossier de la médiathèque, sur ce PC (local) ou sur la
// seedbox (ssh).
type LibFolder struct {
	Path   string `json:"path"`
	Source string `json:"source"`
}

// Links : le repli par liens physiques.
type Links struct {
	On  bool   `json:"on"`
	Dir string `json:"dir,omitempty"`
	// Symbolic : liens symboliques au lieu de liens physiques, pour une copie
	// qu'on ne peut pas lier physiquement (partage en lecture seule, autre
	// disque ; 12a3, 21/10/26). Le lien vise le fichier tel que le client le voit.
	Symbolic bool `json:"symbolic,omitempty"`
}

// Watch : la surveillance du client.
type Watch struct {
	On      bool `json:"on"`
	Minutes int  `json:"minutes,omitempty"` // entre deux analyses (30 par défaut)
	Notify  bool `json:"notify"`
	// AutoAdd : ajoute seules les releases identiques trouvées depuis la
	// dernière analyse (jamais les « noms différents »).
	AutoAdd bool `json:"autoAdd,omitempty"`
}

// Every : l'intervalle entre deux analyses, borné.
func (w Watch) Every() int {
	switch {
	case w.Minutes <= 0:
		return 30
	case w.Minutes < 15:
		return 15
	case w.Minutes > 24*60:
		return 24 * 60
	}
	return w.Minutes
}

// ExcludeList renvoie les motifs écartés en vigueur.
func (c *Config) ExcludeList() []string {
	if c.Exclude == nil {
		return DefaultExclude
	}
	return c.Exclude
}

// DefaultExclude : ce que les médiathèques déposent à côté d'un film et qui
// n'a rien à faire dans une release.
var DefaultExclude = []string{"*.trickplay", "extrafanart", "extrathumbs", "Thumbs.db", "desktop.ini", "@eaDir", "#recycle", "*.part", "*.!qB", "*.parts"}

// Defaults renvoie une configuration neuve.
func Defaults() *Config {
	return &Config{Tag: "D3X", CheckUpdates: true, Watch: Watch{Notify: true}}
}

var mu sync.Mutex

// Dir renvoie le dossier de D3X (créé au besoin, lisible par le seul membre).
func Dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	d := filepath.Join(base, "D3X")
	return d, os.MkdirAll(d, 0o700)
}

// Path renvoie le chemin d'un fichier du dossier de D3X.
func Path(name string) (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, name), nil
}

// Load lit la configuration (réglages par défaut si absente).
func Load() (*Config, error) {
	mu.Lock()
	defer mu.Unlock()
	p, err := Path("config.json")
	if err != nil {
		return nil, err
	}
	c := Defaults()
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	return c, Parse(b, c)
}

// Parse lit un config.json sur c (déjà rempli des réglages par défaut) et
// forme la liste des clients d'une configuration ancienne.
func Parse(b []byte, c *Config) error {
	if err := json.Unmarshal(b, c); err != nil {
		return err
	}
	c.migrate()
	return nil
}

// Marshal : le config.json d'une configuration, anciens champs tenus égaux au
// premier client. c n'est pas modifié.
func Marshal(c *Config) ([]byte, error) {
	cp := *c
	cp.mirror()
	return json.MarshalIndent(&cp, "", "  ")
}

// Save enregistre la configuration.
func Save(c *Config) error {
	mu.Lock()
	defer mu.Unlock()
	b, err := Marshal(c)
	if err != nil {
		return err
	}
	return WriteFileAtomic("config.json", b)
}

// WriteFileAtomic écrit un fichier du dossier de D3X (0600) sans jamais
// laisser un fichier à moitié écrit.
func WriteFileAtomic(name string, data []byte) error {
	p, err := Path(name)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// SSH : la seedbox jointe par SSH (sans secret).
type SSH struct {
	Host    string `json:"host"`
	Port    int    `json:"port,omitempty"`
	User    string `json:"user"`
	KeyPath string `json:"keyPath,omitempty"` // vide = clés habituelles de ~/.ssh
	HostKey string `json:"hostKey,omitempty"` // clé d'hôte acceptée (format authorized_keys)
}

// Label : « user@hôte ».
func (c SSH) Label() string { return c.User + "@" + c.Host }

// Same : même seedbox (adresse, port et utilisateur).
func (c SSH) Same(o SSH) bool {
	return strings.EqualFold(c.Host, o.Host) && c.Port == o.Port && c.User == o.User
}

// MaxSSHSaved : seedboxes gardées au plus dans SSHSaved.
const MaxSSHSaved = 8

// RememberSSH met c en tête des seedboxes enregistrées (remplace la même).
func (cfg *Config) RememberSSH(c SSH) {
	out := []SSH{c}
	for _, o := range cfg.SSHSaved {
		if !o.Same(c) && len(out) < MaxSSHSaved {
			out = append(out, o)
		}
	}
	cfg.SSHSaved = out
}

// ForgetSSH retire c des seedboxes enregistrées.
func (cfg *Config) ForgetSSH(c SSH) {
	out := cfg.SSHSaved[:0:0]
	for _, o := range cfg.SSHSaved {
		if !o.Same(c) {
			out = append(out, o)
		}
	}
	cfg.SSHSaved = out
}
