package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/V3x0rV3x/D3X/internal/config"
	"github.com/V3x0rV3x/D3X/internal/pathmap"
	"github.com/V3x0rV3x/D3X/internal/v3x"
)

// Routes de la publication. La page ne parle jamais au site directement : D3X
// relaie, avec la clé du membre, qu'elle ne voit pas.

func (s *Server) routesPublish() {
	s.mux.HandleFunc("POST /ui/pub/start", s.pubStart)
	s.mux.HandleFunc("GET /ui/pub/context", s.pubContext)
	s.mux.HandleFunc("GET /ui/pub/tmdb/{kind}", s.pubTMDB)
	s.mux.HandleFunc("GET /ui/pub/igdb/{kind}", s.pubIGDB)
	s.mux.HandleFunc("GET /ui/pub/versions", s.pubVersions)
	s.mux.HandleFunc("POST /ui/pub/preview", s.pubPreview)
	s.mux.HandleFunc("POST /ui/pub/image", s.pubImage)
	s.mux.HandleFunc("GET /ui/bbcode.css", s.pubCSS)
	s.mux.HandleFunc("GET /ui/pub/{id}", s.pubGet)
	s.mux.HandleFunc("POST /ui/pub/{id}/cancel", s.pubCancel)
	s.mux.HandleFunc("GET /ui/pub/{id}/file/{index}", s.pubFile)
	s.mux.HandleFunc("POST /ui/pub/{id}/fiche", s.pubFiche)
	s.mux.HandleFunc("POST /ui/pub/{id}/clientdir", s.pubClientDir)
	s.mux.HandleFunc("POST /ui/pub/{id}/client", s.pubClient)
	s.mux.HandleFunc("POST /ui/pub/{id}/links", s.pubLinks)
	s.mux.HandleFunc("POST /ui/pub/{id}/music", s.pubMusic)
	s.mux.HandleFunc("POST /ui/pub/{id}/publish", s.pubPublish)
	s.mux.HandleFunc("POST /ui/pub/{id}/policy", s.pubPolicy)
	s.mux.HandleFunc("POST /ui/pub/{id}/seed-existing", s.pubSeedExisting)
}

// pubSeedExisting : release déjà sur V3X à l'identique, mise en seed avec ces
// fichiers au lieu d'être publiée (publish.SeedExisting).
func (s *Server) pubSeedExisting(w http.ResponseWriter, r *http.Request) {
	if err := s.pub.SeedExisting(r.PathValue("id")); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	j, _ := s.pub.Get(r.PathValue("id"))
	writeJSON(w, http.StatusOK, j)
}

func decode(w http.ResponseWriter, r *http.Request, max int64, v interface{}) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, max)).Decode(v); err != nil {
		fail(w, http.StatusBadRequest, errors.New("requête illisible"))
		return false
	}
	return true
}

// v3xClient : V3X seul (la fiche, TMDB, les demandes n'ont pas besoin du
// client torrent).
func (s *Server) v3xClient() (*v3x.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.v == nil || s.me == nil {
		return nil, errors.New("D3X n'est pas connecté à V3X : vérifie ta clé API dans les réglages")
	}
	return s.v, nil
}

// apiFail transmet une erreur du site avec son code HTTP. Jamais de 401 vers
// la page : pour elle, 401 veut dire « session D3X expirée » (retour
// CoprinusComatus, 20/10/26 : une clé sans le droit « Upload » affichait
// « La session a expiré » sur Publier et Lot au lieu de la vraie raison).
func apiFail(w http.ResponseWriter, err error) {
	var ae *v3x.APIError
	if errors.As(err, &ae) {
		status := ae.Status
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			msg := "V3X refuse : ta clé API doit avoir le droit « Upload » (sur V3X : Paramètres ▸ Intégrations)"
			if status == http.StatusForbidden && ae.Code != "" && ae.Code != "forbidden" && ae.Code != "unauthorized" {
				msg = ae.Error()
			}
			writeJSON(w, http.StatusForbidden, map[string]string{"error": msg, "code": ae.Code})
			return
		}
		writeJSON(w, status, map[string]string{"error": ae.Code})
		return
	}
	fail(w, http.StatusBadGateway, err)
}

func (s *Server) pubStart(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Path   string `json:"path"`
		Source string `json:"source"`
	}
	if !decode(w, r, 1<<14, &in) {
		return
	}
	if strings.TrimSpace(in.Path) == "" {
		fail(w, http.StatusBadRequest, errors.New("choisis un fichier ou un dossier"))
		return
	}
	src, err := s.source(in.Source)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	j, err := s.pub.Start(src, in.Path)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	// Le dossier parent revient au prochain lancement du sélecteur.
	if src.ID() == "local" {
		_ = s.updateConfig(func(c *config.Config) { c.LastDir = filepath.Dir(j.Path) })
	}
	writeJSON(w, http.StatusOK, j)
}

