package crossseed

import (
	"context"
	"time"

	"github.com/V3x0rV3x/D3X/internal/client"
)

// Attente des réponses du client après un ajout. 15 secondes ne suffisaient
// pas à un qBittorrent sur NAS occupé à vérifier un gros fichier : son
// interface restait figée près d'une minute, D3X abandonnait avant de
// renommer et le torrent restait dans le client sous les noms de la fiche
// (YataReborn, Flashdance, 28/11/26). Variables pour les tests.
var (
	clientWait = 2 * time.Minute
	clientPoll = 500 * time.Millisecond
)

// waitClient relance ok toutes les clientPoll jusqu'à ce qu'il réponde vrai,
// au plus clientWait (ou jusqu'à l'annulation de ctx).
func waitClient(ctx context.Context, ok func() bool) bool {
	deadline := time.Now().Add(clientWait)
	for {
		if ok() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(clientPoll):
		}
	}
}

// removeAdded retire un torrent que D3X vient d'ajouter (fichiers gardés),
// en insistant tant que le client ne répond pas : un retrait raté laissait le
// torrent dans le client sous les noms de la fiche, prêt à retélécharger.
func removeAdded(q client.Client, hash string) {
	ctx, cancel := context.WithTimeout(context.Background(), clientWait+clientPoll)
	defer cancel()
	waitClient(ctx, func() bool {
		if q.RemoveKeepFiles(ctx, hash) == nil {
			return true
		}
		lst, err := q.List(ctx, []string{hash})
		return err == nil && len(lst) == 0
	})
}
