package crossseed

import (
	"bytes"
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/V3x0rV3x/D3X/internal/client"
	"github.com/V3x0rV3x/D3X/internal/config"
	"github.com/V3x0rV3x/D3X/internal/links"
	"github.com/V3x0rV3x/D3X/internal/pathmap"
	"github.com/V3x0rV3x/D3X/internal/torrent"
	"github.com/V3x0rV3x/D3X/internal/v3x"
)

// Médiathèque : des fichiers rangés par une médiathèque (renommés par un
// serveur multimédia ou un gestionnaire de téléchargements, sans le .torrent
// d'origine) retrouvés sur V3X par leur taille. Seules les tailles des gros fichiers partent vers le site, jamais
// un nom ni un chemin. Chaque candidate est vérifiée sur le disque, fichier
// par fichier et morceau par morceau, avant d'être proposée ; l'ajout suit la
// règle habituelle (à l'arrêt, vérifié par le client, démarré à 100 %), de
// préférence par liens physiques pour ne jamais toucher la médiathèque.

const (
	libMaxCandidates = 300 // .torrent lus au plus par analyse
	libPiecesPerFile = 3   // morceaux vérifiés par fichier
)

// libMinSize : tailles envoyées au site, gros fichiers seulement (le site
// refuse en dessous de 50 Mio).
var libMinSize int64 = 50 << 20

// LibFile : un fichier de la médiathèque, chemin vu par D3X.
type LibFile struct {
	Path string
	Size int64
}

// LibraryIO : l'accès aux fichiers de la médiathèque (ce PC ou la seedbox).
type LibraryIO interface {
	Files(ctx context.Context, src, dir string) ([]LibFile, error)
	ReadAt(ctx context.Context, src, path string, off, n int64) ([]byte, error)
	// ClientPath : le chemin vu par le client qui reçoit l'ajout, dont rules
	// est la correspondance de dossiers.
	ClientPath(src, path string, rules []pathmap.Rule) string
}

// LibRow : une release V3X retrouvée dans la médiathèque.
type LibRow struct {
	Key     string   `json:"key"`
	Name    string   `json:"name"`
	Size    int64    `json:"size"`
	Files   int      `json:"files"`
	Folder  string   `json:"folder,omitempty"` // où sont ses fichiers (vu par D3X)
	Source  string   `json:"source"`
	Status  string   `json:"status"` // add, partial, different, in_client, queued, checking, seeding, failed
	Detail  string   `json:"detail,omitempty"`
	Missing []string `json:"missing,omitempty"`
	// MissBytes (« À compléter ») : ce que le client téléchargera, les
	// morceaux qui contiennent les fichiers manquants.
	MissBytes int64      `json:"missBytes,omitempty"`
	Progress  float64    `json:"progress,omitempty"`
	Match     *v3x.Match `json:"match"`

	raw   []byte
	pairs []libPair
	// minProgress : vérification attendue pour une release à compléter (les
	// morceaux des fichiers manquants en moins) ; 0 = 100 % exigé.
	minProgress float64
}

type libPair struct {
	v     torrent.File
	local LibFile
}

// Statuts propres à la médiathèque.
const (
	StLibPartial   = "partial"   // il manque des fichiers de la release
	StLibDifferent = "different" // mêmes tailles, autres données
	// StLibComplete : il ne manque que des petits fichiers annexes (.nfo,
	// image, texte, sous-titre), que le client peut télécharger avant de
	// seeder (MiTor, 23/10/26).
	StLibComplete = "completable"
)

