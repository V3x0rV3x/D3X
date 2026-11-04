package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/V3x0rV3x/D3X/internal/batch"
	"github.com/V3x0rV3x/D3X/internal/crossseed"
	"github.com/V3x0rV3x/D3X/internal/journal"
	"github.com/V3x0rV3x/D3X/internal/mediainfo"
	"github.com/V3x0rV3x/D3X/internal/releases"
	"github.com/V3x0rV3x/D3X/internal/source"
)

// Mode lot : chaque entrée d'un dossier (sous-dossier ou fichier) est une
// release. Le lot tourne dans D3X (internal/batch) ; la page le démarre,
// l'arrête et le suit. Le serveur liste les releases, garde les décisions et
// relaie le MediaInfo qu'une page calcule quand le binaire manque.

type batchEntry struct {
	releases.Entry
	Last *journal.Event `json:"last,omitempty"` // dernière décision ou publication
	// Tags : les étiquettes des torrents du client qui couvrent la release ;
	// Skipped : l'une d'elles est à ne jamais publier (Réglages ▸ Ajouts).
	Tags    []string `json:"tags,omitempty"`
	Skipped bool     `json:"skipped,omitempty"`
}

// clientLists : la liste de chaque client, tous en même temps (au plus le
// délai du contexte, et celui du client : 2 min pour qBittorrent) ; warn nomme
// ceux qui n'ont pas répondu (leurs étiquettes manquent) ; tagged : au moins
// un client est réglé (sans client, la page n'a pas de colonne « Étiquettes »
// à montrer). V3X n'est pas requis.
func (s *Server) clientLists(ctx context.Context) (lists []crossseed.Listed, warn string, tagged bool) {
	targets, down := s.targets()
	if len(targets) == 0 && len(down) == 0 {
		return nil, "", false
	}
	lists, down = crossseed.ListAll(ctx, &crossseed.Env{Targets: targets, Down: down})
	if len(down) > 0 {
		warn = "Étiquettes non lues : " + crossseed.DownText(down)
	}
	return lists, warn, true
}

// lotTags : les étiquettes des torrents qui couvrent chaque chemin, la
// correspondance de dossiers devinée pour les clients sans règle (comme Publier).
func lotTags(src source.Source, paths []string, lists []crossseed.Listed) map[string][]string {
	return crossseed.TagsOf(paths, crossseed.GuessRules(lists, paths, src.Dir))
}

// lotEntries : les releases pour la page, chacune avec sa dernière décision,
// les étiquettes des torrents qui la couvrent et « écartée » si l'une est à ne
// jamais publier. Les clients sont lus pendant la lecture du dossier (list),
// qui peut prendre des minutes par SSH. Rend la réponse sans « path ».
func (s *Server) lotEntries(ctx context.Context, src source.Source, list func() ([]releases.Entry, error)) (map[string]interface{}, error) {
	type clients struct {
		lists  []crossseed.Listed
		warn   string
		tagged bool
	}
	// Les clients, pendant la lecture du dossier ; 45 s au plus : un client qui
	// ne répond pas est dit (« Étiquettes non lues »), la page n'attend pas, et
	// le démarrage du lot relit les étiquettes avec son propre délai.
	lctx, lcancel := context.WithTimeout(ctx, 45*time.Second)
	defer lcancel()
	cc := make(chan clients, 1)
	go func() {
		l, w, tg := s.clientLists(lctx)
		cc <- clients{l, w, tg}
	}()
	entries, err := list()
	if err != nil {
		return nil, err
	}
	c := <-cc
	paths := make([]string, len(entries))
	for i, e := range entries {
		paths[i] = e.Path
	}
	tags := lotTags(src, paths, c.lists)
	s.mu.Lock()
	skip := s.cfg.SkipTagSet()
	skipList := append([]string{}, s.cfg.SkipTags...)
	s.mu.Unlock()
	last := journal.LastByPath()
	out := make([]batchEntry, 0, len(entries))
	for _, e := range entries {
		be := batchEntry{Entry: e, Tags: tags[e.Path]}
		be.Skipped = batch.SkippedBy(be.Tags, skip) != ""
		if ev, ok := last[src.Key(e.Path)]; ok {
			ev := ev
			be.Last = &ev
		}
		out = append(out, be)
	}
	return map[string]interface{}{"entries": out, "skipTags": skipList, "tagged": c.tagged, "tagsWarn": c.warn}, nil
}

func (s *Server) routesBatch() {
	s.mux.HandleFunc("GET /ui/batch/list", s.batchList)
	s.mux.HandleFunc("POST /ui/batch/decision", s.batchDecision)
	s.mux.HandleFunc("POST /ui/batch/start", s.batchStart)
	s.mux.HandleFunc("POST /ui/batch/stop", s.batchStop)
	s.mux.HandleFunc("GET /ui/batch/state", s.batchState)
	s.mux.HandleFunc("POST /ui/pub/{id}/mediainfo", s.pubMediaInfo)
	s.routesPrepared()
	s.routesDiscogs()
}

