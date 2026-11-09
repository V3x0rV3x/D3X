package crossseed

import (
	"context"
	"fmt"

	"github.com/V3x0rV3x/D3X/internal/client"
	"github.com/V3x0rV3x/D3X/internal/torrent"
)

// Fichiers que le client ne voit pas (meptalon, 9/11/26) : D3X et le client
// ne lisent pas toujours le même disque (deux conteneurs Docker, un partage
// réseau). La Médiathèque avait vérifié des morceaux du fichier sur le disque
// de D3X ; le client le cherchait sur le sien, ne le trouvait pas, et la
// release restait arrêtée à 0 %, même après « Forcer la revérification ».
// Avant d'ajouter, on demande donc au client s'il voit le premier et le
// dernier fichier, là où il va les lire. Seul un « non » net arrête l'ajout :
// un client qui ne sait pas répondre (Transmission, rTorrent, qBittorrent
// avant la 5) ou une erreur laissent passer, comme avant.

// Unseen renvoie le premier de ces chemins (vus par le client) que le client
// dit ne pas trouver, "" s'il les voit tous ou ne sait pas le dire. Au plus
// deux questions : le premier et le dernier chemin.
func Unseen(ctx context.Context, q client.Client, paths []string) string {
	picks := paths
	if len(picks) > 2 {
		picks = []string{paths[0], paths[len(paths)-1]}
	}
	for _, p := range picks {
		if exists, supported, err := q.Lookup(ctx, p); err == nil && supported && !exists {
			return p
		}
	}
	return ""
}

// UnseenRelease : Unseen pour les fichiers du .torrent raw rangés dans save
// (vu par le client). "" si le .torrent est illisible.
func UnseenRelease(ctx context.Context, q client.Client, save string, raw []byte) string {
	t, err := torrent.Parse(raw)
	if err != nil || len(t.Files) == 0 {
		return ""
	}
	paths := make([]string, len(t.Files))
	for i, f := range t.Files {
		paths[i] = clientJoin(save, f.Path)
	}
	return Unseen(ctx, q, paths)
}

// UnseenError : le message qui dit au membre quoi vérifier.
func UnseenError(p, clientName string) error {
	return fmt.Errorf("%s ne trouve pas « %s », que D3X voit pourtant : ce dossier n'est pas au même endroit pour les deux (conteneurs Docker, partage réseau), ou %s n'a pas le droit de le lire. Monte-le au même endroit pour les deux, ou déclare la correspondance dans Réglages ▸ Clients torrent ▸ Dossiers du client", clientName, p, clientName)
}