// Fichiers annexes qu'une release « À compléter » peut télécharger.
var sideFileExt = map[string]bool{".nfo": true, ".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".bmp": true, ".txt": true, ".sfv": true, ".md5": true, ".url": true, ".srt": true, ".ass": true, ".ssa": true, ".sub": true, ".idx": true}

// À compléter : 20 Mio de fichiers annexes au plus, et 64 Mio de morceaux à
// télécharger au plus (un morceau qui touche un .nfo se télécharge en entier).
const (
	libCompleteMaxFiles  = 20 << 20
	libCompleteMaxPieces = 64 << 20
)

// completable : les fichiers manquants ne sont que des annexes, et ce que le
// client devra télécharger reste petit. Renvoie les octets à télécharger et
// la vérification attendue (en fraction du total).
func completable(t *torrent.Torrent, missing []torrent.File) (int64, float64, bool) {
	var size int64
	for _, f := range missing {
		if !sideFileExt[strings.ToLower(path.Ext(f.Path))] {
			return 0, 0, false
		}
		size += f.Size
	}
	if size > libCompleteMaxFiles || t.PieceLength <= 0 || t.Total <= 0 {
		return 0, 0, false
	}
	pieces := map[int64]bool{}
	for _, f := range missing {
		if f.Size <= 0 {
			continue
		}
		for i := f.Offset / t.PieceLength; i <= (f.Offset+f.Size-1)/t.PieceLength; i++ {
			pieces[i] = true
		}
	}
	var bytes int64
	for i := range pieces {
		bytes += min(t.PieceLength, t.Total-i*t.PieceLength)
	}
	if bytes > libCompleteMaxPieces || bytes >= t.Total {
		return 0, 0, false
	}
	return bytes, 1 - float64(bytes)/float64(t.Total), true
}

type library struct {
	io       LibraryIO
	scan     Progress
	rows     map[string]*LibRow
	queue    []string
	applying bool
	cancel   context.CancelFunc
}

// SetLibraryIO branche l'accès aux fichiers de la médiathèque.
func (e *Engine) SetLibraryIO(io LibraryIO) {
	e.mu.Lock()
	e.lib.io = io
	e.mu.Unlock()
}

func (e *Engine) libScanning() bool {
	p := e.lib.scan.Phase
	return p == "list" || p == "read" || p == "match"
}

// StartLibrary lance l'analyse des dossiers de la médiathèque.
func (e *Engine) StartLibrary(folders []config.LibFolder) error {
	e.mu.Lock()
	if e.libScanning() {
		e.mu.Unlock()
		return errors.New("une analyse de la médiathèque est déjà en cours")
	}
	if e.lib.applying {
		e.mu.Unlock()
		return errors.New("des ajouts sont en cours : attends qu'ils soient finis")
	}
	if e.lib.io == nil {
		e.mu.Unlock()
		return errors.New("médiathèque indisponible")
	}
	if len(folders) == 0 {
		e.mu.Unlock()
		return errors.New("ajoute d'abord un dossier de ta médiathèque")
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.lib.cancel = cancel
	e.lib.scan = Progress{Phase: "list"}
	e.mu.Unlock()
	go func() {
		err := e.runLibrary(ctx, folders)
		e.mu.Lock()
		defer e.mu.Unlock()
		if err != nil {
			e.lib.scan.Phase, e.lib.scan.Error = "error", err.Error()
		} else {
			e.lib.scan.Phase, e.lib.scan.Detail = "done", ""
		}
		e.lib.scan.At = time.Now()
		e.touch()
	}()
	return nil
}

// StopLibrary interrompt l'analyse ou les ajouts de la médiathèque.
func (e *Engine) StopLibrary() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.lib.cancel != nil {
		e.lib.cancel()
	}
	e.lib.queue = nil
}

func (e *Engine) libProgress(phase string, done, total int, detail string) {
	e.mu.Lock()
	e.lib.scan.Phase, e.lib.scan.Done, e.lib.scan.Total, e.lib.scan.Detail = phase, done, total, detail
	e.mu.Unlock()
}

type libLocal struct {
	LibFile
	src string
}

func (e *Engine) runLibrary(ctx context.Context, folders []config.LibFolder) error {
	e.retry(ctx)
	env, err := e.clients()
	if err != nil {
		return err
	}
	v := env.V3X
	e.mu.Lock()
	io := e.lib.io
	e.mu.Unlock()

	// 1. Les fichiers de la médiathèque.
	var all []libLocal
	bySize := map[int64][]int{}
	for i, f := range folders {
		e.libProgress("list", i, len(folders), f.Path)
		list, err := io.Files(ctx, f.Source, f.Path)
		if err != nil {
			return fmt.Errorf("« %s » : %w", f.Path, err)
		}
		for _, x := range list {
			bySize[x.Size] = append(bySize[x.Size], len(all))
			all = append(all, libLocal{x, f.Source})
		}
	}
	var sizes []int64
	for s := range bySize {
		if s >= libMinSize {
			sizes = append(sizes, s)
		}
	}
	sort.Slice(sizes, func(i, j int) bool { return sizes[i] > sizes[j] })

	// 2. Les releases V3X qui ont un fichier de ces tailles, hors celles déjà
	// dans l'un des clients.
	known := map[string]bool{}
	lists, down := ListAll(ctx, env)
	if ctx.Err() != nil {
		return errors.New("analyse arrêtée")
	}
	if len(lists) == 0 {
		return errors.New(DownText(down))
	}
	e.mu.Lock()
	e.lib.scan.Warn = DownText(down)
	e.mu.Unlock()
	for _, l := range lists {
		for _, t := range l.List {
			known[strings.ToLower(t.Hash)] = true
			for _, h := range t.Hashes {
				known[strings.ToLower(h)] = true
			}
		}
	}
	// Déjà proposées par le cross-seed (une copie dans le client) : pas ici.
	crossIDs := map[string]bool{}
	for _, r := range e.Rows() {
		if r.Match != nil {
			crossIDs[r.Match.ID] = true
		}
	}
	type cand struct {
		m    v3x.FileMatch
		hits int
	}
	cands := map[string]*cand{}
	retry := &v3x.Retry{}
	for i := 0; i < len(sizes); i += v3x.MatchFilesSize {
		end := i + v3x.MatchFilesSize
		if end > len(sizes) {
			end = len(sizes)
		}
		e.libProgress("match", i, len(sizes), "")
		res, err := v.MatchFiles(ctx, retry, sizes[i:end])
		if err != nil {
			return err
		}
		for _, list := range res {
			for _, m := range list {
				if known[strings.ToLower(m.InfoHash)] || crossIDs[m.ID] {
					continue
				}
				if c := cands[m.ID]; c != nil {
					c.hits++
				} else {
					cands[m.ID] = &cand{m: m, hits: 1}
				}
			}
		}
	}
	// Les plus prometteuses d'abord (le plus de fichiers trouvés), puis les
	// moins seedées : ce sont elles qui ont besoin de toi.
	order := make([]*cand, 0, len(cands))
	for _, c := range cands {
		order = append(order, c)
	}
	sort.Slice(order, func(i, j int) bool {
		a, b := order[i], order[j]
		ra, rb := float64(a.hits)/float64(max(1, a.m.FileCount)), float64(b.hits)/float64(max(1, b.m.FileCount))
		if ra != rb {
			return ra > rb
		}
		return a.m.Seeders < b.m.Seeders
	})
	if len(order) > libMaxCandidates {
		order = order[:libMaxCandidates]
	}

	// 3. Chaque candidate : son .torrent, tous ses fichiers, des morceaux lus.
	rows := map[string]*LibRow{}
	for i, c := range order {
		if ctx.Err() != nil {
			return errors.New("analyse arrêtée")
		}
		e.libProgress("read", i, len(order), c.m.Name)
		m := &v3x.Match{Kind: "library", ID: c.m.ID, Name: c.m.Name, InfoHash: c.m.InfoHash, Seeders: c.m.Seeders, Leechers: c.m.Leechers, Size: c.m.Size}
		r := &LibRow{Key: "lib:" + c.m.ID, Name: c.m.Name, Size: c.m.Size, Files: c.m.FileCount, Match: m}
		rows[r.Key] = r
		raw, err := v.DownloadTorrent(ctx, &v3x.Retry{}, c.m.ID)
		if err != nil {
			r.Status, r.Detail = StError, err.Error()
			continue
		}
		t, err := torrent.Parse(raw)
		if err != nil || !strings.EqualFold(t.InfoHash, c.m.InfoHash) {
			r.Status, r.Detail = StError, "le .torrent reçu de V3X ne correspond pas à la fiche"
			continue
		}
		r.raw, r.Files = raw, len(t.Files)
		pairs, missing := libPairs(t, all, bySize)
		for _, f := range missing {
			r.Missing = append(r.Missing, f.Path)
		}
		dl, minP, canComplete := completable(t, missing)
		if len(missing) > 0 && (!canComplete || len(pairs) == 0) {
			r.Status = StLibPartial
			r.Detail = fmt.Sprintf("%d fichier%s de la release introuvable%s dans ta médiathèque", len(missing), plural(len(missing)), plural(len(missing)))
			continue
		}
		r.pairs = pairs
		r.Source = all[0].src
		for _, x := range all {
			if x.Path == pairs[0].local.Path {
				r.Source = x.src
			}
		}
		r.Folder = commonDir(pairs)
		n, err := verifyPieces(ctx, io, r.Source, t, pairs)
		switch {
		case err != nil:
			r.Status, r.Detail = StError, "lecture impossible : "+err.Error()
		case n < 0:
			r.Status, r.Detail = StLibDifferent, "mêmes tailles, mais d'autres données : ce n'est pas la même release"
		case len(missing) > 0:
			r.Status, r.MissBytes, r.minProgress = StLibComplete, dl, minP
			r.Detail = missingText(len(missing), "ton client peut %s télécharger (%s de morceaux), puis seeder", humanSize(dl))
		default:
			r.Status = StAdd
			if n > 0 {
				r.Detail = fmt.Sprintf("%d morceau%s lu%s sur ton disque, identique%s à la release V3X", n, pluralX(n), plural(n), plural(n))
			} else {
				r.Detail = "fichiers trop petits pour être vérifiés d'avance : ton client tranchera"
			}
		}
		// Le site n'aime pas les rafales.
		select {
		case <-ctx.Done():
		case <-time.After(downloadGap / 2):
		}
	}
	e.mu.Lock()
	e.lib.rows = rows
	e.touch()
	e.mu.Unlock()
	return nil
}

// missingText : « il ne manque qu'un petit fichier : ton client peut le
// télécharger… », « il ne manque que 3 petits fichiers : … les … ».
func missingText(n int, tail, size string) string {
	if n == 1 {
		return "il ne manque qu'un petit fichier : " + fmt.Sprintf(tail, "le", size)
	}
	return fmt.Sprintf("il ne manque que %d petits fichiers : ", n) + fmt.Sprintf(tail, "les", size)
}

// humanSize : « 4,2 Mio », « 512 Kio ».
func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return strings.Replace(fmt.Sprintf("%.1f Gio", float64(n)/(1<<30)), ".", ",", 1)
	case n >= 1<<20:
		return strings.Replace(fmt.Sprintf("%.1f Mio", float64(n)/(1<<20)), ".", ",", 1)
	default:
		return fmt.Sprintf("%d Kio", max(1, n>>10))
	}
}

