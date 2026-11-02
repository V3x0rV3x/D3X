package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
)

// SetOpener donne à D3X le moyen d'ouvrir un lien dans le navigateur par
// défaut de la machine. Seulement en local : la page y tourne forcément sur la
// même machine que D3X (127.0.0.1).
func (s *Server) SetOpener(open func(string) error) {
	s.mu.Lock()
	s.opener = open
	s.mu.Unlock()
}

// openLink : un lien externe cliqué dans la fenêtre de D3X (Edge ou Chrome en
// mode application, avec un profil à part) s'ouvrait dans ce navigateur-là, et
// non dans celui du membre (AntoineAngel, 2/11/26). La page le confie ici, et
// D3X l'ouvre dans le navigateur par défaut. Liens http et https seulement.
func (s *Server) openLink(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	open := s.opener
	s.mu.Unlock()
	if open == nil || s.opts.Remote {
		fail(w, http.StatusNotFound, errors.New("ouverture de lien indisponible"))
		return
	}
	var in struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, errors.New("lien illisible"))
		return
	}
	u, err := url.Parse(in.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(in.URL) > 4096 {
		fail(w, http.StatusBadRequest, errors.New("lien refusé"))
		return
	}
	if err := open(u.String()); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
