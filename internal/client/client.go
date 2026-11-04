// Package client donne une seule interface aux clients torrent pris en charge
// (qBittorrent, Transmission, Deluge, rTorrent). Le cross-seed et la
// publication ne parlent qu'à elle.
//
// Règles communes, quel que soit le client : un ajout se fait TOUJOURS à
// l'arrêt et sans sauter la vérification, et un retrait ne supprime JAMAIS
// les fichiers.
package client

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/V3x0rV3x/D3X/internal/torrent"
)

// Torrent est un torrent du client, sous une forme commune.
type Torrent struct {
	Hash        string   // identifiant, en minuscules
	Hashes      []string // tous les identifiants connus (v1, v2)
	Name        string
	ContentPath string // dossier racine (multi-fichiers) ou fichier (mono)
	Size        int64  // octets sélectionnés
	TotalSize   int64
	Progress    float64 // 0 à 1
	Checking    bool    // vérifie, ou s'apprête à vérifier
	Downloading bool    // cherche à recevoir des données
	Stopped     bool    // arrêté (ne seede pas)
	Trackers    []string
	// Category : la catégorie du torrent dans le client (qBittorrent ; vide
	// ailleurs). Sert au modèle {cat} de la catégorie des cross-seeds.
	Category string
	// Tags : les étiquettes du torrent dans le client (tags de qBittorrent,
	// labels de Transmission, label de Deluge, custom1 de rTorrent), telles
	// qu'écrites ; vide si le client n'en met pas. Le Lot écarte les releases
	// d'un torrent qui porte une étiquette à ne jamais publier (noHL).
	Tags []string
}

// AddOptions règle un ajout (toujours à l'arrêt).
type AddOptions struct {
	SavePath string // dossier qui contient la release, vu par le client
	Category string
	Tag      string
}

// Client est un client torrent.
type Client interface {
	// Kind nomme le client (« qBittorrent »…).
	Kind() string
	Version(ctx context.Context) (string, error)
	// List liste les torrents, ou seulement ceux des identifiants donnés.
	List(ctx context.Context, hashes []string) ([]Torrent, error)
	// Export rend le .torrent d'origine ; ErrNoExport si le client ne sait pas.
	Export(ctx context.Context, hash string) ([]byte, error)
	// Files rend les fichiers (chemins actuels, avec la racine ; Index : rang
	// dans le .torrent) et la taille totale, de quoi calculer les empreintes
	// sans le .torrent.
	Files(ctx context.Context, hash string) ([]torrent.File, int64, error)
	// CanRename : le client sait renommer les fichiers d'un torrent.
	CanRename() bool
	// Lookup dit si un chemin existe sur le disque du client ; supported :
	// le client sait le dire.
	Lookup(ctx context.Context, path string) (exists, supported bool, err error)
	// RenameFiles renomme des fichiers d'un torrent arrêté, jamais vérifié
	// (voir rename.go).
	RenameFiles(ctx context.Context, hash string, renames []Rename) error
	Add(ctx context.Context, name string, raw []byte, o AddOptions) error
	Recheck(ctx context.Context, hash string) error
	Start(ctx context.Context, hash string) error
	Stop(ctx context.Context, hash string) error
	RemoveKeepFiles(ctx context.Context, hash string) error
}

// Erreurs communes.
var (
	ErrNoExport     = errors.New("ce client ne rend pas le .torrent d'origine")
	ErrAlreadyThere = errors.New("ce torrent est déjà dans le client (même info-hash)")
	ErrAuth         = errors.New("identifiants du client refusés")
)

// Kinds : les clients pris en charge, pour les réglages.
var Kinds = []string{"qbittorrent", "transmission", "deluge", "rtorrent"}

// KindName : le nom affiché d'un type de client (vide = qBittorrent), vide
// s'il est inconnu.
func KindName(kind string) string {
	switch kind {
	case "", "qbittorrent":
		return "qBittorrent"
	case "transmission":
		return "Transmission"
	case "deluge":
		return "Deluge"
	case "rtorrent":
		return "rTorrent"
	}
	return ""
}

// Options : connexion à un client.
type Options struct {
	Kind     string
	URL      string
	User     string
	Pass     string
	Insecure bool
}

// New ouvre un client du type demandé.
func New(o Options) (Client, error) {
	switch o.Kind {
	case "", "qbittorrent":
		return newQbit(o)
	case "transmission":
		return newTransmission(o)
	case "deluge":
		return newDeluge(o)
	case "rtorrent":
		return newRTorrent(o)
	}
	return nil, fmt.Errorf("client inconnu : %s", o.Kind)
}

// TrackerHosts extrait les hôtes d'une liste d'adresses d'annonce.
func TrackerHosts(urls []string) []string {
	var out []string
	for _, raw := range urls {
		if u, err := url.Parse(strings.TrimSpace(raw)); err == nil && u.Hostname() != "" {
			out = append(out, strings.ToLower(u.Hostname()))
		}
	}
	return out
}

// normURL complète une adresse saisie (« 127.0.0.1:9091 » → http://…).
func normURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, errors.New("adresse du client invalide")
	}
	return u, nil
}

// joinPath colle un dossier et un nom avec le séparateur du dossier.
func joinPath(dir, name string) string {
	if dir == "" {
		return name
	}
	sep := "/"
	if strings.Contains(dir, `\`) && !strings.Contains(dir, "/") {
		sep = `\`
	}
	return strings.TrimRight(dir, `/\`) + sep + name
}

// tagList nettoie des étiquettes : sans les blancs, sans les vides ; nil s'il
// n'en reste aucune.
func tagList(parts []string) []string {
	var out []string
	for _, t := range parts {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}