func pluralX(n int) string {
	if n > 1 {
		return "x"
	}
	return ""
}

// libPairs : un fichier de la médiathèque pour chaque fichier de la release,
// de même taille, en restant de préférence dans le dossier du plus gros.
func libPairs(t *torrent.Torrent, all []libLocal, bySize map[int64][]int) ([]libPair, []torrent.File) {
	files := append([]torrent.File(nil), t.Files...)
	sort.SliceStable(files, func(i, j int) bool { return files[i].Size > files[j].Size })
	used := map[int]bool{}
	anchor := ""
	var pairs []libPair
	var missing []torrent.File
	for _, f := range files {
		best, bestScore := -1, -1.0
		for _, i := range bySize[f.Size] {
			if used[i] {
				continue
			}
			s := 0.0
			if anchor != "" && sameDir(all[i].Path, anchor) {
				s += 100
			}
			if strings.EqualFold(ext(all[i].Path), ext(f.Path)) {
				s += 10
			}
			s += 10 * tokenSimilarity(baseName(all[i].Path), base(f.Path))
			if s > bestScore {
				best, bestScore = i, s
			}
		}
		if best < 0 {
			missing = append(missing, f)
			continue
		}
		// Trop petit pour qu'un morceau en soit lu d'avance (un .nfo) : retenu
		// seulement près de la vidéo. Ailleurs dans la médiathèque, un autre
		// fichier de même taille passait pour lui, la release partait « À
		// ajouter » et le client la trouvait incomplète (Poelon, 25/10/26).
		if anchor != "" && len(t.InnerPieces(f, 1)) == 0 && !underDir(all[best].Path, anchor) {
			missing = append(missing, f)
			continue
		}
		used[best] = true
		if anchor == "" {
			anchor = all[best].Path
		}
		pairs = append(pairs, libPair{v: f, local: all[best].LibFile})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].v.Index < pairs[j].v.Index })
	return pairs, missing
}

