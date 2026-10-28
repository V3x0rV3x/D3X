package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/V3x0rV3x/D3X/internal/batch"
	"github.com/V3x0rV3x/D3X/internal/releases"
	"github.com/V3x0rV3x/D3X/internal/v3x"
)

// Règles de réupload de V3X appliquées à TOUTE publication, quelle que soit
// l'origine de la release (dossier parcouru, « Pas sur V3X », Médiathèque,
// fiche gardée, Publier ouvert à la main) : une team qui interdit le réupload
// ou le soumet à son accord (sans te l'avoir donné), un groupe refusé sur V3X,
// et le detag de l'une d'elles, que V3X ne peut pas voir (Ekhstaz, 27/10/26).
// Le Lot vérifie avant de hacher, Publier le dit dès l'ouverture, et la
// publication elle-même refuse en dernier recours (publishGuarded).

// policyIndex : les règles de V3X rangées pour la recherche, motifs des tags
// refusés compilés une fois (un dossier voisin peut compter des milliers de
// noms).
type policyIndex struct {
	banned   []v3x.TeamPolicy
	bannedRe []*regexp.Regexp
	byTag    map[string]policyHit
}

func indexPolicies(list []v3x.TeamPolicy) policyIndex {
	idx := policyIndex{byTag: map[string]policyHit{}}
	for _, t := range list {
		if t.Policy == "banned" {
			idx.banned = append(idx.banned, t)
			idx.bannedRe = append(idx.bannedRe, bannedPattern(t.Name))
			continue
		}
		if t.Allowed {
			continue
		}
		for _, tag := range t.Tags {
			idx.byTag[strings.ToLower(tag)] = policyHit{Team: t.Name, Policy: t.Policy}
		}
	}
	return idx
}

// of : comme policyOf, avec les motifs déjà compilés.
func (idx policyIndex) of(name string) (policyHit, bool) {
	base := tagBase(name)
	for i, re := range idx.bannedRe {
		if re != nil && re.MatchString(base) {
			b := idx.banned[i]
			return policyHit{Team: b.Name, Policy: "banned", Reason: b.Reason}, true
		}
	}
	if g := releaseGroup(name); g != "" {
		if h, ok := idx.byTag[strings.ToLower(g)]; ok {
			return h, true
		}
	}
	if t := lastToken(name); t != "" {
		if h, ok := idx.byTag[strings.ToLower(t)]; ok {
			return h, true
		}
	}
	return policyHit{}, false
}

// copySig : une copie que D3X connaît (son nom, sa taille, ses fichiers).
type copySig struct {
	name  string
	size  int64
	files int
}

// findPolicy : la règle qui vise une release de ces noms, d'abord par son tag,
// puis comme detag : même taille à l'octet près et même nombre de fichiers
// qu'une copie taguée connue (au-delà de detagMinSize, comme detagHits).
func findPolicy(idx policyIndex, names []string, size int64, files int, copies []copySig) (policyHit, bool) {
	own := map[string]bool{}
	for _, n := range names {
		if strings.TrimSpace(n) == "" {
			continue
		}
		own[n] = true
		if h, ok := idx.of(n); ok {
			return h, true
		}
	}
	if size < detagMinSize {
		return policyHit{}, false
	}
	for _, c := range copies {
		if c.size != size || c.files != files || own[c.name] {
			continue
		}
		if h, ok := idx.of(c.name); ok {
			h.Detag = c.name
			return h, true
		}
	}
	return policyHit{}, false
}

// label : la pastille de « Pas sur V3X » (app.js, même texte).
func (h policyHit) label() string {
	kind := "Réupload sur accord"
	switch h.Policy {
	case "banned":
		kind = "Groupe refusé sur V3X"
	case "forbidden":
		kind = "Réupload interdit"
	}
	pre := ""
	if h.Detag != "" {
		pre = "Detag · "
	}
	return pre + kind + " · " + h.Team
}

// motive : la raison en clair, du point de vue du membre.
func (h policyHit) motive() string {
	of := ""
	if h.Detag != "" {
		of = "copie sans le tag de « " + h.Detag + " » (même taille au fichier près) ; "
	}
	switch h.Policy {
	case "banned":
		why := ""
		if h.Reason != "" {
			why = " (" + h.Reason + ")"
		}
		return of + "le groupe " + h.Team + " est refusé sur V3X" + why + ", D3X ne publie pas ses releases"
	case "forbidden":
		return of + "la team " + h.Team + " n'autorise pas le réupload de ses releases, D3X ne la publie pas"
	}
	return of + "la team " + h.Team + " demande son accord avant un réupload : demande-le sur sa page de V3X, puis relance la publication"
}

