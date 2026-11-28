package client

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/V3x0rV3x/D3X/internal/qbit"
)

// Torrents hybrides v1+v2 (YataReborn, Flashdance, 28/11/26) : qBittorrent
// (libtorrent 2) les range sous l'empreinte v2 TRONQUÉE, pas sous la v1 que
// D3X calcule depuis le .torrent. torrents/info?hashes=<v1> ne les renvoie
// pas, torrents/files?hash=<v1> répond 404, et recheck ou renameFile ne font
// rien : D3X attendait un ajout qu'il ne voyait jamais (« le client n'a pas
// confirmé l'ajout »). On traduit donc la v1 en identifiant du client, lu dans
// le champ infohash_v1 de la liste complète, et gardé en mémoire.
type hybridIDs struct {
	mu   sync.Mutex
	ids  map[string]string // v1 → identifiant du client
	last time.Time         // dernier relevé complet
	// Relevés rapprochés permis juste après un ajout (une fois par seconde au
	// plus) : le torrent qu'on vient d'ajouter doit être retrouvé tout de suite,
	// sans attendre hybridRefreshEvery.
	burst int
}

// Un relevé complet au plus toutes les hybridRefreshEvery : une empreinte
// absente du client (torrent retiré, simple vérification avant un ajout) ne
// doit pas faire relire toute la liste à chaque appel. Variable pour les tests.
var hybridRefreshEvery = 20 * time.Second

// idOf : l'identifiant du client pour cette empreinte (elle-même si ce n'est
// pas un hybride connu).
func (q *qbitClient) idOf(h string) string {
	h = strings.ToLower(h)
	q.hy.mu.Lock()
	defer q.hy.mu.Unlock()
	if id, ok := q.hy.ids[h]; ok {
		return id
	}
	return h
}

// refreshIDs relit la liste complète du client (au plus toutes les
// hybridRefreshEvery) et retient l'identifiant de chaque hybride. Renvoie
// l'identifiant de h et vrai s'il diffère de h.
func (q *qbitClient) refreshIDs(ctx context.Context, h string) (string, bool) {
	h = strings.ToLower(h)
	q.hy.mu.Lock()
	if id, ok := q.hy.ids[h]; ok {
		q.hy.mu.Unlock()
		return id, true
	}
	since := time.Since(q.hy.last)
	switch {
	case since >= hybridRefreshEvery:
	case q.hy.burst > 0 && since >= time.Second:
		q.hy.burst--
	default:
		q.hy.mu.Unlock()
		return h, false
	}
	q.hy.last = time.Now()
	q.hy.mu.Unlock()
	list, err := q.c.List(ctx, "", nil)
	if err != nil {
		return h, false
	}
	q.hy.mu.Lock()
	defer q.hy.mu.Unlock()
	if q.hy.ids == nil {
		q.hy.ids = map[string]string{}
	}
	for _, t := range list {
		id, v1 := strings.ToLower(t.Hash), strings.ToLower(t.InfoHashV1)
		if v1 != "" && v1 != id {
			q.hy.ids[v1] = id
		}
	}
	id, ok := q.hy.ids[h]
	if !ok {
		return h, false
	}
	return id, true
}

// justAdded : un torrent vient d'être ajouté, les prochains relevés peuvent
// être rapprochés (voir burst).
func (q *qbitClient) justAdded() {
	q.hy.mu.Lock()
	q.hy.burst = 10
	q.hy.mu.Unlock()
}

func isNotFound(err error) bool {
	var he *qbit.HTTPError
	return errors.As(err, &he) && he.Status == http.StatusNotFound
}
