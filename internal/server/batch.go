package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/V3x0rV3x/D3X/internal/batch"
	"github.com/V3x0rV3x/D3X/internal/journal"
	"github.com/V3x0rV3x/D3X/internal/mediainfo"
	"github.com/V3x0rV3x/D3X/internal/releases"
)

// Mode lot : chaque entrée d'un dossier (sous-dossier ou fichier) est une
// release. Le lot tourne dans D3X (internal/batch) ; la page le démarre,
// l'arrête et le suit. Le serveur liste les releases, garde les décisions et
// relaie le MediaInfo qu'une page calcule quand le binaire manque.

type batchEntry struct {
	releases.Entry
	Last *journal.Event `json:"last,omitempty"` // dernière décision ou publication
}

func (s *Server) routesBatch() {
	s.mux.HandleFunc("GET /ui/batch/list", s.batchList)
	s.mux.HandleFunc("POST /ui/batch/decision", s.batchDecision)
	s.mux.HandleFunc("POST /ui/batch/hold", s.batchHold)
	s.mux.HandleFunc("POST /ui/batch/start", s.batchStart)
	s.mux.HandleFunc("POST /ui/batch/stop", s.batchStop)
	s.mux.HandleFunc("GET /ui/batch/state", s.batchState)
	s.mux.HandleFunc("POST /ui/pub/{id}/mediainfo", s.pubMediaInfo)
	s.routesPrepared()
	s.routesDiscogs()
}

// batchStart démarre le lot dans D3X : la page envoie les releases cochées
// et les options, puis suit l'état. Un seul lot à la fois.
func (s *Server) batchStart(w http.ResponseWriter, r *http.Request) {
	// Refusé tout de suite : pas de liste des releases (jusqu'à 5 min par
	// SSH) pour rien. Le moteur revérifie au démarrage.
	if s.lot.Running() {
		fail(w, http.StatusConflict, batch.ErrRunning)
		return
	}
	var spec batch.Spec
	if !decode(w, r, 4<<20, &spec) {
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
	list, err := src.List(ctx, dir, exclude)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	last := journal.LastByPath()
	out := make([]batchEntry, 0, len(list))
	for _, e := range list {
		be := batchEntry{Entry: e}
		if ev, ok := last[src.Key(e.Path)]; ok {
			ev := ev
			be.Last = &ev
		}
		out = append(out, be)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"path": dir, "entries": out})
}

// batchHold : bail « Démarrer le seed à la fin du lot » (voir
// crossseed.HoldPublished), renouvelé par la page tant que le lot tourne ;
// 0 le lève.
func (s *Server) batchHold(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Seconds int `json:"seconds"`
	}
	if !decode(w, r, 1<<10, &in) {
		return
	}
	s.engine.HoldPublished(time.Duration(in.Seconds) * time.Second)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
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