func (s *Server) pubGet(w http.ResponseWriter, r *http.Request) {
	j, ok := s.pub.Get(r.PathValue("id"))
	if !ok {
		fail(w, http.StatusNotFound, errors.New("publication inconnue"))
		return
	}
	writeJSON(w, http.StatusOK, j)
}

func (s *Server) pubCancel(w http.ResponseWriter, r *http.Request) {
	s.pub.Cancel(r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// pubFile sert un morceau d'un fichier de la release (MediaInfo lit le fichier
// par morceaux, dans la page), lu sur ce PC ou sur la seedbox. Rien d'autre
// n'est lisible.
func (s *Server) pubFile(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		fail(w, http.StatusBadRequest, errors.New("fichier inconnu"))
		return
	}
	q := r.URL.Query()
	off, _ := strconv.ParseInt(q.Get("offset"), 10, 64)
	n, _ := strconv.ParseInt(q.Get("size"), 10, 64)
	b, size, err := s.pub.ReadFile(r.Context(), r.PathValue("id"), idx, off, n)
	if err != nil {
		fail(w, http.StatusNotFound, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-File-Size", strconv.FormatInt(size, 10))
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	_, _ = w.Write(b)
}

// publishContext : le contexte du membre (catégories, team, modèles), gardé
// 5 minutes. Pour la page et pour le lot.
func (s *Server) publishContext(ctx context.Context, fresh bool) ([]byte, error) {
	s.mu.Lock()
	cached, at := s.pubCtx, s.pubCtxAt
	s.mu.Unlock()
	if cached != nil && time.Since(at) < 5*time.Minute && !fresh {
		return cached, nil
	}
	v, err := s.v3xClient()
	if err != nil {
		return nil, err
	}
	raw, err := v.PublishContext(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.pubCtx, s.pubCtxAt = raw, time.Now()
	s.mu.Unlock()
	return raw, nil
}

func (s *Server) pubContext(w http.ResponseWriter, r *http.Request) {
	// D3X pas connecté : 400, comme avant ; une erreur du site garde son code.
	if _, err := s.v3xClient(); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	raw, err := s.publishContext(ctx, r.URL.Query().Get("fresh") != "")
	if err != nil {
		apiFail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(raw)
}

func (s *Server) pubTMDB(w http.ResponseWriter, r *http.Request) {
	v, err := s.v3xClient()
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	raw, err := v.TMDB(ctx, r.PathValue("kind"), r.URL.Query())
	if err != nil {
		apiFail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(raw)
}

// pubIGDB relaie la recherche de jeux du site (Nayr, 22/10/26) : la clé de la
// base jeux reste sur V3X.
func (s *Server) pubIGDB(w http.ResponseWriter, r *http.Request) {
	v, err := s.v3xClient()
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	raw, err := v.IGDB(ctx, r.PathValue("kind"), r.URL.Query())
	if err != nil {
		apiFail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(raw)
}

// pubVersions relaie les versions déjà sur V3X du même contenu, et les REPACK
// de la release (Nico31, Turias, quebecman, 23/10/26).
func (s *Server) pubVersions(w http.ResponseWriter, r *http.Request) {
	v, err := s.v3xClient()
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	raw, err := v.Versions(ctx, r.URL.Query())
	if err != nil {
		apiFail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(raw)
}

func (s *Server) pubFiche(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ReleaseName  string  `json:"releaseName"`
		MediaInfo    string  `json:"mediainfo"`
		TmdbRef      *string `json:"tmdbRef"`
		MbRef        *string `json:"mbRef"`
		IgdbID       *int64  `json:"igdbId"`
		BookRef      *string `json:"bookRef"`
		CategorySlug *string `json:"categorySlug"`
		TemplateID   *string `json:"templateId"`
	}
	if !decode(w, r, 4<<20, &in) {
		return
	}
	dl, ok := s.pub.Download(r.PathValue("id"))
	if !ok {
		fail(w, http.StatusNotFound, errors.New("publication inconnue"))
		return
	}
	v, err := s.v3xClient()
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	raw, err := v.Fiche(ctx, v3x.FicheRequest{ReleaseName: in.ReleaseName, MediaInfo: in.MediaInfo, TmdbRef: in.TmdbRef, MbRef: in.MbRef, IgdbID: in.IgdbID, BookRef: in.BookRef, Download: dl, CategorySlug: in.CategorySlug, TemplateID: in.TemplateID})
	if err != nil {
		apiFail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(raw)
}

func (s *Server) pubPreview(w http.ResponseWriter, r *http.Request) {
	var in struct {
		BBCode string `json:"bbcode"`
	}
	if !decode(w, r, 1<<20, &in) {
		return
	}
	v, err := s.v3xClient()
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	html, err := v.Preview(ctx, in.BBCode)
	if err != nil {
		apiFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"html": html})
}

// pubImage héberge sur V3X une image choisie dans la page (pochette, capture),
// avec la clé du membre. Arrive en JSON (base64) : le serveur local n'accepte
// que du JSON en écriture, ce qui le protège des formulaires d'autres sites.
func (s *Server) pubImage(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
		Data string `json:"data"`
	}
	if !decode(w, r, 24<<20, &in) {
		return
	}
	raw, err := base64.StdEncoding.DecodeString(in.Data)
	if err != nil || len(raw) == 0 {
		fail(w, http.StatusBadRequest, errors.New("image illisible"))
		return
	}
	if len(raw) > 16<<20 {
		fail(w, http.StatusRequestEntityTooLarge, errors.New("image trop lourde pour V3X (16 Mio au plus)"))
		return
	}
	if !strings.HasPrefix(http.DetectContentType(raw), "image/") {
		fail(w, http.StatusBadRequest, errors.New("ce fichier n'est pas une image (JPEG, PNG, GIF ou WebP)"))
		return
	}
	name := filepath.Base(strings.TrimSpace(in.Name))
	if name == "." || name == string(filepath.Separator) || name == "" {
		name = "image"
	}
	v, err := s.v3xClient()
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	u, err := v.UploadImage(ctx, name, raw)
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": u})
}

// pubCSS relaie la feuille de style du rendu BBCode du site (gardée 1 h).
func (s *Server) pubCSS(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	css, at := s.bbCSS, s.bbCSSAt
	s.mu.Unlock()
	if css == nil || time.Since(at) > time.Hour {
		if v, err := s.v3xClient(); err == nil {
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			if b, err := v.BBCodeCSS(ctx); err == nil {
				css = b
				s.mu.Lock()
				s.bbCSS, s.bbCSSAt = b, time.Now()
				s.mu.Unlock()
			}
			cancel()
		}
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Write(css)
}

func (s *Server) pubClientDir(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Dir      string `json:"dir"`
		Remember bool   `json:"remember"`
	}
	if !decode(w, r, 1<<14, &in) {
		return
	}
	rule, cid, err := s.pub.SetClientDir(r.PathValue("id"), in.Dir)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	// Retenue dans les dossiers du client qui seedera la release.
	if in.Remember && rule != nil {
		err := s.updateConfig(func(c *config.Config) {
			cc := c.Client(cid)
			if cc == nil {
				return
			}
			rules := []pathmap.Rule{*rule}
			for _, x := range cc.PathMap {
				if x.From != rule.From {
					rules = append(rules, x)
				}
			}
			cc.PathMap = rules
		})
		if err != nil {
			fail(w, http.StatusInternalServerError, errors.New("dossier non retenu : "+err.Error()))
			return
		}
	}
	s.pubGet(w, r)
}

// pubClient : le client qui seedera la release, choisi par le membre.
func (s *Server) pubClient(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Client string `json:"client"`
	}
	if !decode(w, r, 1<<12, &in) {
		return
	}
	if err := s.pub.SetClient(r.PathValue("id"), in.Client); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	s.pubGet(w, r)
}

// pubLinks : la release publiée part par liens (dossier de liens du client)
// ou sur ses fichiers d'origine, au choix du membre jusqu'à l'envoi.
func (s *Server) pubLinks(w http.ResponseWriter, r *http.Request) {
	var in struct {
		On bool `json:"on"`
	}
	if !decode(w, r, 1<<12, &in) {
		return
	}
	if err := s.pub.SetLinks(r.PathValue("id"), in.On); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	s.pubGet(w, r)
}

// pubMusic bascule la release en album ou en vidéo (publish.SetMusic).
func (s *Server) pubMusic(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Music bool `json:"music"`
	}
	if !decode(w, r, 1<<12, &in) {
		return
	}
	if err := s.pub.SetMusic(r.PathValue("id"), in.Music); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	s.pubGet(w, r)
}

func (s *Server) pubPublish(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Fields map[string]string `json:"fields"`
	}
	if !decode(w, r, 4<<20, &in) {
		return
	}
	// Champs acceptés : ceux de la page d'upload du site, rien d'autre.
	fields := publishFields(in.Fields)
	if fields["categoryId"] == "" {
		fail(w, http.StatusBadRequest, errors.New("choisis une catégorie"))
		return
	}
	if err := s.publishGuarded(r.PathValue("id"), fields); err != nil {
		fail(w, http.StatusConflict, err)
		return
	}
	s.pubGet(w, r)
}