// knownCopies : les copies que D3X connaît dans tes clients torrent (toutes
// les lignes de la dernière analyse, avec les copies fusionnées dans une
// ligne). La Médiathèque ne garde pas la liste de ses fichiers après son
// analyse : elle n'en fait pas partie.
func (s *Server) knownCopies() []copySig {
	rows := s.engine.Rows()
	out := make([]copySig, 0, len(rows))
	for _, r := range rows {
		out = append(out, copySig{r.Name, r.Size, r.Files})
		for _, a := range r.Alts {
			if b := path.Base(strings.ReplaceAll(strings.TrimRight(a.Path, `/\`), `\`, "/")); b != "" && b != "." && b != r.Name {
				out = append(out, copySig{b, r.Size, r.Files})
			}
		}
	}
	return out
}

// siblingDirMax : dossiers voisins tagués parcourus au plus, par vérification
// (un fichier voisin, lui, se compare à sa taille sans rien parcourir).
const siblingDirMax = 50

// siblingCopies : les copies taguées voisines de même taille et même nombre
// de fichiers, dans le même dossier de ce PC (Ekhstaz avait la copie taguée
// et le detag côte à côte). Seules les voisines dont le nom porte une règle
// sont mesurées : jamais tout le dossier.
func siblingCopies(idx policyIndex, dir string, exclude []string, size int64, files int) []copySig {
	if dir == "" {
		return nil
	}
	list, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []copySig
	dirs := 0
	for _, e := range list {
		if _, ok := idx.of(e.Name()); !ok {
			continue
		}
		if !e.IsDir() {
			if files != 1 {
				continue
			}
			if info, err := e.Info(); err == nil && info.Size() == size {
				out = append(out, copySig{e.Name(), size, 1})
			}
			continue
		}
		if dirs >= siblingDirMax {
			continue
		}
		dirs++
		en := releases.EntryOf(filepath.Join(dir, e.Name()), exclude)
		if en.Error == "" && en.Size == size && en.Files == files {
			out = append(out, copySig{en.Name, en.Size, en.Files})
		}
	}
	return out
}

// releasePolicy : la règle qui interdit de publier cette release, nil sinon.
// dir : son dossier sur ce PC, dont les voisines taguées comptent comme
// copies ("" ailleurs, sur la seedbox). Une règle trouvée dans la liste
// gardée en mémoire est revérifiée sur une liste fraîche (accord donné par
// la team depuis, par exemple).
func (s *Server) releasePolicy(ctx context.Context, names []string, size int64, files int, extra []copySig, dir string) (*policyHit, error) {
	v, err := s.v3xClient()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	exclude := s.cfg.ExcludeList()
	s.mu.Unlock()
	known := append(s.knownCopies(), extra...)
	find := func(list []v3x.TeamPolicy) (policyHit, bool) {
		idx := indexPolicies(list)
		copies := known
		if size >= detagMinSize {
			copies = append(append([]copySig(nil), known...), siblingCopies(idx, dir, exclude, size, files)...)
		}
		return findPolicy(idx, names, size, files, copies)
	}
	list, err := s.teamPolicies(ctx, v)
	if err != nil {
		return nil, err
	}
	h, ok := find(list)
	if !ok {
		return nil, nil
	}
	s.polC.mu.Lock()
	stale := time.Since(s.polC.at) > time.Minute
	if stale {
		s.polC.list = nil
	}
	s.polC.mu.Unlock()
	if stale {
		if list, err = s.teamPolicies(ctx, v); err != nil {
			return nil, err
		}
		if h, ok = find(list); !ok {
			return nil, nil
		}
	}
	return &h, nil
}

// jobPolicy : la règle qui vise une publication de Publier (nom choisi, nom du
// dossier ou du fichier, taille et nombre de fichiers du job).
func (s *Server) jobPolicy(ctx context.Context, id, releaseName string) (*policyHit, error) {
	j, ok := s.pub.Get(id)
	if !ok || j == nil {
		return nil, errors.New("publication inconnue")
	}
	names := []string{releaseName, j.Name, path.Base(strings.ReplaceAll(j.Path, `\`, "/"))}
	dir := ""
	if _, err := os.Stat(j.Path); err == nil {
		dir = filepath.Dir(j.Path)
	}
	return s.releasePolicy(ctx, names, j.Total, len(j.Files), nil, dir)
}

// publishGuarded : la seule porte vers la publication (Publier comme le Lot).
// Une release sous une règle de réupload ne part jamais, même si la page ou le
// lot ne l'avaient pas vue.
func (s *Server) publishGuarded(id string, fields map[string]string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h, err := s.jobPolicy(ctx, id, fields["releaseName"])
	if err != nil {
		return errors.New("règles de réupload des teams illisibles, rien n'est publié : " + err.Error())
	}
	if h != nil {
		return errors.New(h.label() + " : " + h.motive())
	}
	return s.pub.Publish(id, fields)
}

// pubPolicy : pour Publier, la règle qui bloque la publication (avertissement
// dès l'ouverture, et à chaque changement du nom de release).
func (s *Server) pubPolicy(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !decode(w, r, 1<<12, &in) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	h, err := s.jobPolicy(ctx, r.PathValue("id"), in.Name)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"stop": nil, "error": err.Error()})
		return
	}
	if h == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"stop": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"stop": map[string]string{"label": h.label(), "motive": h.motive()}})
}

// Policy (batch.Deps) : la règle d'une release du lot, les autres releases du
// lot comptant comme copies taguées possibles.
func (d batchDeps) Policy(ctx context.Context, q batch.PolicyQuery) (*batch.PolicyStop, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	extra := make([]copySig, 0, len(q.Others))
	for _, o := range q.Others {
		extra = append(extra, copySig{o.Name, o.Size, o.Files})
	}
	dir := ""
	if q.Local && q.Path != "" {
		dir = filepath.Dir(q.Path)
	}
	h, err := d.s.releasePolicy(ctx, q.Names, q.Size, q.Files, extra, dir)
	if err != nil || h == nil {
		return nil, err
	}
	return &batch.PolicyStop{Label: h.label(), Motive: h.motive()}, nil
}
