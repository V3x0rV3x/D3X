package server

import (
	"context"
	"crypto/ed25519"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/V3x0rV3x/D3X/internal/update"
)

// Update annonce une version plus récente, prête ou non à être installée.
type Update struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	Ready   bool   `json:"ready"`             // téléchargée ET vérifiée (signature, empreinte)
	Error   string `json:"error,omitempty"`   // téléchargement ou vérification refusés
	Managed string `json:"managed,omitempty"` // « docker » : à mettre à jour par l'image
}

// Repo est le dépôt public de D3X.
const Repo = update.Repo

// SetUpdateKey donne la clé publique qui signe les versions.
func (s *Server) SetUpdateKey(pub ed25519.PublicKey) {
	s.mu.Lock()
	s.pubKey = pub
	s.mu.Unlock()
}

// pageSilence : une fenêtre réduite ne ralentit ses signaux qu'à un par
// minute.
const pageSilence = 3 * time.Minute

// PageGone dit si la page a été fermée : elle a dit au revoir sans revenir
// depuis 8 s (un rechargement revient tout de suite), ou elle ne donne plus
// signe de vie depuis 3 min. Faux tant que la page n'est jamais venue. Le
// lot ne dépend plus de la page : il tourne dans D3X (voir Busy).
func (s *Server) PageGone() bool {
	gone, _ := s.pageGone()
	return gone
}

// GoneWhy : pourquoi la page passe pour partie, pour le journal.
func (s *Server) GoneWhy() string {
	_, why := s.pageGone()
	return why
}

func (s *Server) pageGone() (bool, string) {
	last, bye := s.lastSeen.Load(), s.byeAt.Load()
	if last == 0 {
		return false, ""
	}
	now := time.Now().UnixNano()
	if bye > 0 && bye >= last && now-bye > int64(8*time.Second) {
		return true, "la page a dit au revoir"
	}
	quiet := time.Duration(now - last)
	return quiet > pageSilence, "page muette depuis " + quiet.Round(time.Second).String()
}

// LotAdvancing : un lot tourne et avance (pas en attente d'une page pour le
// MediaInfo). Windows ne met pas le PC en veille pendant ce temps.
func (s *Server) LotAdvancing() bool {
	return s.lot.Running() && s.miPage.Pending() == nil
}

// StopLot arrête le lot proprement (icône « Quitter », docker stop).
func (s *Server) StopLot() { s.lot.Stop() }

// Resumed : l'ordinateur sort de veille. La page s'est tue pendant la veille
// sans être fermée : elle a de nouveau 3 min pour se manifester. Chez karat
// (24/10/26), l'écran éteint mettait le PC en veille légère ; au réveil, D3X
// prenait ce silence pour une fenêtre fermée et s'arrêtait en plein lot. Une
// page qui a dit au revoir reste partie.
func (s *Server) Resumed() {
	last, bye := s.lastSeen.Load(), s.byeAt.Load()
	if last == 0 || (bye > 0 && bye >= last) {
		return
	}
	s.lastSeen.CompareAndSwap(last, time.Now().UnixNano())
}

// PageSeen dit si la page a déjà appelé D3X depuis son lancement.
func (s *Server) PageSeen() bool { return s.lastSeen.Load() != 0 }

