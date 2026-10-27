// Package crossseed retrouve sur V3X les releases que ton client seede déjà
// ailleurs, et les ajoute au client torrent sur les mêmes fichiers.
//
// Règles de prudence :
//   - correspondance par empreinte de contenu (chemins + tailles de chaque
//     fichier), jamais par taille totale ni par ressemblance de nom ;
//   - le .torrent reçu de V3X est revérifié en local avant l'ajout ;
//   - ajout À L'ARRÊT puis vérification complète par le client ; le torrent
//     n'est démarré que s'il est complet à 100 %. Sinon il reste arrêté :
//     D3X ne télécharge jamais rien à ta place.
package crossseed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/V3x0rV3x/D3X/internal/client"
	"github.com/V3x0rV3x/D3X/internal/config"
	"github.com/V3x0rV3x/D3X/internal/links"
	"github.com/V3x0rV3x/D3X/internal/pathmap"
	"github.com/V3x0rV3x/D3X/internal/torrent"
	"github.com/V3x0rV3x/D3X/internal/v3x"
)

// Statuts d'une ligne.
const (
	StAdd      = "add"       // mêmes fichiers, mêmes noms : ajoutable
	StRenamed  = "renamed"   // mêmes tailles, noms différents
	StOnV3X    = "on_v3x"    // déjà seedé pour V3X
	StInClient = "in_client" // le torrent V3X est déjà dans le client
	StNone     = "none"      // pas sur V3X
	StPartial  = "partial"   // téléchargé en partie : rien à seeder en entier
	StError    = "error"     // .torrent illisible ou export impossible
	StQueued   = "queued"    // dans la file d'ajout
	StChecking = "checking"  // ajouté à l'arrêt, le client vérifie
	StSeeding  = "seeding"   // vérifié à 100 %, démarré
	StFailed   = "failed"    // vérification incomplète : laissé à l'arrêt
	StRemoved  = "removed"   // retiré du client (fichiers intacts)
	// Pas de fiche en ligne, mais V3X a retiré (remplacé, archivé) une fiche du
	// même contenu : pas proposée à la publication (issue GitHub #1, peplade).
	StGone = "gone_v3x"
	// Ta copie n'est plus sur le disque (supprimée ou déplacée) alors que le
	// client la croit complète : rien à ajouter (retour de V3x0r, 21/10/26).
	StMissing = "missing"
	// La même vidéo est sur V3X sous une autre forme (nom refait, .nfo en
	// plus, dossier au lieu d'un fichier seul) : pas « Pas sur V3X » (Luci,
	// 21/10/26).
	StSameVideo = "same_video"
)

// Domaines de V3X : un torrent qui annonce déjà chez nous est ignoré.
var v3xHosts = []string{"v3x.club", "v3x.fr", "v3x.tw"}

// IsV3XHost dit si un tracker est celui de V3X.
func IsV3XHost(h string) bool { return isV3XHost(h) }

func isV3XHost(h string) bool {
	for _, s := range v3xHosts {
		if h == s || strings.HasSuffix(h, "."+s) {
			return true
		}
	}
	return false
}

// Sig est l'empreinte d'un torrent du client (mise en cache par info-hash).
type Sig struct {
	InfoHash    string `json:"infoHash"`
	Name        string `json:"name"`
	ContentHash string `json:"contentHash"`
	SizeHash    string `json:"sizeHash"`
	Total       int64  `json:"total"`
	Real        int64  `json:"real"` // hors fichiers de bourrage
	Files       int    `json:"files"`
	Big         int64  `json:"big,omitempty"`     // taille du plus gros fichier
	BigExt      string `json:"bigExt,omitempty"`  // son extension (« . » s'il n'en a pas)
	BigName     string `json:"bigName,omitempty"` // son nom, sans dossier
}

