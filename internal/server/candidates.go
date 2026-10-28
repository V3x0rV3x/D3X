package server

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/V3x0rV3x/D3X/internal/crossseed"
	"github.com/V3x0rV3x/D3X/internal/journal"
	"github.com/V3x0rV3x/D3X/internal/names"
	"github.com/V3x0rV3x/D3X/internal/pathmap"
	"github.com/V3x0rV3x/D3X/internal/source"
	"github.com/V3x0rV3x/D3X/internal/v3x"
)

// « Pas sur V3X » : ce que tu seedes ailleurs et qui manque sur V3X. Chaque
// release se publie d'un clic ou passe en lot, et celles qui répondent à une
// demande ouverte sont signalées avec leur prime.

type requestsCache struct {
	mu   sync.Mutex
	at   time.Time
	list []v3x.Request
}

// policiesCache : teams dont le réupload n'est pas libre (30 min).
type policiesCache struct {
	mu   sync.Mutex
	at   time.Time
	list []v3x.TeamPolicy
}

func (s *Server) routesCandidates() {
	s.mux.HandleFunc("POST /ui/rows/local", s.rowsLocal)
	s.mux.HandleFunc("GET /ui/requests/match", s.requestsMatch)
	s.mux.HandleFunc("GET /ui/teams/policies", s.teamPoliciesMatch)
	s.mux.HandleFunc("POST /ui/batch/entries", s.batchEntries)
	s.mux.HandleFunc("POST /ui/requests/propose", s.requestsPropose)
	s.mux.HandleFunc("GET /ui/requests/for", s.requestsFor)
}

// requestsFor : les demandes ouvertes auxquelles répond un nom de release
// (après une publication, pour la proposer).
func (s *Server) requestsFor(w http.ResponseWriter, r *http.Request) {
	v, err := s.v3xClient()
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	reqs, err := s.requests(ctx, v)
	if err != nil {
		apiFail(w, err)
		return
	}
	hits := matchRequest(r.URL.Query().Get("name"), reqs)
	if hits == nil {
		hits = []requestHit{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"requests": hits})
}

// requestsPropose : propose une release qu'on vient de publier pour une
// demande. Le demandeur la valide sur le site.
func (s *Server) requestsPropose(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RequestID string `json:"requestId"`
		TorrentID string `json:"torrentId"`
	}
	if !decode(w, r, 1<<12, &in) {
		return
	}
	v, err := s.v3xClient()
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := v.Propose(ctx, in.RequestID, in.TorrentID); err != nil {
		apiFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// requests : les demandes ouvertes, relues toutes les 30 minutes.
func (s *Server) requests(ctx context.Context, v *v3x.Client) ([]v3x.Request, error) {
	s.reqC.mu.Lock()
	defer s.reqC.mu.Unlock()
	if s.reqC.list != nil && time.Since(s.reqC.at) < 30*time.Minute {
		return s.reqC.list, nil
	}
	list, err := v.Requests(ctx)
	if err != nil {
		return nil, err
	}
	s.reqC.list, s.reqC.at = list, time.Now()
	return list, nil
}

type requestHit struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Year   string `json:"year,omitempty"`
	Reward int    `json:"reward"`
	Mine   bool   `json:"mine,omitempty"`
}

// matchRequest : les demandes auxquelles une release répond (même titre, même
// année quand les deux la donnent, même type quand la demande le dit).
func matchRequest(name string, reqs []v3x.Request) []requestHit {
	rel := names.Parse(name)
	if rel.Title == "" {
		return nil
	}
	relS, relE := names.Episode(name)
	var out []requestHit
	for _, q := range reqs {
		title, ty := names.RequestTitle(q.Title)
		year := q.Year
		if year == "" {
			year = ty
		}
		sim := names.Similarity(title, rel.Title)
		if sim < 0.85 {
			continue
		}
		if year != "" && rel.Year != "" && year != rel.Year {
			continue
		}
		// Sans année d'un côté : le titre doit être exactement le même.
		if (year == "") != (rel.Year == "") && sim < 1 {
			continue
		}
		if q.MediaType == "tv" && !rel.TV || q.MediaType == "movie" && rel.TV {
			continue
		}
		// Une autre saison (ou un autre épisode) ne remplit pas la demande.
		if qs, qe := names.Episode(q.Title); qs >= 0 && relS >= 0 && qs != relS || qe >= 0 && relE >= 0 && qe != relE {
			continue
		}
		out = append(out, requestHit{ID: q.ID, Title: q.Title, Year: year, Reward: q.Reward, Mine: q.Mine})
	}
	return out
}

