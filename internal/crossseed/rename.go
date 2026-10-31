package crossseed

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/V3x0rV3x/D3X/internal/client"
	"github.com/V3x0rV3x/D3X/internal/config"
	"github.com/V3x0rV3x/D3X/internal/links"
	"github.com/V3x0rV3x/D3X/internal/pathmap"
	"github.com/V3x0rV3x/D3X/internal/torrent"
	"github.com/V3x0rV3x/D3X/internal/v3x"
)

// « Noms différents » : ta copie a les mêmes fichiers que la release V3X
// (mêmes tailles, mêmes données), sous d'autres noms. D3X ajoute la release
// à l'arrêt, renomme ses fichiers DANS LE CLIENT pour qu'ils pointent sur ta
// copie, puis lance la vérification : le torrent ne démarre qu'à 100 %.
// Aucun fichier n'est créé, déplacé ni renommé sur le disque.
//
// Garde-fou : un client qui renomme le fichier d'un torrent le déplace aussi
// sur le disque s'il existe sous l'ancien nom. On refuse donc dès qu'un nom
// de la release V3X pourrait déjà exister : nom d'un autre fichier de ta
// copie, emplacement d'un autre torrent du client, ou fichier présent sur le
// disque quand D3X le voit.

// planRenames associe chaque fichier de la release V3X (v) à un fichier de ta
// copie (local) de même taille : même chemin sous la racine, puis même nom,
// puis nom le plus proche ; à égalité, l'ordre des chemins. La vérification
// du client tranche ensuite : un mauvais appariement reste à l'arrêt.
func planRenames(v, local []torrent.File) ([]client.Rename, error) {
	if len(v) != len(local) {
		return nil, fmt.Errorf("%d fichiers dans la release V3X, %d dans ta copie", len(v), len(local))
	}
	bySize := map[int64][]int{}
	for i, f := range local {
		bySize[f.Size] = append(bySize[f.Size], i)
	}
	type cand struct {
		vi, li int
		score  float64
	}
	var cands []cand
	for vi, f := range v {
		ls := bySize[f.Size]
		if len(ls) == 0 {
			return nil, errors.New("une taille de fichier de la release V3X manque dans ta copie")
		}
		for _, li := range ls {
			cands = append(cands, cand{vi, li, pairScore(f.Path, local[li].Path)})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		if v[cands[i].vi].Path != v[cands[j].vi].Path {
			return v[cands[i].vi].Path < v[cands[j].vi].Path
		}
		return local[cands[i].li].Path < local[cands[j].li].Path
	})
	vUsed, lUsed := make([]bool, len(v)), make([]bool, len(local))
	out := make([]client.Rename, len(v))
	n := 0
	for _, c := range cands {
		if vUsed[c.vi] || lUsed[c.li] {
			continue
		}
		vUsed[c.vi], lUsed[c.li] = true, true
		out[c.vi] = client.Rename{Index: v[c.vi].Index, From: v[c.vi].Path, To: local[c.li].Path}
		n++
	}
	if n != len(v) {
		return nil, errors.New("les tailles des fichiers ne correspondent pas une à une")
	}
	return out, nil
}