// errRestarted : demande de lot faite avant le dernier lancement de D3X.
var errRestarted = errors.New("D3X a redémarré depuis cette demande : le lot interrompu ne repart pas seul. Recharge la page, vérifie les releases cochées, puis relance le lot")

// batchStart démarre le lot dans D3X : la page envoie les releases cochées
// et les options, puis suit l'état. Un seul lot à la fois.
func (s *Server) batchStart(w http.ResponseWriter, r *http.Request) {
	// Refusé tout de suite : pas de liste des releases (jusqu'à 5 min par
	// SSH) pour rien. Le moteur revérifie au démarrage.
	if s.lot.Running() {
		fail(w, http.StatusConflict, batch.ErrRunning)
		return
	}
	var in struct {
		batch.Spec
		Boot string `json:"boot"`
	}
	if !decode(w, r, 4<<20, &in) {
		return
	}
	spec := in.Spec
	// Un lot ne part que sur un clic fait pour CE lancement de D3X : une
	// demande d'avant un redémarrage (renvoyée par le navigateur ou un proxy,
	// page restée sur l'ancien état) est refusée. Le README le promet : un lot
	// interrompu ne repart pas seul, même après un redémarrage du conteneur
	// (bertho-zero, ticket n°3, 27/10/26 : « lot démarré » une minute après
	// chaque docker restart).
	if in.Boot != s.boot {
		fail(w, http.StatusConflict, errRestarted)
		return
	}
	if len(spec.Paths) == 0 {
		fail(w, http.StatusBadRequest, errors.New("aucune release cochée"))
		return
	}
	if err := s.ready(); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	run, err := s.lot.Start(spec)
	if err != nil {
		if errors.Is(err, batch.ErrRunning) {
			fail(w, http.StatusConflict, err)
		} else {
			fail(w, http.StatusBadRequest, err)
		}
		return
	}
	log.Println("lot démarré :", run.Total, "releases")
	writeJSON(w, http.StatusOK, s.lotState(run))
}

func (s *Server) batchStop(w http.ResponseWriter, r *http.Request) {
	s.lot.Stop()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// lotState : le run tel que la page l'affiche, « paused » et la demande de
// MediaInfo en plus. Le champ State déclaré ici masque celui de *batch.Run
// dans le JSON (règle de profondeur d'encoding/json) : c'est voulu.
func (s *Server) lotState(run *batch.Run) interface{} {
	if run == nil {
		return nil
	}
	out := struct {
		*batch.Run
		State           string             `json:"state"`
		MediaInfoWanted *mediainfo.Request `json:"mediainfoWanted,omitempty"`
	}{Run: run, State: run.State}
	if run.State == batch.StRunning {
		if p := s.miPage.Pending(); p != nil {
			out.State, out.MediaInfoWanted = "paused", p
		}
	}
	return out
}

func (s *Server) batchState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.lotState(s.lot.State()))
}

// pubMediaInfo : une page rend le MediaInfo demandé par le lot.
func (s *Server) pubMediaInfo(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text string `json:"text"`
	}
	if !decode(w, r, 4<<20, &in) {
		return
	}
	if !s.miPage.Deliver(r.PathValue("id"), in.Text) {
		fail(w, http.StatusConflict, errors.New("aucun MediaInfo attendu pour cette publication"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) batchList(w http.ResponseWriter, r *http.Request) {
	dir := strings.TrimSpace(r.URL.Query().Get("path"))
	if dir == "" {
		fail(w, http.StatusBadRequest, errors.New("choisis un dossier"))
		return
	}
	src, err := s.source(r.URL.Query().Get("source"))
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	s.mu.Lock()
	exclude := s.cfg.ExcludeList()
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	out, err := s.lotEntries(ctx, src, func() ([]releases.Entry, error) { return src.List(ctx, dir, exclude) })
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	out["path"] = dir
	writeJSON(w, http.StatusOK, out)
}

// batchDecision garde la décision du lot pour une release (à revoir, simulée,
// ignorée, déjà présente) : elle revient à la prochaine liste.
func (s *Server) batchDecision(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Path   string `json:"path"`
		Source string `json:"source"`
		Name   string `json:"name"`
		Kind   string `json:"kind"`
		Detail string `json:"detail"`
		ID     string `json:"id"`
	}
	if !decode(w, r, 1<<16, &in) {
		return
	}
	switch in.Kind {
	case "batch_review", "batch_simulated", "batch_ignored", "batch_unignored", "batch_duplicate", "batch_error", "batch_reset":
	default:
		fail(w, http.StatusBadRequest, errors.New("décision inconnue"))
		return
	}
	src, err := s.source(in.Source)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	key := src.Key(in.Path)
	// Remise en attente ou écartée : la fiche gardée pour le lot ne sert plus.
	if in.Kind == "batch_reset" || in.Kind == "batch_ignored" {
		dropPrepared(key)
	}
	journal.Add(journal.Event{Kind: in.Kind, Name: in.Name, Path: key, Detail: in.Detail, ID: in.ID})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