// Chemins vus par D3X : Windows (« \ ») ou Unix.
func slash(p string) string { return strings.ReplaceAll(p, `\`, "/") }

func dirOf(p string) string { return pathDir(slash(p)) }

// pathDir : path.Dir qui garde le « // » de tête d'un chemin réseau Windows
// (« \\serveur\partage\… ») : path.Dir le réduit à « / ». Le dossier devenait
// « \serveur\… » et le nom du fichier, un chemin complet que qBittorrent
// refusait (HTTP 409, médiathèque d'odeuxcool, 22/10/26).
func pathDir(s string) string {
	if strings.HasPrefix(s, "//") && !strings.HasPrefix(s, "///") {
		return "/" + path.Dir(s[1:])
	}
	return path.Dir(s)
}

func baseName(p string) string { return path.Base(slash(p)) }

func sameDir(a, b string) bool { return strings.EqualFold(dirOf(a), dirOf(b)) }

// underDir : p est dans le dossier du fichier anchor, ou dans un de ses
// sous-dossiers (« Subs/ » d'une release).
func underDir(p, anchor string) bool {
	d := strings.ToLower(dirOf(anchor))
	return strings.HasPrefix(strings.ToLower(dirOf(p))+"/", strings.TrimSuffix(d, "/")+"/")
}

// commonDir : le dossier qui contient tous les fichiers trouvés.
func commonDir(pairs []libPair) string {
	if len(pairs) == 0 {
		return ""
	}
	d := dirOf(pairs[0].local.Path)
	for _, p := range pairs[1:] {
		for !strings.HasPrefix(strings.ToLower(slash(p.local.Path)), strings.ToLower(d)+"/") {
			nd := pathDir(d)
			if nd == d {
				break
			}
			d = nd
		}
	}
	if strings.Contains(pairs[0].local.Path, `\`) {
		d = strings.ReplaceAll(d, "/", `\`)
	}
	return d
}

// verifyPieces lit quelques morceaux de chaque fichier et compare leur
// empreinte à celle du .torrent. Renvoie le nombre de morceaux vérifiés, ou
// -1 au premier morceau différent.
func verifyPieces(ctx context.Context, io LibraryIO, src string, t *torrent.Torrent, pairs []libPair) (int, error) {
	n := 0
	for _, p := range pairs {
		for _, i := range t.InnerPieces(p.v, libPiecesPerFile) {
			off := int64(i)*t.PieceLength - p.v.Offset
			b, err := io.ReadAt(ctx, src, p.local.Path, off, t.PieceLength)
			if err != nil {
				return 0, err
			}
			if int64(len(b)) != t.PieceLength {
				return -1, nil
			}
			sum := sha1.Sum(b)
			if !bytes.Equal(sum[:], t.PieceHash(i)) {
				return -1, nil
			}
			n++
		}
	}
	return n, nil
}

// LibraryState : l'avancement de l'analyse et des ajouts.
type LibraryState struct {
	Scan     Progress       `json:"scan"`
	Version  int            `json:"version"`
	Counts   map[string]int `json:"counts"`
	Applying bool           `json:"applying"`
	Queue    int            `json:"queue"`
}

func (e *Engine) libStatus(r *LibRow) (string, float64, string) {
	if r.Match != nil {
		h := strings.ToLower(r.Match.InfoHash)
		if w := e.watches[h]; w != nil {
			return w.Status, w.Progress, w.Detail
		}
		if st, ok := e.final[h]; ok && (r.Status == StChecking || r.Status == StQueued) {
			return st.Status, st.Progress, st.Detail
		}
	}
	return r.Status, r.Progress, r.Detail
}

// Library renvoie l'état de la médiathèque.
func (e *Engine) Library() LibraryState {
	e.mu.Lock()
	defer e.mu.Unlock()
	counts := map[string]int{}
	for _, r := range e.lib.rows {
		st, _, _ := e.libStatus(r)
		counts[st]++
	}
	return LibraryState{Scan: e.lib.scan, Version: e.version, Counts: counts, Applying: e.lib.applying, Queue: len(e.lib.queue)}
}

// LibraryRows : les releases trouvées, à ajouter d'abord (les moins seedées
// en tête).
func (e *Engine) LibraryRows() []LibRow {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]LibRow, 0, len(e.lib.rows))
	for _, r := range e.lib.rows {
		c := *r
		c.Status, c.Progress, c.Detail = e.libStatus(r)
		c.raw, c.pairs = nil, nil
		out = append(out, c)
	}
	rank := map[string]int{StAdd: 0, StQueued: 0, StChecking: 0, StFailed: 0, StSeeding: 1, StInClient: 1, StLibComplete: 2, StLibPartial: 2, StLibDifferent: 3, StError: 3}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if rank[a.Status] != rank[b.Status] {
			return rank[a.Status] < rank[b.Status]
		}
		if a.Match.Seeders != b.Match.Seeders {
			return a.Match.Seeders < b.Match.Seeders
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return out
}

// ApplyLibrary met des releases de la médiathèque en file d'ajout.
func (e *Engine) ApplyLibrary(keys []string) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.libScanning() {
		return 0, errors.New("attends la fin de l'analyse")
	}
	n := 0
	for _, k := range keys {
		r := e.lib.rows[k]
		if r == nil || (r.Status != StAdd && r.Status != StLibComplete) {
			continue
		}
		r.Status, r.Detail = StQueued, ""
		e.lib.queue = append(e.lib.queue, k)
		n++
	}
	if n > 0 {
		e.touch()
	}
	if n > 0 && !e.lib.applying {
		e.lib.applying = true
		ctx, cancel := context.WithCancel(context.Background())
		e.lib.cancel = cancel
		go e.runLibQueue(ctx)
	}
	return n, nil
}

func (e *Engine) runLibQueue(ctx context.Context) {
	defer func() {
		e.mu.Lock()
		e.lib.applying = false
		for _, k := range e.lib.queue {
			if r := e.lib.rows[k]; r != nil && r.Status == StQueued {
				r.Status = StAdd
			}
		}
		e.lib.queue = nil
		e.touch()
		e.mu.Unlock()
	}()
	for {
		e.mu.Lock()
		if len(e.lib.queue) == 0 || ctx.Err() != nil {
			e.mu.Unlock()
			return
		}
		k := e.lib.queue[0]
		e.lib.queue = e.lib.queue[1:]
		r := e.lib.rows[k]
		e.mu.Unlock()
		if r == nil {
			continue
		}
		status, detail := e.addLibrary(ctx, r)
		e.mu.Lock()
		r.Status, r.Detail = status, detail
		e.touch()
		e.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(downloadGap):
		}
	}
}

// addLibrary : la release ajoutée sur les fichiers de la médiathèque, dans le
// client choisi pour la médiathèque (le premier sinon). Liens physiques si le
// membre les a choisis (la médiathèque n'est pas touchée), sinon renommage
// dans le client (rien ne bouge sur le disque).
func (e *Engine) addLibrary(ctx context.Context, r *LibRow) (string, string) {
	// Rien n'a été fait : la ligne reste ajoutable (ou à compléter).
	again := StAdd
	if r.minProgress > 0 {
		again = StLibComplete
	}
	env, err := e.clients()
	if err != nil {
		return again, err.Error()
	}
	cfg := env.Cfg
	id := cfg.LibraryClient
	if cfg.Client(id) == nil {
		id = ""
	}
	tg := env.Get(id)
	if tg == nil {
		return again, env.Unreachable(id).Error()
	}
	q := tg.Client
	e.mu.Lock()
	io := e.lib.io
	e.mu.Unlock()
	all, err := q.List(ctx, nil)
	if err != nil {
		return again, err.Error()
	}
	// Chemins vus par le client.
	cps := make([]string, len(r.pairs))
	for i, p := range r.pairs {
		cps[i] = io.ClientPath(r.Source, p.local.Path, tg.PathMap)
	}
	// Chemin de ce PC qu'aucune correspondance n'a traduit, pour un client qui
	// tourne sous Linux : on n'ajoute rien, on dit quel dossier déclarer.
	if len(cps) > 0 && ForeignDir(cps[0], all) {
		return again, ForeignDirError(commonDir(r.pairs), tg.Name).Error()
	}
	// À compléter : seulement dans le dossier de liens, et avec des liens
	// physiques. Le client y écrit les fichiers manquants, et réécrit à
	// l'identique les octets des morceaux qu'ils partagent avec tes fichiers :
	// un lien symbolique vers un partage en lecture seule le refuserait.
	if r.minProgress > 0 {
		if e.linkerFor(cfg, *tg) == nil {
			return again, "compléter demande le dossier de liens (Réglages) : les fichiers manquants y sont téléchargés, ta médiathèque n'est pas touchée"
		}
		if cfg.Links.Symbolic {
			return again, "compléter n'est pas possible avec des liens symboliques : le client doit pouvoir écrire les morceaux qui touchent tes fichiers"
		}
	}
	if link := e.linkerFor(cfg, *tg); link != nil {
		dir := strings.TrimSpace(tg.LinksDir)
		lp := make([]links.Pair, len(r.pairs))
		roots := map[string]bool{}
		for i, p := range r.pairs {
			lp[i] = links.Pair{Src: cps[i], Dst: clientJoin(dir, p.v.Path), Size: p.v.Size}
			roots[strings.SplitN(p.v.Path, "/", 2)[0]] = true
		}
		for root := range roots {
			target := clientJoin(dir, root)
			for _, x := range all {
				if under(x.ContentPath, target) || under(target, x.ContentPath) {
					return again, "« " + root + " » est déjà l'emplacement d'un torrent dans ton dossier de liens"
				}
			}
		}
		if err := link(ctx, tg.PathMap, dir, lp, false); err != nil {
			return again, "liens : " + err.Error()
		}
		hash, err := e.AddAndVerify(ctx, *tg, WithCategory(cfg, ""), r.Key, r.Match.ID, r.raw, dir)
		if err != nil {
			if hash == "" || errors.Is(err, ErrInClient) {
				_ = link(context.Background(), tg.PathMap, dir, lp, true)
			}
			switch {
			case errors.Is(err, ErrInClient):
				return StInClient, ""
			case hash == "":
				return again, err.Error()
			}
			return StFailed, err.Error()
		}
		if r.minProgress > 0 {
			e.allowMissing(hash, r.minProgress)
			return StChecking, fmt.Sprintf("liens dans « %s » : après la vérification, le client télécharge %s, puis seede", dir, map[bool]string{true: "le petit fichier manquant", false: fmt.Sprintf("les %d petits fichiers manquants", len(r.Missing))}[len(r.Missing) == 1])
		}
		return StChecking, fmt.Sprintf("liens dans « %s » : ta médiathèque n'est pas touchée", dir)
	}
	if !q.CanRename() {
		return again, q.Kind() + " ne sait pas renommer les fichiers d'un torrent : active les liens dans les réglages"
	}
	save := clientCommonDir(cps)
	if save == "" {
		return again, "fichiers dans des dossiers sans parent commun"
	}
	pairs := make([]client.Rename, len(r.pairs))
	var local []torrent.File
	for i, p := range r.pairs {
		rel := clientRel(save, cps[i])
		pairs[i] = client.Rename{Index: p.v.Index, From: p.v.Path, To: rel}
		local = append(local, torrent.File{Path: rel, Size: p.v.Size, Index: i})
	}
	if err := renameSafe(pairs, local, save, "", all, diskLookup(ctx, q, save, tg.PathMap)); err != nil {
		return again, err.Error() + " (ou active les liens dans les réglages, symboliques pour un partage en lecture seule)"
	}
	hash, n, err := e.addAndRename(ctx, *tg, cfg, r.Key, r.Match.ID, r.raw, save, pairs)
	if err != nil {
		switch {
		case errors.Is(err, ErrInClient):
			return StInClient, ""
		case hash == "":
			return again, err.Error()
		}
		return StFailed, err.Error()
	}
	return StChecking, fmt.Sprintf("%d nom%s ajusté%s dans le client, rien ne bouge sur le disque", n, plural(n), plural(n))
}

// clientCommonDir : le dossier parent commun de chemins vus par le client.
func clientCommonDir(ps []string) string {
	if len(ps) == 0 {
		return ""
	}
	win := strings.Contains(ps[0], `\`) || (len(ps[0]) >= 2 && ps[0][1] == ':')
	d := dirOf(ps[0])
	for _, p := range ps[1:] {
		for !strings.HasPrefix(slash(p), d+"/") {
			nd := pathDir(d)
			if nd == d {
				return ""
			}
			d = nd
		}
	}
	if win {
		d = strings.ReplaceAll(d, "/", `\`)
		if strings.HasSuffix(d, ":") {
			d += `\`
		}
	}
	return d
}

// clientRel : p relatif à dir, avec des « / ».
func clientRel(dir, p string) string {
	d := strings.TrimRight(slash(dir), "/")
	return strings.TrimPrefix(slash(p), d+"/")
}