// underRoot : le chemin sans son premier élément (la racine du torrent).
func underRoot(p string) string {
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

func base(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

func ext(p string) string {
	b := base(p)
	if i := strings.LastIndexByte(b, '.'); i > 0 {
		return strings.ToLower(b[i:])
	}
	return ""
}

func pairScore(a, b string) float64 {
	s := 0.0
	switch {
	case underRoot(a) == underRoot(b):
		s += 100
	case strings.EqualFold(base(a), base(b)):
		s += 50
	}
	if ext(a) == ext(b) {
		s += 10
	}
	return s + 10*tokenSimilarity(base(a), base(b))
}

func tokens(s string) map[string]bool {
	out := map[string]bool{}
	for _, t := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		out[t] = true
	}
	return out
}

func tokenSimilarity(a, b string) float64 {
	A, B := tokens(a), tokens(b)
	if len(A) == 0 || len(B) == 0 {
		return 0
	}
	n := 0
	for t := range A {
		if B[t] {
			n++
		}
	}
	return float64(n) / float64(len(A)+len(B)-n)
}

// clientJoin joint un dossier vu par le client et un chemin du torrent, avec
// le séparateur du client.
func clientJoin(dir, rel string) string {
	if strings.Contains(dir, `\`) || (len(dir) >= 2 && dir[1] == ':') {
		return strings.TrimRight(dir, `\/`) + `\` + strings.ReplaceAll(rel, "/", `\`)
	}
	return strings.TrimRight(dir, "/") + "/" + rel
}

func samePath(a, b string) bool {
	n := func(p string) string { return strings.TrimRight(strings.ReplaceAll(p, `\`, "/"), "/") }
	return n(a) == n(b) || (strings.Contains(a, `\`) && strings.EqualFold(n(a), n(b)))
}

func under(p, dir string) bool {
	n := func(s string) string { return strings.TrimRight(strings.ReplaceAll(s, `\`, "/"), "/") }
	return samePath(p, dir) || strings.HasPrefix(n(p)+"/", n(dir)+"/")
}

// lookupFunc dit si un chemin (relatif au dossier de rangement) existe sur le
// disque du client ; supported=false : impossible de le savoir.
type lookupFunc func(rel string) (exists, supported bool, err error)

// renameSafe vérifie qu'aucun nom de la release V3X à renommer n'existe déjà,
// ni dans ta copie, ni à l'emplacement d'un autre torrent, ni sur le disque
// (vu par D3X, ou demandé au client). Impossible à vérifier : refus.
// self : l'emplacement de ta copie (les torrents qui le partagent sont les
// mêmes données, d'autres trackers).
func renameSafe(pairs []client.Rename, local []torrent.File, save, self string, all []client.Torrent, lookup lookupFunc) error {
	// Sans la casse : sur un disque Windows ou macOS, « Film.MKV » et
	// « Film.mkv » sont le même fichier.
	localSet := map[string]bool{}
	for _, f := range local {
		localSet[strings.ToLower(f.Path)] = true
	}
	// Dossiers des nouveaux noms : ceux-là existent (c'est ta copie).
	toDirs := map[string]bool{}
	for _, p := range pairs {
		for d := parentRel(p.To); d != ""; d = parentRel(d) {
			toDirs[d] = true
		}
	}
	roots := map[string]bool{}
	var check []string
	seen := map[string]bool{}
	for _, p := range pairs {
		if p.From == p.To {
			continue
		}
		// Casse seule : sur un disque qui distingue la casse, ce serait un
		// autre fichier ; on ne devine pas.
		if strings.EqualFold(p.From, p.To) {
			return errors.New("« " + p.From + " » et « " + p.To + " » ne diffèrent que par les majuscules")
		}
		if localSet[strings.ToLower(p.From)] {
			return errors.New("noms croisés : « " + p.From + " » est déjà le nom d'un autre fichier de ta copie")
		}
		root := p.From
		if i := strings.IndexByte(root, '/'); i >= 0 {
			root = root[:i]
		}
		roots[root] = true
		// À vérifier sur le disque : le fichier, et ses dossiers qui ne sont
		// pas ceux de ta copie (un client peut nettoyer un ancien dossier).
		for x := p.From; x != ""; x = parentRel(x) {
			if toDirs[x] {
				break
			}
			if !seen[x] {
				seen[x] = true
				check = append(check, x)
			}
		}
	}
	for root := range roots {
		target := clientJoin(save, root)
		for _, t := range all {
			if samePath(t.ContentPath, self) {
				continue
			}
			if under(t.ContentPath, target) || under(target, t.ContentPath) {
				return errors.New("« " + root + " » est déjà l'emplacement d'un autre torrent de ton client")
			}
		}
	}
	// Du moins profond au plus profond : un dossier absent dispense de
	// vérifier ce qu'il contiendrait.
	sort.Slice(check, func(i, j int) bool {
		if di, dj := strings.Count(check[i], "/"), strings.Count(check[j], "/"); di != dj {
			return di < dj
		}
		return check[i] < check[j]
	})
	missing := map[string]bool{}
	for _, x := range check {
		skip := false
		for d := parentRel(x); d != ""; d = parentRel(d) {
			if missing[d] {
				skip = true
				break
			}
		}
		if skip {
			missing[x] = true
			continue
		}
		exists, supported, err := lookup(x)
		if err != nil {
			return errors.New("vérification du disque impossible : " + err.Error())
		}
		if !supported {
			return errors.New("D3X ne peut pas vérifier le disque de ton client (nécessaire avant de renommer) : client sur la même machine, qBittorrent 5 ou Deluge, ou indique où D3X voit ce dossier")
		}
		if exists {
			return errors.New("« " + x + " » existe déjà sur le disque")
		}
		missing[x] = true
	}
	return nil
}

func parentRel(p string) string {
	if i := strings.LastIndexByte(p, '/'); i > 0 {
		return p[:i]
	}
	return ""
}

// diskLookup : le disque vu par D3X (même machine, ou partage avec une
// correspondance de chemins), sinon demandé au client.
func diskLookup(ctx context.Context, q client.Client, save string, rules []pathmap.Rule) lookupFunc {
	localDir := pathmap.FromClient(save, rules)
	if st, err := os.Stat(localDir); err == nil && st.IsDir() {
		return func(rel string) (bool, bool, error) {
			_, err := os.Lstat(filepath.Join(localDir, filepath.FromSlash(rel)))
			if err == nil {
				return true, true, nil
			}
			if os.IsNotExist(err) {
				return false, true, nil
			}
			return false, true, err
		}
	}
	return func(rel string) (bool, bool, error) {
		return q.Lookup(ctx, clientJoin(save, rel))
	}
}

// addRenamed : la release V3X ajoutée sous les noms de ta copie, dans le
// client tg qui l'a.
func (e *Engine) addRenamed(ctx context.Context, env *Env, tg Target, r *Row) (string, string) {
	q, cfg := tg.Client, env.Cfg
	link := e.linkerFor(cfg, tg)
	if !q.CanRename() && link == nil {
		return StRenamed, q.Kind() + " ne sait pas renommer les fichiers d'un torrent : ajout impossible (ou active les liens dans les réglages)"
	}
	m := r.Match
	raw, err := env.V3X.DownloadTorrent(ctx, &v3x.Retry{}, m.ID)
	if err != nil {
		return StRenamed, err.Error()
	}
	t, err := torrent.Parse(raw)
	if err != nil {
		return StRenamed, "le .torrent reçu de V3X est illisible"
	}
	if !strings.EqualFold(t.InfoHash, m.InfoHash) {
		return StRenamed, "le .torrent reçu ne correspond pas à la fiche (info-hash différent)"
	}
	if r.sig == nil || t.SizeHash != r.sig.SizeHash {
		return StRenamed, "tailles différentes de ta copie après vérification"
	}
	files, _, err := q.Files(ctx, r.hash)
	if err != nil {
		return StRenamed, "fichiers de ta copie illisibles : " + err.Error()
	}
	var local []torrent.File
	for _, f := range files {
		if !torrent.IsPadPath(f.Path) {
			local = append(local, f)
		}
	}
	if len(local) == 0 {
		return StRenamed, "fichiers de ta copie illisibles"
	}
	root := local[0].Path
	if i := strings.IndexByte(root, '/'); i >= 0 {
		root = root[:i]
	}
	save, ok := saveDir(r.contentPath, root)
	if !ok && len(local) == 1 {
		save, ok = saveDir(r.contentPath, local[0].Path)
	}
	if !ok {
		return StRenamed, "emplacement de ta copie introuvable dans le client"
	}
	pairs, err := planRenames(t.Files, local)
	if err != nil {
		return StRenamed, err.Error()
	}
	all, err := q.List(ctx, nil)
	if err != nil {
		return StRenamed, err.Error()
	}
	if !q.CanRename() {
		return e.addLinked(ctx, tg, cfg, r, link, raw, t, save, pairs, all)
	}
	if err := renameSafe(pairs, local, save, r.contentPath, all, diskLookup(ctx, q, save, tg.PathMap)); err != nil {
		// Renommer serait risqué : les liens physiques, eux, vont dans un
		// dossier à part où rien n'existe encore.
		if link != nil {
			return e.addLinked(ctx, tg, cfg, r, link, raw, t, save, pairs, all)
		}
		return StRenamed, err.Error() + " : ajout à faire à la main"
	}
	hash, n, err := e.addAndRename(ctx, tg, cfg, r.Key, m.ID, raw, save, pairs)
	if err != nil {
		switch {
		case errors.Is(err, ErrInClient):
			return StInClient, ""
		case hash == "":
			return StRenamed, err.Error()
		}
		return StFailed, err.Error()
	}
	return StChecking, fmt.Sprintf("%d nom%s ajusté%s dans le client, rien ne bouge sur le disque", n, plural(n), plural(n))
}

// addLinked : la release V3X rangée dans le dossier des liens, chacun de ses
// fichiers étant un lien physique vers le fichier de même taille de ta copie.
// Ta copie n'est pas touchée et rien n'est écrasé ; si l'ajout échoue, les
// liens sont retirés. Le client vérifie, le torrent ne démarre qu'à 100 %.
func (e *Engine) addLinked(ctx context.Context, tg Target, cfg *config.Config, r *Row, link Linker, raw []byte, t *torrent.Torrent, save string, pairs []client.Rename, all []client.Torrent) (string, string) {
	dir := strings.TrimSpace(tg.LinksDir)
	size := map[int]int64{}
	for _, f := range t.Files {
		size[f.Index] = f.Size
	}
	roots := map[string]bool{}
	lp := make([]links.Pair, 0, len(pairs))
	for _, p := range pairs {
		root := p.From
		if i := strings.IndexByte(root, '/'); i >= 0 {
			root = root[:i]
		}
		roots[root] = true
		lp = append(lp, links.Pair{Src: clientJoin(save, p.To), Dst: clientJoin(dir, p.From), Size: size[p.Index]})
	}
	for root := range roots {
		if occupied(all, clientJoin(dir, root)) {
			return StRenamed, "« " + root + " » est déjà l'emplacement d'un torrent dans ton dossier de liens"
		}
	}
	if _, err := e.linkAndAdd(ctx, tg, WithCategory(cfg, r.category), link, dir, lp, r.Key, r.Match.ID, raw); err != nil {
		var le *LinkError
		switch {
		case errors.Is(err, ErrInClient):
			return StInClient, ""
		case errors.As(err, &le):
			return StRenamed, le.Reason
		}
		return StFailed, err.Error()
	}
	return StChecking, fmt.Sprintf("%d lien%s physique%s dans « %s », ta copie n'est pas touchée", len(lp), plural(len(lp)), plural(len(lp)), dir)
}

func plural(n int) string {
	if n > 1 {
		return "s"
	}
	return ""
}

// addAndRename : ajout à l'arrêt, renommages dans le client, contrôle des
// noms obtenus, puis vérification et surveillance (démarrage à 100 %).
// Un échec après l'ajout retire le torrent (fichiers jamais touchés).
func (e *Engine) addAndRename(ctx context.Context, tg Target, cfg *config.Config, key, id string, raw []byte, save string, pairs []client.Rename) (string, int, error) {
	q := tg.Client
	t, err := torrent.Parse(raw)
	if err != nil {
		return "", 0, errors.New("le .torrent reçu de V3X est illisible")
	}
	hash := strings.ToLower(t.InfoHash)
	if lst, err := q.List(ctx, []string{hash}); err == nil && len(lst) > 0 {
		return hash, 0, ErrInClient
	}
	if err := q.Add(ctx, "[V3X] "+t.Name+".torrent", raw, client.AddOptions{SavePath: save, Category: cfg.Category, Tag: cfg.Tag}); err != nil {
		if errors.Is(err, client.ErrAlreadyThere) {
			return hash, 0, ErrInClient
		}
		return "", 0, err
	}
	undo := func(err error) (string, int, error) {
		_ = q.RemoveKeepFiles(context.Background(), hash)
		return "", 0, err
	}
	// Le client voit-il les mêmes noms d'origine, aux mêmes rangs ?
	var got map[int]string
	for i := 0; i < 30; i++ {
		if files, _, err := q.Files(ctx, hash); err == nil && len(files) > 0 {
			got = map[int]string{}
			for _, f := range files {
				got[f.Index] = f.Path
			}
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if got == nil {
		return undo(errors.New("le client n'a pas confirmé l'ajout"))
	}
	n := 0
	for _, p := range pairs {
		if got[p.Index] != p.From {
			return undo(errors.New("le client ne voit pas les mêmes noms que le .torrent : ajout annulé"))
		}
		if p.From != p.To {
			n++
		}
	}
	if err := q.RenameFiles(ctx, hash, pairs); err != nil {
		return undo(err)
	}
	// Renommages faits ? (certains clients les appliquent en différé)
	ok := false
	for i := 0; i < 40 && !ok; i++ {
		files, _, err := q.Files(ctx, hash)
		if err == nil {
			now := map[int]string{}
			for _, f := range files {
				now[f.Index] = f.Path
			}
			ok = true
			for _, p := range pairs {
				if now[p.Index] != p.To {
					ok = false
					break
				}
			}
		}
		if !ok {
			time.Sleep(500 * time.Millisecond)
		}
	}
	if !ok {
		return undo(errors.New("le client n'a pas appliqué les nouveaux noms : ajout annulé"))
	}
	if err := q.Recheck(ctx, hash); err != nil {
		return hash, n, errors.New("vérification impossible à lancer : " + err.Error())
	}
	e.Watch(tg.ID, key, t.Name, id, hash)
	return hash, n, nil
}
