package crossseed

import (
	"fmt"
	"strings"

	"github.com/V3x0rV3x/D3X/internal/client"
)

// winLike : chemin Windows (lettre de lecteur ou « \ », partage réseau compris).
func winLike(p string) bool {
	return strings.Contains(p, `\`) || (len(p) >= 2 && p[1] == ':')
}

// ForeignDir dit si dir est un chemin de ce PC Windows qu'un client tournant
// ailleurs (NAS, Docker, seedbox sous Linux) ne saura pas lire : dir est un
// chemin Windows, et le client range ses torrents sous des chemins Linux
// (« / »). Sans correspondance de dossiers, D3X envoyait ce chemin tel quel, et
// le client le collait derrière son propre dossier
// (« /share/Series2/\\NAS-Jo\Series\From », ftpktf 7/11/26). Un client sans
// torrent, ou qui range lui-même ses torrents sous des chemins Windows (il
// tourne sur ce PC), n'est jamais concerné.
func ForeignDir(dir string, list []client.Torrent) bool {
	if !winLike(dir) {
		return false
	}
	unix, win := 0, 0
	for _, t := range list {
		switch p := t.ContentPath; {
		case p == "":
		case winLike(p):
			win++
		case strings.HasPrefix(p, "/"):
			unix++
		}
	}
	return unix > 0 && unix > 3*win
}

// ForeignDirError : le message qui dit au membre quoi régler.
func ForeignDirError(dir, clientName string) error {
	return fmt.Errorf("%s ne peut pas lire « %s » : c'est un chemin de ce PC, et ce client tourne ailleurs. Ajoute ce dossier dans Réglages ▸ Clients torrent ▸ Dossiers du client : à gauche « %s », à droite le même dossier tel que %s le voit", clientName, dir, dir, clientName)
}