// noteFile compte un fichier dans l'empreinte et retient le plus gros.
func (s *Sig) noteFile(p string, size int64) {
	s.Real += size
	if size > s.Big {
		s.Big, s.BigExt, s.BigName = size, extOf(p), path.Base(strings.ReplaceAll(p, `\`, "/"))
	}
}

// extOf : l'extension en minuscules, « . » pour un fichier sans extension.
func extOf(p string) string {
	if e := strings.ToLower(path.Ext(p)); e != "" {
		return e
	}
	return "."
}

// Row est une release du client (copies sur les mêmes données fusionnées).
type Row struct {
	Key      string     `json:"key"`
	Name     string     `json:"name"`
	Size     int64      `json:"size"`
	Files    int        `json:"files"`
	Copies   int        `json:"copies"`
	Status   string     `json:"status"`
	Detail   string     `json:"detail,omitempty"`
	Match    *v3x.Match `json:"match,omitempty"`
	Progress float64    `json:"progress,omitempty"`
	// CanAdd : ligne « noms différents » ajoutable (le client sait renommer).
	CanAdd bool `json:"canAdd,omitempty"`
	// Path : l'emplacement de la copie, vu par le client.
	Path string `json:"path,omitempty"`
	// Client : le client qui a cette copie (identifiant), et son nom.
	Client     string `json:"client,omitempty"`
	ClientName string `json:"clientName,omitempty"`
	// Alts : les autres copies fusionnées dans cette ligne (un torrent par
	// tracker, souvent sur des liens physiques). Publier et le Lot prennent
	// la première que D3X sait lire (exodius19, 21/10/26).
	Alts []RowCopy `json:"-"`

	hash        string // info-hash de la copie dans son client
	contentPath string
	category    string // catégorie de la copie dans son client (modèle {cat})
	sig         *Sig
}

// RowCopy : une copie de plus d'une ligne, dans son client.
type RowCopy struct {
	Client string
	Path   string
}

// Progress décrit l'analyse en cours.
type Progress struct {
	Phase  string `json:"phase"` // idle, list, read, match, done, error
	Done   int    `json:"done"`
	Total  int    `json:"total"`
	Detail string `json:"detail,omitempty"`
	Error  string `json:"error,omitempty"`
	// Warn : les clients injoignables pendant l'analyse, qui a continué avec
	// les autres (« Seedbox injoignable : … »).
	Warn string    `json:"warn,omitempty"`
	At   time.Time `json:"at,omitempty"`
}

// watch est un ajout que D3X surveille jusqu'à la fin de la vérification.
type watch struct {
	// Client : l'identifiant du client où l'ajout a été fait (vide : le
	// premier, ajout lancé par une version qui n'en connaissait qu'un).
	Client      string    `json:"client,omitempty"`
	Key         string    `json:"key"`
	Name        string    `json:"name"`
	ID          string    `json:"id"`
	InfoHash    string    `json:"infoHash"`
	Since       time.Time `json:"since"`
	SawChecking bool      `json:"sawChecking"`
	// Vérification relancée (la demande se perd quand le client n'est pas
	// encore prêt juste après l'ajout) : combien de fois, et quand.
	Rechecks  int       `json:"rechecks,omitempty"`
	RecheckAt time.Time `json:"recheckAt,omitempty"`
	// Fin de vérification sous 100 % vue à ce moment-là, avec cette progression :
	// l'échec n'est retenu qu'une fois l'état stable (qBittorrent 5 passe par des
	// états intermédiaires pendant une revérification).
	EndAt       time.Time `json:"endAt,omitempty"`
	EndProgress float64   `json:"endProgress,omitempty"`
	// Vue en vérification à cette progression, et bougée depuis ou non : un
	// torrent sorti de la file d'attente de vérification sans avoir été
	// vérifié (client arrêté, bertho-zero, ticket n°3, 27/10/26) finit
	// arrêté là où il était. Relaunched : cette vérification perdue a déjà
	// été relancée une fois, le résultat suivant fait foi.
	CheckFrom  float64 `json:"checkFrom,omitempty"`
	CheckMoved bool    `json:"checkMoved,omitempty"`
	Relaunched bool    `json:"relaunched,omitempty"`
	// MinProgress : démarre dès cette vérification atteinte, sous 100 %
	// (release de la médiathèque à compléter) ; 0 = 100 % exigé.
	MinProgress float64 `json:"minProgress,omitempty"`
	// Absent de la liste du client depuis ce moment : « retiré » seulement
	// après missGrace (un client qui redémarre répond un temps sans ses
	// torrents ; exodius19, 21/10/26, Transmission en conteneur).
	MissSince time.Time `json:"missSince,omitempty"`
	Status    string    `json:"status"` // checking, failed
	Progress  float64   `json:"progress"`
	Detail    string    `json:"detail,omitempty"`
}

// Engine garde l'état du cross-seed pour la page.
type Engine struct {
	clients Clients

	mu       sync.Mutex
	rows     map[string]*Row
	scan     Progress
	version  int // incrémenté à chaque changement des lignes
	queue    []string
	current  string
	watches  map[string]*watch     // par info-hash V3X
	final    map[string]WatchState // dernier état connu des ajouts terminés
	cancel   context.CancelFunc
	applying bool
	linker   Linker
	refresh  Refresh
	lib      library
	batch    int // releases du lot d'ajout en cours
	batchOK  int // déjà traitées
	// Publications vérifiées gardées à l'arrêt jusqu'à ce moment (option
	// « Démarrer le seed à la fin du lot », voir HoldPublished).
	holdPub time.Time
}

// New crée le moteur et reprend la surveillance des ajouts en cours.
func New(c Clients) *Engine {
	e := &Engine{clients: c, rows: map[string]*Row{}, watches: map[string]*watch{}, final: map[string]WatchState{}, scan: Progress{Phase: "idle"}}
	e.loadWatches()
	go e.watchLoop()
	return e
}

func (e *Engine) touch() { e.version++ }

// Linker crée (remove=false) ou retire des liens physiques ; chemins vus par
// le client torrent, dont rules est la correspondance de dossiers.
type Linker func(ctx context.Context, rules []pathmap.Rule, base string, pairs []links.Pair, remove bool) error

// SetLinker branche la création des liens physiques (ce PC ou la seedbox).
func (e *Engine) SetLinker(l Linker) {
	e.mu.Lock()
	e.linker = l
	e.mu.Unlock()
}

// linkerFor : le créateur de liens, si le membre a choisi ce repli et donné
// le dossier des liens de ce client.
func (e *Engine) linkerFor(cfg *config.Config, t Target) Linker {
	if cfg == nil || !cfg.Links.On || strings.TrimSpace(t.LinksDir) == "" {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.linker
}

// ---------- analyse ----------

// Scanning dit si une analyse tourne.
func (e *Engine) Scanning() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.scan.Phase == "list" || e.scan.Phase == "read" || e.scan.Phase == "match"
}

// StartScan lance une analyse en arrière-plan.
func (e *Engine) StartScan() error {
	e.mu.Lock()
	if e.scan.Phase == "list" || e.scan.Phase == "read" || e.scan.Phase == "match" {
		e.mu.Unlock()
		return errors.New("une analyse est déjà en cours")
	}
	if e.applying {
		e.mu.Unlock()
		return errors.New("des ajouts sont en cours : attends qu'ils soient finis")
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.scan = Progress{Phase: "list"}
	e.mu.Unlock()
	go func() {
		err := e.runScan(ctx)
		e.mu.Lock()
		defer e.mu.Unlock()
		if err != nil {
			e.scan.Phase, e.scan.Error = "error", err.Error()
		} else {
			e.scan.Phase, e.scan.Detail = "done", ""
		}
		e.scan.At = time.Now()
		e.touch()
	}()
	return nil
}

// Stop interrompt l'analyse ou la file d'ajout.
func (e *Engine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cancel != nil {
		e.cancel()
	}
	e.queue = nil
}

func (e *Engine) setProgress(phase string, done, total int, detail string) {
	e.mu.Lock()
	e.scan.Phase, e.scan.Done, e.scan.Total, e.scan.Detail = phase, done, total, detail
	e.mu.Unlock()
}

// normPath sert de clé de regroupement : / et \ se valent, sans séparateur
// final ; un chemin Windows se compare sans la casse.
func normPath(p string) string {
	n := strings.TrimRight(strings.ReplaceAll(p, `\`, "/"), "/")
	if len(n) >= 2 && n[1] == ':' {
		n = strings.ToLower(n)
	}
	return n
}

func (e *Engine) runScan(ctx context.Context) error {
	e.retry(ctx)
	env, err := e.clients()
	if err != nil {
		return err
	}
	cfg := env.Cfg
	lists, down := ListAll(ctx, env)
	if ctx.Err() != nil {
		return errors.New("analyse arrêtée")
	}
	if len(lists) == 0 {
		return errors.New(DownText(down))
	}
	e.mu.Lock()
	e.scan.Warn = DownText(down)
	e.mu.Unlock()

	// Toutes les listes fusionnées. Ce qui est déjà dans l'un des clients
	// (info-hash connu) n'est jamais proposé à l'ajout.
	known := map[string]bool{}
	type group struct {
		t      Target
		rank   int // place du client dans les réglages
		path   string
		copies []client.Torrent
		onV3X  bool
	}
	groups := map[string]*group{}
	var order []string
	byID := map[string]Target{}
	for rank, l := range lists {
		byID[l.Target.ID] = l.Target
		for _, t := range l.List {
			for _, h := range t.Hashes {
				known[h] = true
			}
			if t.Progress < 1 || t.ContentPath == "" {
				continue
			}
			// Mêmes données = même emplacement dans le même client (deux
			// clients ne nomment pas leurs dossiers de la même façon).
			k := l.Target.ID + "|" + normPath(t.ContentPath)
			g := groups[k]
			if g == nil {
				g = &group{t: l.Target, rank: rank, path: normPath(t.ContentPath)}
				groups[k] = g
				order = append(order, k)
			}
			g.copies = append(g.copies, t)
			for _, h := range t.Trackers {
				if isV3XHost(h) {
					g.onV3X = true
				}
			}
		}
	}

	// Ordre stable (client dans l'ordre des réglages, puis emplacement) :
	// d'une analyse à l'autre, la même copie sert à l'ajout quand il y en a
	// plusieurs identiques, et c'est celle du premier client qui l'a.
	sort.Slice(order, func(i, j int) bool {
		a, b := groups[order[i]], groups[order[j]]
		if a.rank != b.rank {
			return a.rank < b.rank
		}
		return a.path < b.path
	})

	cache := loadSigCache()
	fresh := map[string]Sig{}
	rows := map[string]*Row{}
	rowOf := map[string]*Row{} // par groupe
	var toRead []*Row
	for _, k := range order {
		g := groups[k]
		rep := g.copies[0]
		hash := strings.ToLower(rep.Hash)
		r := &Row{Key: hash + "@" + g.t.ID, hash: hash, Name: rep.Name, Size: rep.TotalSize, Copies: len(g.copies), contentPath: rep.ContentPath, category: rep.Category, Path: rep.ContentPath, Client: g.t.ID, ClientName: g.t.Name}
		rows[r.Key] = r
		rowOf[k] = r
		if g.onV3X {
			r.Status = StOnV3X
			continue
		}
		if s, ok := cache[hash]; ok {
			s := s
			r.sig = &s
			fresh[hash] = s
		} else {
			toRead = append(toRead, r)
		}
		r.Status = StNone
	}

	// Lecture des .torrent d'origine (4 à la fois), seulement ceux absents du cache.
	e.setProgress("read", 0, len(toRead), "")
	var (
		wg   sync.WaitGroup
		sem  = make(chan struct{}, 4)
		mu   sync.Mutex
		done int
	)
	for _, r := range toRead {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(r *Row) {
			defer wg.Done()
			defer func() { <-sem }()
			s, err := sigFromClient(ctx, byID[r.Client].Client, r.hash, r.Name)
			mu.Lock()
			defer mu.Unlock()
			done++
			if err != nil {
				r.Status, r.Detail = StError, err.Error()
			} else {
				r.sig = s
				fresh[r.hash] = *s
			}
			e.setProgress("read", done, len(toRead), "")
		}(r)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return errors.New("analyse arrêtée")
	}
	// Un client injoignable : ses empreintes restent en cache pour la suite.
	if len(down) > 0 {
		for h, s := range cache {
			if _, ok := fresh[h]; !ok {
				fresh[h] = s
			}
		}
	}
	saveSigCache(fresh)

	// Téléchargé en partie : moins de données sélectionnées que la release.
	var items []v3x.MatchItem
	byKey := map[string]*Row{}
	for _, k := range order {
		g := groups[k]
		r := rowOf[k]
		if r.sig == nil || r.Status != StNone {
			continue
		}
		r.Files = r.sig.Files
		if g.copies[0].Size < r.sig.Real {
			r.Status, r.Detail = StPartial, "fichiers en partie désélectionnés dans le client"
			continue
		}
		// Pas d'info-hash : celui d'un torrent d'un autre tracker privé lui est
		// propre et dirait au site d'où vient ta copie. Ce qui est déjà sur V3X
		// se repère ici, par le tracker et par les info-hash connus du client.
		// La clé envoyée est un simple numéro d'ordre, pour la même raison.
		k := strconv.Itoa(len(items))
		items = append(items, v3x.MatchItem{Key: k, ContentHash: r.sig.ContentHash, SizeHash: r.sig.SizeHash, Total: r.sig.Total})
		byKey[k] = r
	}

	// Comparaison au catalogue, 500 empreintes par appel.
	retry := &v3x.Retry{OnWait: func(d time.Duration) {
		e.mu.Lock()
		if d > 0 {
			e.scan.Detail = fmt.Sprintf("V3X limite les appels, reprise dans %d s", int(d.Round(time.Second)/time.Second))
		} else {
			e.scan.Detail = ""
		}
		e.mu.Unlock()
	}}
	for i := 0; i < len(items); i += v3x.MatchBatchSize {
		end := i + v3x.MatchBatchSize
		if end > len(items) {
			end = len(items)
		}
		e.setProgress("match", i, len(items), "")
		res, err := env.V3X.MatchBatch(ctx, retry, items[i:end])
		if err != nil {
			return err
		}
		for key, m := range res {
			r := byKey[key]
			if r == nil || m == nil {
				continue
			}
			if m.Kind == "gone" {
				r.Status = StGone
				r.Detail = m.Gone.Text() + " : D3X ne la propose pas à la publication (Publier reste possible si la raison ne s'applique plus)"
				continue
			}
			// Petite release rapprochée par les tailles seules (un livre dont le
			// nom ne diffère que par les majuscules, Luci 23/10/26) : seulement
			// au même nom, à la casse et à la ponctuation près.
			if m.NameCheck && !sameReleaseName(m.Name, r.Name) {
				continue
			}
			r.Match = m
			switch {
			case m.Kind == "self":
				r.Status = StOnV3X
			case known[strings.ToLower(m.InfoHash)]:
				r.Status = StInClient
			case m.Kind == "exact":
				r.Status = StAdd
			default:
				r.Status = StRenamed
				t := byID[r.Client]
				if t.Client.CanRename() {
					r.CanAdd = true
					r.Detail = "mêmes fichiers sous d'autres noms : D3X peut l'ajouter sous les noms de ta copie, sans rien toucher sur le disque"
				} else if e.linkerFor(cfg, t) != nil {
					r.CanAdd = true
					r.Detail = "mêmes fichiers sous d'autres noms : D3X peut la ranger dans ton dossier de liens, ses fichiers étant des liens vers ta copie (aucune place en plus)"
				} else {
					r.Detail = "mêmes fichiers sous d'autres noms : " + t.Client.Kind() + " ne sait pas renommer les fichiers d'un torrent"
				}
			}
		}
	}
	e.setProgress("match", len(items), len(items), "")

	var left []*Row
	for _, k := range order {
		if r := rowOf[k]; r != nil && r.Status == StNone && r.sig != nil {
			left = append(left, r)
		}
	}
	// Simple confort : si le site ne répond pas, ces lignes restent « Pas sur
	// V3X », l'analyse ne tombe pas pour autant, mais le dit (1.1.28 : l'échec
	// passait sans un mot).
	if err := e.markSameVideo(ctx, env.V3X, retry, byID, left, known, fresh); err != nil {
		if ctx.Err() != nil {
			return errors.New("analyse arrêtée")
		}
		e.mu.Lock()
		msg := "rapprochement par fichier (même vidéo, même image disque) impossible : " + err.Error()
		if e.scan.Warn != "" {
			e.scan.Warn += " ; " + msg
		} else {
			e.scan.Warn = msg
		}
		e.mu.Unlock()
	}

	// Plusieurs copies identiques rangées à des endroits différents (une par
	// tracker, ou une par client) trouvent la même release V3X : une seule
	// ligne, la première copie (celle du premier client qui l'a) servira à
	// l'ajout, les autres sont comptées.
	firstOf := map[string]*Row{}
	for _, k := range order {
		r := rowOf[k]
		if r == nil || r.Match == nil || (r.Status != StAdd && r.Status != StRenamed) {
			continue
		}
		if f := firstOf[r.Match.ID]; f != nil {
			f.Copies += r.Copies
			f.Alts = append(append(f.Alts, RowCopy{Client: r.Client, Path: r.Path}), r.Alts...)
			delete(rows, r.Key)
			continue
		}
		firstOf[r.Match.ID] = r
	}
	// Pas sur V3X : de même, une release seedée plusieurs fois (un torrent par
	// tracker, sur des liens physiques) n'apparaît qu'une fois, avec son nombre
	// de copies, au lieu d'une ligne par torrent (exodius19, 21/10/26). Mêmes
	// noms et mêmes tailles de fichiers = même release.
	sameRelease := map[string]*Row{}
	for _, k := range order {
		r := rowOf[k]
		if r == nil || r.sig == nil || r.sig.ContentHash == "" || (r.Status != StNone && r.Status != StSameVideo) {
			continue
		}
		if f := sameRelease[r.sig.ContentHash]; f != nil {
			f.Copies += r.Copies
			f.Alts = append(append(f.Alts, RowCopy{Client: r.Client, Path: r.Path}), r.Alts...)
			delete(rows, r.Key)
			continue
		}
		sameRelease[r.sig.ContentHash] = r
	}

	e.mu.Lock()
	e.rows = rows
	e.touch()
	e.mu.Unlock()
	return nil
}

// sigFromClient : l'empreinte d'un torrent du client, par son .torrent
// d'origine quand le client sait le rendre (qBittorrent 4.5+), sinon par la
// liste de fichiers qu'il donne (Transmission, Deluge, rTorrent).
func sigFromClient(ctx context.Context, q client.Client, hash, name string) (*Sig, error) {
	raw, err := q.Export(ctx, hash)
	if err == nil {
		t, err := torrent.Parse(raw)
		if err != nil {
			return nil, err
		}
		return sigOf(t), nil
	}
	if !errors.Is(err, client.ErrNoExport) {
		return nil, err
	}
	files, total, err := q.Files(ctx, hash)
	if err != nil {
		return nil, err
	}
	var kept []torrent.File
	for _, f := range files {
		if !torrent.IsPadPath(f.Path) {
			kept = append(kept, f)
		}
	}
	if len(kept) == 0 || total <= 0 {
		return nil, errors.New("liste de fichiers vide")
	}
	s := &Sig{Name: name, ContentHash: torrent.ContentSignature(kept, total), SizeHash: torrent.SizeSignature(kept, total), Total: total, Files: len(kept)}
	for _, f := range kept {
		s.noteFile(f.Path, f.Size)
	}
	return s, nil
}

func sigOf(t *torrent.Torrent) *Sig {
	s := &Sig{InfoHash: t.InfoHash, Name: t.Name, ContentHash: t.ContentHash, SizeHash: t.SizeHash, Total: t.Total, Files: len(t.Files)}
	for _, f := range t.Files {
		s.noteFile(f.Path, f.Size)
	}
	return s
}

// sameVideoMin : en dessous, deux fichiers de même taille peuvent l'être par
// hasard.
const sameVideoMin = 200 << 20

// sameVideoExt : « Même vidéo » ne vaut que pour une vraie vidéo. Les
// installeurs de jeux découpés en morceaux de taille fixe (setup-1.bin…) font
// la même taille d'un jeu à l'autre (Nayr, 22/10/26 : un Terminator pris pour
// Baldur's Gate 3), et une image disque (.iso) peut aussi être un jeu.
var sameVideoExt = map[string]bool{".mkv": true, ".mp4": true, ".avi": true, ".m2ts": true, ".ts": true, ".mov": true, ".wmv": true, ".webm": true, ".mpg": true, ".mpeg": true, ".m4v": true, ".vob": true}

// sameFileExt : une image disque passe en « Même fichier sur V3X » seulement
// si, en plus de la taille à l'octet près, la fiche V3X porte le même nom de
// release (Nayr, 22/10/26 : Ori.and.the.Will.of.the.Wisps-Mephisto, un .nfo
// différent à côté de l'.iso). Deux jeux différents n'ont pas le même nom.
var sameFileExt = map[string]bool{".iso": true}

// sameReleaseName : même nom de release, aux séparateurs, à la casse et à
// l'extension près (« Ori.and.the.Will-Mephisto » = « Ori and the Will-Mephisto.iso »).
func sameReleaseName(a, b string) bool {
	norm := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		if e := path.Ext(s); sameFileExt[e] || sameVideoExt[e] {
			s = strings.TrimSuffix(s, e)
		}
		var sb strings.Builder
		for _, c := range s {
			if unicode.IsLetter(c) || unicode.IsDigit(c) {
				sb.WriteRune(c)
			}
		}
		return sb.String()
	}
	na := norm(a)
	return na != "" && na == norm(b)
}

// looksLikeFileExt : « .mkv », « .epub »… et pas la fin d'un nom de release
// (« .wisps-mephisto », « .x264 »), qui trahit un fichier rangé dans un dossier.
var notAnExt = regexp.MustCompile(`^\.(?:[xh]26[45]|hevc|avc|av1|\d{3,4}p|web|dl|multi)$`)
var fileExtRe = regexp.MustCompile(`^\.[a-z0-9]{2,4}$`)

func looksLikeFileExt(ext string) bool {
	return sameVideoExt[ext] || sameFileExt[ext] || (fileExtRe.MatchString(ext) && !notAnExt.MatchString(ext))
}

// sameFileName : même nom de fichier, à la casse près (« Ori…-Mephisto.iso »).
func sameFileName(a, b string) bool {
	return a != "" && b != "" && strings.EqualFold(path.Base(a), path.Base(b))
}

// markSameVideo : une release absente du catalogue sous cette forme peut y
// être sous une autre (nom refait, .nfo en plus, dossier au lieu d'un fichier
// seul ; Motives chez Luci, 21/10/26). L'empreinte de la release entière ne
// colle pas, mais son plus gros fichier se retrouve à l'octet près dans une
// fiche V3X : la ligne passe en « Même vidéo sur V3X », ou en « Déjà dans le
// client » si cette fiche y est déjà. Seule la taille part au site, comme pour
// la Médiathèque.
func (e *Engine) markSameVideo(ctx context.Context, v *v3x.Client, retry *v3x.Retry, byID map[string]Target, rows []*Row, known map[string]bool, fresh map[string]Sig) error {
	reread := false
	for _, r := range rows {
		// Image disque : son nom de fichier sert au rapprochement (1.1.28), une
		// empreinte d'avant ne l'a pas et se relit.
		if (r.sig.Big > 0 && r.sig.BigExt != "" && (r.sig.BigName != "" || !sameFileExt[r.sig.BigExt])) || ctx.Err() != nil {
			continue
		}
		// Un seul fichier, nommé comme le torrent : c'est lui. Mais un fichier
		// seul rangé dans un dossier porte le nom du dossier, sans extension
		// (Ori de Nayr, 23/10/26 : « Ori…-Mephisto/Ori…-Mephisto.iso » lu comme
		// un fichier d'extension « .wisps-mephisto ») : celui-là se relit.
		if ext := extOf(r.sig.Name); r.sig.Files == 1 && looksLikeFileExt(ext) {
			r.sig.Big, r.sig.BigExt, r.sig.BigName = r.sig.Real, ext, r.sig.Name
			continue
		}
		// Empreinte d'une version d'avant : la taille ou l'extension du plus
		// gros fichier manque, le .torrent de cette ligne-là est relu.
		if s, err := sigFromClient(ctx, byID[r.Client].Client, r.hash, r.Name); err == nil {
			r.sig = s
			fresh[r.hash] = *s
			reread = true
		}
	}
	if reread {
		saveSigCache(fresh)
	}
	if err := ctx.Err(); err != nil {
		return errors.New("analyse arrêtée")
	}
	bySize := map[int64][]*Row{}
	var sizes []int64
	for _, r := range rows {
		if r.sig.Big < sameVideoMin || !(sameVideoExt[r.sig.BigExt] || sameFileExt[r.sig.BigExt]) {
			continue
		}
		if bySize[r.sig.Big] == nil {
			sizes = append(sizes, r.sig.Big)
		}
		bySize[r.sig.Big] = append(bySize[r.sig.Big], r)
	}
	for i := 0; i < len(sizes); i += v3x.MatchFilesSize {
		res, err := v.MatchFiles(ctx, retry, sizes[i:min(i+v3x.MatchFilesSize, len(sizes))])
		if err != nil {
			return err
		}
		for size, list := range res {
			if len(list) == 0 {
				continue
			}
			for _, r := range bySize[size] {
				// Image disque : seulement une fiche au même nom de release, ou dont
				// l'image porte le même nom de fichier (Nayr, 22/10/26 : sa copie
				// d'Ori vient d'ailleurs sous un autre nom de release, avec le même
				// .iso à l'octet et au nom près).
				file := sameFileExt[r.sig.BigExt]
				best, inClient, ok := bestFileMatch(list, known, func(m v3x.FileMatch) bool {
					return !file || sameReleaseName(m.Name, r.Name) || sameFileName(m.File, r.sig.BigName)
				})
				if !ok {
					continue
				}
				kind, what := "same_video", "la même vidéo"
				if file {
					kind, what = "same_file", "le même fichier "+r.sig.BigExt
				}
				r.Match = &v3x.Match{Kind: kind, ID: best.ID, Name: best.Name, InfoHash: best.InfoHash, Seeders: best.Seeders, Leechers: best.Leechers, Size: best.Size}
				if inClient {
					r.Status, r.Detail = StInClient, what+" est déjà dans ton client avec la fiche V3X « "+best.Name+" »"
				} else {
					r.Status, r.Detail = StSameVideo, what+" est sur V3X dans « "+best.Name+" », sous un autre nom ou avec d'autres fichiers à côté"
				}
			}
		}
	}
	return nil
}

// bestFileMatch : parmi les fiches V3X retenues par keep, celle déjà dans un
// client d'abord, sinon la plus seedée.
func bestFileMatch(list []v3x.FileMatch, known map[string]bool, keep func(v3x.FileMatch) bool) (best v3x.FileMatch, inClient, ok bool) {
	for _, m := range list {
		if !keep(m) {
			continue
		}
		if known[strings.ToLower(m.InfoHash)] {
			return m, true, true
		}
		if !ok || m.Seeders > best.Seeders {
			best, ok = m, true
		}
	}
	return best, false, ok
}

func loadSigCache() map[string]Sig {
	out := map[string]Sig{}
	p, err := config.Path("signatures.json")
	if err != nil {
		return out
	}
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

func saveSigCache(m map[string]Sig) {
	if b, err := json.Marshal(m); err == nil {
		_ = config.WriteFileAtomic("signatures.json", b)
	}
}

// ---------- état pour la page ----------

// Status est le résumé léger interrogé chaque seconde par la page.
type Status struct {
	Scan     Progress       `json:"scan"`
	Version  int            `json:"version"`
	Counts   map[string]int `json:"counts"`
	Queue    int            `json:"queue"`
	Current  string         `json:"current,omitempty"`
	Applying bool           `json:"applying"`
	Batch    int            `json:"batch"`
	BatchOK  int            `json:"batchDone"`
}

// State renvoie le résumé.
func (e *Engine) State() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	counts := map[string]int{}
	for _, r := range e.rows {
		counts[e.statusOf(r)]++
	}
	return Status{Scan: e.scan, Version: e.version, Counts: counts, Queue: len(e.queue), Current: e.current, Applying: e.applying, Batch: e.batch, BatchOK: e.batchOK}
}

// statusOf : le statut d'une ligne, suivi des ajouts compris (verrou tenu).
func (e *Engine) statusOf(r *Row) string {
	if r.Match != nil {
		if w := e.watches[strings.ToLower(r.Match.InfoHash)]; w != nil {
			return w.Status
		}
	}
	return r.Status
}

// Groupes de la page (un onglet chacun) : à l'intérieur d'un groupe, l'ordre
// ne dépend pas du statut, pour que les lignes ne sautent pas pendant l'ajout.
var statusGroup = map[string]int{StAdd: 0, StQueued: 0, StChecking: 0, StFailed: 0, StSeeding: 1, StInClient: 1, StOnV3X: 1, StRenamed: 2, StSameVideo: 3, StNone: 3, StPartial: 4, StError: 4, StRemoved: 4, StGone: 4}

// Rows renvoie les lignes, les plus utiles d'abord : à ajouter, et parmi
// elles les releases les moins seedées sur V3X (celles qui ont besoin de toi).
func (e *Engine) Rows() []Row {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Row, 0, len(e.rows))
	for _, r := range e.rows {
		c := *r
		c.Status = e.statusOf(r)
		if r.Match != nil {
			if w := e.watches[strings.ToLower(r.Match.InfoHash)]; w != nil {
				c.Progress, c.Detail = w.Progress, w.Detail
			}
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if statusGroup[a.Status] != statusGroup[b.Status] {
			return statusGroup[a.Status] < statusGroup[b.Status]
		}
		if a.Match != nil && b.Match != nil && a.Match.Seeders != b.Match.Seeders {
			return a.Match.Seeders < b.Match.Seeders
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return out
}