// ping : signal de présence de la page, qui dit aussi si elle est vraiment à
// l'écran (onglet visible et image dessinée). Chez Luci (21/10/26), la
// première fenêtre chargeait sa page sans jamais s'afficher (absente de la
// barre des tâches) : une fois une autre fenêtre affichée, celle-là reçoit
// « close » et se ferme d'elle-même.
func (s *Server) ping(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" || len(id) > 32 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	shown := r.URL.Query().Get("shown") == "1"
	s.pagesMu.Lock()
	if s.pages == nil {
		s.pages = map[string]bool{}
	}
	before, known := s.pages[id]
	if len(s.pages) < 64 || known {
		s.pages[id] = before || shown
	}
	other := false
	for k, v := range s.pages {
		if k != id && v {
			other = true
		}
	}
	s.pagesMu.Unlock()
	if !known {
		if shown {
			log.Println("page de D3X affichée")
		} else {
			log.Println("page de D3X chargée mais pas affichée")
		}
	} else if shown && !before {
		log.Println("page de D3X affichée")
	}
	if !shown && !before && other {
		writeJSON(w, http.StatusOK, map[string]bool{"close": true})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PageShown : une page au moins est à l'écran.
func (s *Server) PageShown() bool {
	s.pagesMu.Lock()
	defer s.pagesMu.Unlock()
	for _, v := range s.pages {
		if v {
			return true
		}
	}
	return false
}

// PageHidden : des pages ont parlé, aucune n'est à l'écran.
func (s *Server) PageHidden() bool {
	s.pagesMu.Lock()
	defer s.pagesMu.Unlock()
	if len(s.pages) == 0 {
		return false
	}
	for _, v := range s.pages {
		if v {
			return false
		}
	}
	return true
}

// PageClosed : la page a dit au revoir (fenêtre fermée) et rien n'est revenu
// depuis 8 s. Contrairement au simple silence (veille de l'ordinateur), c'est
// sûr : ce qui reste de la fenêtre est un fantôme qu'on peut fermer.
func (s *Server) PageClosed() bool {
	last, bye := s.lastSeen.Load(), s.byeAt.Load()
	return bye > 0 && bye >= last && time.Now().UnixNano()-bye > int64(8*time.Second)
}

// Busy dit si D3X travaille (ajouts, vérifications, publications, lot qui
// avance) : pas de redémarrage ni d'arrêt à ce moment-là. Un lot en attente
// d'une page pour le MediaInfo n'occupe pas D3X.
func (s *Server) Busy() bool { return s.engine.Busy() || s.pub.Busy() || s.LotAdvancing() }

// updateEvery : une version sortie dans la soirée n'était vue que six heures
// plus tard (retour de V3x0r, 20/10/26).
const updateEvery = time.Hour

// updateLoop cherche une nouvelle version au lancement puis toutes les heures,
// la télécharge et la vérifie. Elle n'est installée qu'au redémarrage (bouton
// de la page ou prochain lancement), jamais au milieu d'un travail.
func (s *Server) updateLoop() {
	if s.Version == "dev" || s.Version == "" {
		return
	}
	time.Sleep(5 * time.Second)
	for {
		s.mu.Lock()
		enabled, pub := s.cfg.CheckUpdates, s.pubKey
		s.mu.Unlock()
		if enabled {
			s.checkUpdate(pub)
		}
		time.Sleep(updateEvery)
	}
}

func (s *Server) checkUpdate(pub ed25519.PublicKey) {
	s.lastUpdateCheck.Store(time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	rel, err := update.Latest(ctx, s.Version)
	if err != nil || !update.Newer(rel.Version, s.Version) {
		return
	}
	s.stageRelease(ctx, pub, rel)
}

// stageRelease annonce la version, puis la télécharge et la vérifie (sauf en
// Docker, où l'image se met à jour à part). Une version déjà prête n'est pas
// retéléchargée à chaque passage, et deux préparations ne tournent jamais en
// même temps.
func (s *Server) stageRelease(ctx context.Context, pub ed25519.PublicKey, rel *update.Release) {
	s.mu.Lock()
	if cur := s.update; cur != nil && cur.Version == rel.Version && cur.Ready {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	if !s.updateStaging.CompareAndSwap(false, true) {
		return
	}
	defer s.updateStaging.Store(false)
	u := &Update{Version: rel.Version, URL: rel.URL, Managed: s.opts.Managed}
	s.mu.Lock()
	s.update = u
	s.mu.Unlock()
	if pub == nil || s.opts.Managed != "" {
		return
	}
	done := &Update{Version: u.Version, URL: u.URL, Managed: u.Managed}
	if p, err := update.Stage(ctx, pub, rel); err != nil {
		done.Error = err.Error()
		log.Printf("mise à jour %s non préparée : %v", rel.Version, err)
	} else {
		done.Ready = true
		log.Printf("mise à jour %s prête : %s", rel.Version, p)
	}
	s.mu.Lock()
	s.update = done
	s.mu.Unlock()
}

// checkNow : bouton « Vérifier » à côté du numéro de version. Une recherche au
// plus par minute (l'API de GitHub limite les appels sans compte). La réponse
// part dès que la dernière version est connue ; si elle est plus récente, son
// téléchargement et sa vérification continuent en arrière-plan.
func (s *Server) checkNow(w http.ResponseWriter, r *http.Request) {
	out := map[string]interface{}{}
	if s.Version != "dev" && s.Version != "" && time.Since(time.Unix(0, s.lastUpdateCheck.Load())) >= time.Minute {
		s.lastUpdateCheck.Store(time.Now().UnixNano())
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		rel, err := update.Latest(ctx, s.Version)
		cancel()
		if err != nil {
			fail(w, http.StatusBadGateway, errors.New("GitHub ne répond pas, réessaie dans un moment"))
			return
		}
		out["latest"] = rel.Version
		if update.Newer(rel.Version, s.Version) {
			s.mu.Lock()
			pub := s.pubKey
			s.mu.Unlock()
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
				defer cancel()
				s.stageRelease(ctx, pub, rel)
			}()
			// Annoncée tout de suite (« téléchargement en cours ») si elle ne l'est pas déjà.
			s.mu.Lock()
			if s.update == nil || s.update.Version != rel.Version {
				s.update = &Update{Version: rel.Version, URL: rel.URL, Managed: s.opts.Managed}
			}
			s.mu.Unlock()
		}
	}
	s.mu.Lock()
	out["version"], out["update"] = s.Version, s.update
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

// Restart est fermé quand la page demande d'installer la mise à jour : main
// remplace le binaire, relance D3X sur la même session et s'arrête.
func (s *Server) RestartRequested() <-chan struct{} {
	s.restartMu.Lock()
	defer s.restartMu.Unlock()
	return s.restart
}

// RestartFailed : main n'a pas pu installer la mise à jour. Le bouton refait
// une demande (avant, un premier échec le rendait muet), la page montre la
// raison et la version est retéléchargée en arrière-plan.
func (s *Server) RestartFailed(err error) {
	s.restartMu.Lock()
	s.restart, s.restartOnce = make(chan struct{}), &sync.Once{}
	s.restartMu.Unlock()
	s.restage("installation impossible : " + err.Error())
}

// restage : la version prête n'est plus utilisable (fichier disparu, échec de
// l'installation) ; on la retélécharge, la page dit pourquoi en attendant.
func (s *Server) restage(why string) {
	s.mu.Lock()
	if s.update != nil {
		u := *s.update
		u.Ready, u.Error = false, why
		s.update = &u
	}
	pub := s.pubKey
	s.mu.Unlock()
	if s.Version == "dev" || s.Version == "" {
		return // version de développement : jamais de téléchargement
	}
	go s.checkUpdate(pub)
}

func (s *Server) restartNow(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	u := s.update
	s.mu.Unlock()
	if u == nil || !u.Ready {
		fail(w, http.StatusConflict, errors.New("aucune mise à jour vérifiée à installer"))
		return
	}
	if s.Busy() {
		fail(w, http.StatusConflict, errors.New("D3X travaille (ajouts, vérification ou publication) : réessaie quand c'est fini"))
		return
	}
	if !update.Staged() {
		p, _ := update.StagedPath()
		log.Printf("mise à jour %s introuvable à côté de D3X (%s) : nouveau téléchargement", u.Version, p)
		s.restage("le fichier de la mise à jour a disparu, nouveau téléchargement en cours")
		fail(w, http.StatusConflict, errors.New("le fichier de la mise à jour a disparu : D3X la retélécharge, réessaie dans une minute"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	s.restartMu.Lock()
	once, ch := s.restartOnce, s.restart
	s.restartMu.Unlock()
	once.Do(func() { close(ch) })
}