func (s *Server) requestsMatch(w http.ResponseWriter, r *http.Request) {
	v, err := s.v3xClient()
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	reqs, err := s.requests(ctx, v)
	if err != nil {
		apiFail(w, err)
		return
	}
	out := map[string][]requestHit{}
	for _, row := range s.engine.Rows() {
		if row.Status != crossseed.StNone {
			continue
		}
		if hits := matchRequest(row.Name, reqs); len(hits) > 0 {
			out[row.Key] = hits
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"byKey": out, "open": len(reqs)})
}

type localPath struct {
	Path   string `json:"path,omitempty"`
	Source string `json:"source,omitempty"`
	Error  string `json:"error,omitempty"`
}

// resolve : où D3X voit une release d'un client (ce PC, ou la seedbox quand
// le client y tourne) ; rules : la correspondance de dossiers de ce client.
func (s *Server) resolve(ctx context.Context, rules []pathmap.Rule, clientPath string) localPath {
	s.mu.Lock()
	ssh := s.ssh
	s.mu.Unlock()
	local := pathmap.FromClient(clientPath, rules)
	if (source.Local{}).Exists(ctx, local) {
		return localPath{Path: local, Source: "local"}
	}
	if ssh != nil && strings.HasPrefix(clientPath, "/") {
		if (source.Remote{C: ssh}).Exists(ctx, clientPath) {
			return localPath{Path: clientPath, Source: "ssh"}
		}
	}
	dir := clientPath
	if i := strings.LastIndexAny(strings.TrimRight(dir, `/\`), `/\`); i > 0 {
		dir = dir[:i]
	}
	return localPath{Error: "D3X ne voit pas « " + dir + " » : dis-lui où il voit ce dossier (Réglages ▸ Dossiers du client), ou règle ta seedbox"}
}

func (s *Server) rowsLocal(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Keys []string `json:"keys"`
	}
	if !decode(w, r, 1<<20, &in) {
		return
	}
	want := map[string]bool{}
	for _, k := range in.Keys {
		want[k] = true
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	out := map[string]localPath{}
	for _, row := range s.engine.Rows() {
		if want[row.Key] && row.Path != "" {
			out[row.Key] = s.resolveCopies(ctx, append([]crossseed.RowCopy{{Client: row.Client, Path: row.Path}}, row.Alts...))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// resolveCopies : la première copie que D3X sait lire. Les copies d'origine
// passent avant celles d'un dossier de liens d'outil de cross-seed : c'est
// sur elles que la release publiée partira en seed (exodius19, 21/10/26 :
// D3X ne lisait pas /VIDEO/cross-seed-links/…, l'original était ailleurs).
func (s *Server) resolveCopies(ctx context.Context, copies []crossseed.RowCopy) localPath {
	sort.SliceStable(copies, func(i, j int) bool { return !crossSeedDir(copies[i].Path) && crossSeedDir(copies[j].Path) })
	var first localPath
	for i, c := range copies {
		var rules []pathmap.Rule
		s.mu.Lock()
		if cc := s.cfg.Client(c.Client); cc != nil {
			rules = cc.PathMap
		}
		s.mu.Unlock()
		lp := s.resolve(ctx, rules, c.Path)
		if lp.Path != "" {
			return lp
		}
		if i == 0 {
			first = lp
		}
	}
	return first
}

// crossSeedDir : le chemin passe par un dossier de liens d'outil de cross-seed.
func crossSeedDir(p string) bool {
	l := strings.ToLower(strings.ReplaceAll(p, `\`, "/"))
	for _, k := range []string{"cross-seed", "crossseed", "cross_seed"} {
		if strings.Contains(l, k) {
			return true
		}
	}
	return false
}

// batchEntries : un lot fait de releases choisies (au lieu d'un dossier).
func (s *Server) batchEntries(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Paths  []string `json:"paths"`
		Source string   `json:"source"`
	}
	if !decode(w, r, 1<<20, &in) {
		return
	}
	if len(in.Paths) == 0 {
		fail(w, http.StatusBadRequest, errors.New("aucune release choisie"))
		return
	}
	src, err := s.source(in.Source)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	s.mu.Lock()
	exclude := s.cfg.ExcludeList()
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	list, err := src.Entries(ctx, in.Paths, exclude)
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
	writeJSON(w, http.StatusOK, map[string]interface{}{"path": "", "entries": out})
}

func (s *Server) teamPolicies(ctx context.Context, v *v3x.Client) ([]v3x.TeamPolicy, error) {
	s.polC.mu.Lock()
	defer s.polC.mu.Unlock()
	if s.polC.list != nil && time.Since(s.polC.at) < 30*time.Minute {
		return s.polC.list, nil
	}
	list, err := v.TeamPolicies(ctx)
	if err != nil {
		return nil, err
	}
	s.polC.list, s.polC.at = list, time.Now()
	return list, nil
}

type policyHit struct {
	Team   string `json:"team"`             // la team, ou le tag refusé
	Policy string `json:"policy"`           // forbidden, ask, banned
	Reason string `json:"reason,omitempty"` // banned : la raison donnée par V3X
	// Detag : la release n'a pas ce tag dans son nom, mais une copie taguée de
	// la même taille au fichier près est chez toi (son nom) : c'est la même
	// release, détaguée ou renommée (Ekhstaz, 27/10/26).
	Detag string `json:"detag,omitempty"`
}

// detagMinSize : en dessous, deux releases différentes peuvent avoir la même
// taille par hasard ; au-dessus, une taille identique à l'octet près désigne
// la même release (même seuil que les doublons de taille de V3X).
const detagMinSize = 100 << 20

// policyOf : la règle de V3X qui vise ce nom (groupe refusé, puis team qui
// interdit le réupload ou le soumet à son accord), d'après son tag de fin.
func policyOf(name string, banned []v3x.TeamPolicy, byTag map[string]policyHit) (policyHit, bool) {
	if b := bannedTagOf(name, banned); b != nil {
		return policyHit{Team: b.Name, Policy: "banned", Reason: b.Reason}, true
	}
	if g := releaseGroup(name); g != "" {
		if h, ok := byTag[strings.ToLower(g)]; ok {
			return h, true
		}
	}
	// Tag séparé par un espace ou un point (« … x264 ac3 mHDgz.mkv ») :
	// seulement s'il est celui d'une team qui a une règle.
	if t := lastToken(name); t != "" {
		if h, ok := byTag[strings.ToLower(t)]; ok {
			return h, true
		}
	}
	return policyHit{}, false
}

// lastToken : le dernier mot du nom, sans extension ni titre entre
// parenthèses en fin de nom.
func lastToken(name string) string {
	base := trailingTitle.ReplaceAllString(strings.TrimSpace(name), "")
	base = fileExt.ReplaceAllString(base, "")
	parts := tagParts.Split(base, -1)
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			return parts[i]
		}
	}
	return ""
}

// detagHits : les releases « Pas sur V3X » sans règle propre, de même taille
// et même nombre de fichiers qu'une copie qui en a une (taguée, dans
// n'importe quel onglet) : un detag ou un renommage de cette release garde
// sa règle. Les clés des lignes déjà réglées (hits) ne bougent pas.
func detagHits(rows []crossseed.Row, hits map[string]policyHit, banned []v3x.TeamPolicy, byTag map[string]policyHit) {
	type tagged struct {
		hit  policyHit
		name string
	}
	type sig struct {
		size  int64
		files int
	}
	src := map[sig]tagged{}
	for _, row := range rows {
		if row.Size < detagMinSize {
			continue
		}
		k := sig{row.Size, row.Files}
		if _, seen := src[k]; seen {
			continue
		}
		if h, ok := policyOf(row.Name, banned, byTag); ok {
			src[k] = tagged{h, row.Name}
		}
	}
	for _, row := range rows {
		if row.Status != crossseed.StNone || row.Size < detagMinSize {
			continue
		}
		if _, done := hits[row.Key]; done {
			continue
		}
		if t, ok := src[sig{row.Size, row.Files}]; ok && t.name != row.Name {
			h := t.hit
			h.Detag = t.name
			hits[row.Key] = h
		}
	}
}

// teamPoliciesMatch : pour chaque release « Pas sur V3X », la team de son tag
// de groupe quand elle interdit le réupload ou le soumet à son accord, et que
// le membre n'en fait pas partie (demande d'Ekhstaz, 21/10/26).
func (s *Server) teamPoliciesMatch(w http.ResponseWriter, r *http.Request) {
	v, err := s.v3xClient()
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	list, err := s.teamPolicies(ctx, v)
	if err != nil {
		apiFail(w, err)
		return
	}
	idx := indexPolicies(list)
	banned, byTag := idx.banned, idx.byTag
	out := map[string]policyHit{}
	rows := s.engine.Rows()
	for _, row := range rows {
		if row.Status != crossseed.StNone {
			continue
		}
		// Groupe refusé sur V3X (liste du staff, 22/10/26) avant tout le reste,
		// puis la team du tag.
		if h, ok := policyOf(row.Name, banned, byTag); ok {
			out[row.Key] = h
		}
	}
	detagHits(rows, out, banned, byTag)
	writeJSON(w, http.StatusOK, map[string]interface{}{"byKey": out})
}

var (
	trailingTitle = regexp.MustCompile(`\s*\([^()]*\)\s*$`)
	groupToken    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_]{1,19}$`)
	notAGroup     = regexp.MustCompile(`^(?i:\d+p?|raws|subs|dl|web|hdtv|x264|x265|h264|h265|hevc|avc|aac|ac3|dts|multi|french|vff|vfq|vostfr)$`)
	fileExt       = regexp.MustCompile(`(?i)\.(mkv|mp4|avi|m2ts|ts|iso|epub|pdf|cbz|cbr|mp3|flac|zip|rar|7z|torrent)$`)
	tagParts      = regexp.MustCompile(`[^A-Za-z0-9]+`)
)

// bannedTagOf : le tag refusé qui termine ce nom (« …x264-DreadTeam »,
// « …-Dread-Team.mkv », « …[TOXIC] »), comme le site (lib/banned-tags.ts) :
// après un tiret ou entre crochets, séparateurs internes du tag facultatifs.
func bannedTagOf(name string, list []v3x.TeamPolicy) *v3x.TeamPolicy {
	base := tagBase(name)
	for i := range list {
		if re := bannedPattern(list[i].Name); re != nil && re.MatchString(base) {
			return &list[i]
		}
	}
	return nil
}

// tagBase : le nom sans titre entre parenthèses en fin ni extension.
func tagBase(name string) string {
	base := trailingTitle.ReplaceAllString(strings.TrimSpace(name), "")
	return fileExt.ReplaceAllString(base, "")
}

// bannedPattern : le motif d'un tag refusé (nil s'il est vide).
func bannedPattern(tag string) *regexp.Regexp {
	var q []string
	for _, p := range tagParts.Split(tag, -1) {
		if p != "" {
			q = append(q, regexp.QuoteMeta(p))
		}
	}
	if len(q) == 0 {
		return nil
	}
	re, err := regexp.Compile(`(?i)(?:-\s*|\[)` + strings.Join(q, `[-._ ]?`) + `\]?$`)
	if err != nil {
		return nil
	}
	return re
}

// releaseGroup : le tag de groupe en fin de nom (« …x265-TEAM »), comme le site
// (lib/release-quality.ts) ; vide s'il n'y en a pas.
func releaseGroup(name string) string {
	n := trailingTitle.ReplaceAllString(name, "")
	for _, ext := range []string{".mkv", ".mp4", ".avi", ".torrent"} {
		if strings.HasSuffix(strings.ToLower(n), ext) {
			n = n[:len(n)-len(ext)]
		}
	}
	i := strings.LastIndex(n, "-")
	if i < 0 || i == len(n)-1 {
		return ""
	}
	tail := strings.TrimSpace(n[i+1:])
	if !groupToken.MatchString(tail) || notAGroup.MatchString(tail) {
		return ""
	}
	return tail
}
