package crossseed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/V3x0rV3x/D3X/internal/client"
	"github.com/V3x0rV3x/D3X/internal/config"
	"github.com/V3x0rV3x/D3X/internal/journal"
	"github.com/V3x0rV3x/D3X/internal/torrent"
	"github.com/V3x0rV3x/D3X/internal/v3x"
)

// Pause entre deux .torrent demandés à V3X : le site n'est pas pressé.
var downloadGap = time.Second

// Rythme de la surveillance des vérifications (raccourci par les tests).
var watchEvery = 3 * time.Second

// Apply met en file les lignes choisies (seulement celles « à ajouter »).
func (e *Engine) Apply(keys []string) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.scan.Phase == "list" || e.scan.Phase == "read" || e.scan.Phase == "match" {
		return 0, errors.New("attends la fin de l'analyse")
	}
	queued := map[string]bool{}
	for _, k := range e.queue {
		queued[k] = true
	}
	n := 0
	for _, k := range keys {
		r := e.rows[k]
		if r == nil || r.Match == nil || queued[k] {
			continue
		}
		// « Noms différents » : seulement si le client sait renommer.
		if st := e.statusOf(r); st != StAdd && !(st == StRenamed && r.CanAdd) {
			continue
		}
		r.Status = StQueued
		e.queue = append(e.queue, k)
		queued[k] = true
		n++
	}
	if n > 0 {
		e.touch()
	}
	if n > 0 && !e.applying {
		e.batch, e.batchOK = 0, 0
	}
	e.batch += n
	if n > 0 && !e.applying {
		e.applying = true
		ctx, cancel := context.WithCancel(context.Background())
		e.cancel = cancel
		go e.runQueue(ctx)
	}
	return n, nil
}

func (e *Engine) runQueue(ctx context.Context) {
	defer func() {
		e.mu.Lock()
		e.applying, e.current = false, ""
		for _, k := range e.queue { // arrêt : ce qui restait redevient ajoutable
			if r := e.rows[k]; r != nil && r.Status == StQueued {
				r.Status = StAdd
			}
		}
		e.queue = nil
		e.touch()
		e.mu.Unlock()
	}()
	for {
		e.mu.Lock()
		if len(e.queue) == 0 || ctx.Err() != nil {
			e.mu.Unlock()
			return
		}
		k := e.queue[0]
		e.queue = e.queue[1:]
		r := e.rows[k]
		if r == nil {
			e.mu.Unlock()
			continue
		}
		e.current = r.Name
		row := *r
		e.mu.Unlock()

		status, detail := e.addOne(ctx, &row)

		e.mu.Lock()
		if cur := e.rows[k]; cur != nil {
			cur.Status, cur.Detail = status, detail
		}
		e.batchOK++
		e.touch()
		e.mu.Unlock()

		select {
		case <-ctx.Done():
			return
		case <-time.After(downloadGap):
		}
	}
}

// addOne : télécharge le .torrent V3X, le revérifie, l'ajoute à l'arrêt et
// demande la vérification, dans le client qui a la copie. Renvoie le statut
// de la ligne.
func (e *Engine) addOne(ctx context.Context, r *Row) (string, string) {
	again := StAdd // rien n'a été fait : la ligne reste ajoutable
	if r.Match != nil && r.Match.Kind == "renamed" {
		again = StRenamed
	}
	env, err := e.clients()
	if err != nil {
		return again, err.Error()
	}
	tg := env.Get(r.Client)
	if tg == nil {
		return again, env.Unreachable(r.Client).Error()
	}
	if r.Match != nil && r.Match.Kind == "renamed" {
		return e.addRenamed(ctx, env, *tg, r)
	}
	m := r.Match
	raw, err := env.V3X.DownloadTorrent(ctx, &v3x.Retry{}, m.ID)
	if err != nil {
		return StAdd, err.Error()
	}
	t, err := torrent.Parse(raw)
	if err != nil {
		return StAdd, "le .torrent reçu de V3X est illisible"
	}
	if !strings.EqualFold(t.InfoHash, m.InfoHash) {
		return StAdd, "le .torrent reçu ne correspond pas à la fiche (info-hash différent)"
	}
	// Ceinture : le site a dit « mêmes fichiers », on le constate soi-même.
	if r.sig == nil || t.ContentHash != r.sig.ContentHash {
		return StRenamed, "contenu différent de ta copie après vérification"
	}
	save, ok := saveDir(r.contentPath, t.Name)
	if !ok && len(t.Files) == 1 {
		save, ok = saveDir(r.contentPath, t.Files[0].Path)
	}
	if !ok {
		// Ta copie a été renommée dans le client : mêmes fichiers, autres
		// noms sur le disque.
		return e.addRenamed(ctx, env, *tg, r)
	}
	// Ta copie a disparu du disque (supprimée, déplacée par un outil de
	// médiathèque) alors que le client la croit complète : la vérification
	// tomberait à 0 %. On le dit au lieu d'ajouter.
	if exists, supported, err := tg.Client.Lookup(ctx, r.contentPath); err == nil && supported && !exists {
		return StMissing, missingDetail
	}
	if hash, err := e.AddAndVerify(ctx, *tg, WithCategory(env.Cfg, r.category), r.Key, m.ID, raw, save); err != nil {
		switch {
		case errors.Is(err, ErrInClient):
			return StInClient, ""
		case hash == "":
			return StAdd, err.Error() // rien n'a été ajouté : on pourra réessayer
		}
		return StFailed, err.Error()
	}
	return StChecking, ""
}

// saveDir : le dossier où le client doit trouver la release. rel est la fin
// attendue de l'emplacement (« Racine », ou « Racine/fichier » : pour un
// torrent d'un seul fichier rangé dans un dossier, qBittorrent donne le
// chemin du fichier lui-même).
func saveDir(contentPath, rel string) (string, bool) {
	p := strings.TrimRight(contentPath, `/\`)
	parts := strings.Split(rel, "/")
	j := -1
	for i := len(parts) - 1; i >= 0; i-- {
		j = strings.LastIndexAny(p, `/\`)
		if j < 0 || p[j+1:] != parts[i] {
			return "", false
		}
		if i > 0 {
			p = p[:j]
		}
	}
	dir := p[:j]
	switch {
	case dir == "":
		dir = p[:j+1] // racine Unix « / »
	case strings.HasSuffix(dir, ":"):
		dir += `\` // racine d'un lecteur Windows
	}
	return dir, true
}

// ---------- surveillance des vérifications ----------

func (e *Engine) watchLoop() {
	var failedAt time.Time
	for {
		time.Sleep(watchEvery)
		// Les vérifications en cours, client par client. Les ajouts laissés à
		// l'arrêt (« Vérification incomplète ») sont relus une fois par minute :
		// retirés du client à la main, leur ligne restait en erreur dans D3X
		// (Bobobobobo123, 25/10/26).
		withFailed := time.Since(failedAt) >= failedEvery
		if withFailed {
			failedAt = time.Now()
		}
		e.mu.Lock()
		byClient := map[string][]string{}
		for h, w := range e.watches {
			if w.Status == StChecking || (withFailed && w.Status == StFailed) {
				byClient[w.Client] = append(byClient[w.Client], h)
			}
		}
		e.mu.Unlock()
		if len(byClient) == 0 {
			continue
		}
		env, err := e.clients()
		if err != nil {
			continue
		}
		for id, hashes := range byClient {
			t := env.Get(id)
			if t == nil && id != "" && env.Cfg != nil && env.Cfg.Client(id) == nil {
				// Client retiré des réglages : plus personne à surveiller.
				for _, h := range hashes {
					e.mu.Lock()
					if w := e.watches[h]; w != nil {
						w.Status, w.Detail = StRemoved, "client retiré des réglages avant la fin de la vérification"
					}
					e.mu.Unlock()
					e.forget(h)
				}
				continue
			}
			if t == nil {
				continue // client injoignable : on attend qu'il revienne
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			lst, err := t.Client.List(ctx, hashes)
			if err != nil {
				cancel()
				continue
			}
			byHash := map[string]client.Torrent{}
			for _, x := range lst {
				byHash[x.Hash] = x
				for _, h := range x.Hashes {
					byHash[h] = x
				}
			}
			for _, h := range hashes {
				e.step(ctx, t.Client, h, byHash)
			}
			cancel()
		}
		e.mu.Lock()
		e.saveWatchesLocked()
		e.touch()
		e.mu.Unlock()
	}
}

func (e *Engine) step(ctx context.Context, q client.Client, h string, byHash map[string]client.Torrent) {
	e.mu.Lock()
	w := e.watches[h]
	e.mu.Unlock()
	if w == nil {
		return
	}
	t, ok := byHash[h]
	set := func(status, detail string) {
		e.mu.Lock()
		w.Status, w.Detail = status, detail
		e.mu.Unlock()
	}
	e.mu.Lock()
	if ok {
		w.MissSince = time.Time{}
	} else if w.MissSince.IsZero() {
		w.MissSince = time.Now()
	}
	missing := !ok && time.Since(w.MissSince) >= missGrace
	failed := w.Status == StFailed
	e.mu.Unlock()
	// Laissé à l'arrêt : on ne guette que son retrait du client, rien d'autre
	// ne change sans un « Revérifier ».
	if failed {
		if missing {
			set(StRemoved, "retiré du client, fichiers intacts")
			journal.Add(journal.Event{Kind: "removed", Name: w.Name, InfoHash: h, ID: w.ID, Detail: w.Detail})
			e.forget(h)
		}
		return
	}
	switch {
	case !ok && !missing:
		// Peut-être un client qui redémarre : on attend avant de conclure.
	case !ok:
		set(StRemoved, "retiré du client")
		e.forget(h)
	case t.Downloading && t.Progress < 1:
		// Démarré tout seul alors que les données ne sont pas complètes
		// (client trop ancien pour « ajouter à l'arrêt ») : on l'arrête
		// avant qu'il ne télécharge.
		_ = q.Stop(ctx, h)
		set(StFailed, "données incomplètes ("+pct(t.Progress)+") : arrêté avant tout téléchargement")
		journal.Add(journal.Event{Kind: "crossseed_failed", Name: w.Name, InfoHash: h, ID: w.ID, Detail: w.Detail})
	case t.Checking:
		e.mu.Lock()
		if !w.SawChecking {
			w.CheckFrom, w.CheckMoved = t.Progress, false
		} else if t.Progress != w.Progress {
			w.CheckMoved = true
		}
		w.SawChecking, w.Progress = true, t.Progress
		w.EndAt = time.Time{}
		e.mu.Unlock()
	case t.Progress >= 1:
		e.mu.Lock()
		held := e.heldLocked(w)
		if held {
			w.Status, w.Detail, w.Progress = StChecking, HeldDetail, 1
		}
		e.mu.Unlock()
		if held {
			return
		}
		if err := q.Start(ctx, h); err != nil {
			set(StChecking, "vérifié à 100 %, démarrage refusé : "+err.Error())
			return
		}
		set(StSeeding, "")
		journal.Add(journal.Event{Kind: "crossseed", Name: w.Name, InfoHash: h, ID: w.ID, Detail: "vérifié à 100 %, en seed"})
		e.forget(h)
	case w.SawChecking:
		// Vérification finie sous 100 % : retenue seulement une fois stable.
		e.mu.Lock()
		if w.EndAt.IsZero() || t.Progress != w.EndProgress {
			w.EndAt, w.EndProgress = time.Now(), t.Progress
			e.mu.Unlock()
			return
		}
		stable := time.Since(w.EndAt) >= endStable
		minP := w.MinProgress
		// Rien de vérifié : arrêté à 0 % pile, ou là où la vérification l'avait
		// trouvé, sans que la progression ait bougé. « En vérification »
		// couvre aussi la file d'attente (checkingDL) : un torrent qui en sort
		// sans avoir été vérifié (client arrêté pendant l'attente) n'a pas des
		// fichiers différents, sa vérification s'est perdue (bertho-zero,
		// ticket n°3, 27/10/26 : 53 ajouts de la Médiathèque marqués « vérifié
		// à 0,0 % » au redémarrage, données bonnes). Relancée une fois avant
		// de conclure.
		lost := !w.Relaunched && (t.Progress <= 0 || (!w.CheckMoved && t.Progress == w.CheckFrom))
		e.mu.Unlock()
		if !stable {
			return
		}
		// Release à compléter : ce qui manque n'est que les morceaux des petits
		// fichiers annexes, le client les télécharge puis seede (MiTor).
		if minP > 0 && t.Progress+1e-6 >= minP {
			if err := q.Start(ctx, h); err != nil {
				set(StChecking, "vérifié à "+pct(t.Progress)+", démarrage refusé : "+err.Error())
				return
			}
			set(StSeeding, "vérifié à "+pct(t.Progress)+" : les petits fichiers manquants se téléchargent, puis il seede")
			journal.Add(journal.Event{Kind: "crossseed", Name: w.Name, InfoHash: h, ID: w.ID, Detail: w.Detail})
			e.forget(h)
			return
		}
		if lost {
			// Comme une vérification jamais vue : relancée, puis suivie par la
			// branche du bas si le client ne la lance pas.
			_ = q.Recheck(ctx, h)
			e.mu.Lock()
			w.Relaunched, w.SawChecking, w.CheckMoved, w.CheckFrom = true, false, false, 0
			w.EndAt, w.EndProgress, w.Rechecks, w.RecheckAt = time.Time{}, 0, 0, time.Now()
			e.mu.Unlock()
			return
		}
		set(StFailed, "vérifié à "+pct(t.Progress)+" : tes fichiers diffèrent de la release V3X, le torrent reste arrêté")
		journal.Add(journal.Event{Kind: "crossseed_failed", Name: w.Name, InfoHash: h, ID: w.ID, Detail: w.Detail})
	default:
		// Jamais vu en vérification : la demande s'est perdue (client pas encore
		// prêt juste après l'ajout, retour de V3x0r le 21/10/26 : des releases
		// identiques morceau par morceau marquées « vérifié à 0 % »). On relance.
		e.mu.Lock()
		last := w.RecheckAt
		if last.IsZero() {
			last = w.Since
		}
		due := time.Since(last) >= recheckGap
		tries := w.Rechecks
		e.mu.Unlock()
		if !due {
			return
		}
		// Copie absente du disque : inutile de relancer, on le dit.
		e.mu.Lock()
		cp := ""
		if r := e.rows[w.Key]; r != nil {
			cp = r.contentPath
		}
		e.mu.Unlock()
		if cp != "" {
			if exists, supported, err := q.Lookup(ctx, cp); err == nil && supported && !exists {
				set(StFailed, missingDetail+" Retire cet ajout du client.")
				journal.Add(journal.Event{Kind: "crossseed_failed", Name: w.Name, InfoHash: h, ID: w.ID, Detail: w.Detail})
				return
			}
		}
		if tries < maxRechecks {
			_ = q.Recheck(ctx, h)
			e.mu.Lock()
			w.Rechecks, w.RecheckAt = tries+1, time.Now()
			e.mu.Unlock()
			return
		}
		set(StFailed, "ton client n'a pas lancé la vérification : relance-la avec « Revérifier »")
		journal.Add(journal.Event{Kind: "crossseed_failed", Name: w.Name, InfoHash: h, ID: w.ID, Detail: w.Detail})
	}
}

// missingDetail : ta copie n'est plus là où le client la voit.
const missingDetail = "ta copie n'est plus à cet endroit (supprimée, ou déplacée par un outil de médiathèque) alors que ton client la croit complète. Si elle a été rangée ailleurs, la Médiathèque de D3X la retrouve."

// Rythme de la surveillance des vérifications (raccourci dans les tests).
var (
	recheckGap  = 30 * time.Second // avant de relancer une vérification jamais vue
	maxRechecks = 3
	endStable   = 20 * time.Second // fin sous 100 % stable avant de conclure
	missGrace   = 2 * time.Minute  // absent du client aussi longtemps avant de le dire retiré
	failedEvery = time.Minute      // relecture des ajouts laissés à l'arrêt
)

// Recheck relance la vérification d'un ajout marqué en échec (bouton
// « Revérifier ») : il repasse sous surveillance et démarrera à 100 %.
func (e *Engine) Recheck(key string) error {
	e.mu.Lock()
	var hash, cid string
	for h, w := range e.watches {
		if w.Key == key && w.Status == StFailed {
			hash, cid = h, w.Client
		}
	}
	e.mu.Unlock()
	if hash == "" {
		return errors.New("rien à revérifier pour cette release")
	}
	env, err := e.clients()
	if err != nil {
		return err
	}
	t := env.Get(cid)
	if t == nil {
		return env.Unreachable(cid)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := t.Client.Recheck(ctx, hash); err != nil {
		return errors.New("vérification impossible à lancer : " + err.Error())
	}
	e.mu.Lock()
	if w := e.watches[hash]; w != nil {
		w.Status, w.Detail, w.Progress = StChecking, "", 0
		w.Since, w.SawChecking, w.Rechecks, w.RecheckAt, w.EndAt = time.Now(), false, 0, time.Time{}, time.Time{}
		w.CheckFrom, w.CheckMoved, w.Relaunched = 0, false, false
		if r := e.rows[w.Key]; r != nil {
			r.Status, r.Detail = StChecking, ""
		}
	}
	delete(e.final, hash)
	e.saveWatchesLocked()
	e.touch()
	e.mu.Unlock()
	return nil
}

// pct écrit un pourcentage à la française (« 62,4 % »).
func pct(p float64) string {
	return strings.Replace(fmt.Sprintf("%.1f", p*100), ".", ",", 1) + " %"
}

// forget garde le statut final sur la ligne et sort l'ajout de la surveillance.
func (e *Engine) forget(h string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	w := e.watches[h]
	if w == nil {
		return
	}
	if r := e.rows[w.Key]; r != nil {
		r.Status, r.Detail = w.Status, w.Detail
	}
	e.final[h] = WatchState{Status: w.Status, Progress: w.Progress, Detail: w.Detail}
	delete(e.watches, h)
}

// Busy dit si le moteur travaille : file d'ajout ou vérifications en cours.
func (e *Engine) Busy() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.applying || e.lib.applying || e.libScanning() || e.scan.Phase == "list" || e.scan.Phase == "read" || e.scan.Phase == "match" {
		return true
	}
	for _, w := range e.watches {
		if w.Status == StChecking {
			return true
		}
	}
	return false
}

// WatchState est l'état d'un ajout suivi (ou terminé).
type WatchState struct {
	Status   string  `json:"status"`
	Progress float64 `json:"progress"`
	Detail   string  `json:"detail,omitempty"`
}

// Watch met sous surveillance un torrent qu'on vient d'ajouter À L'ARRÊT au
// client clientID et dont la vérification est lancée : il sera démarré à
// 100 %, laissé arrêté sinon. Sert aussi à la publication (seed de sa propre
// release).
// allowMissing : cet ajout démarre aussi sous 100 %, dès que sa vérification
// atteint min (release de la médiathèque à compléter : le client télécharge
// ensuite les petits fichiers manquants).
func (e *Engine) allowMissing(hash string, min float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if w := e.watches[strings.ToLower(hash)]; w != nil {
		w.MinProgress = min
		e.saveWatchesLocked()
	}
}

func (e *Engine) Watch(clientID, key, name, id, hash string) {
	hash = strings.ToLower(hash)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.watches[hash] = &watch{Client: clientID, Key: key, Name: name, ID: id, InfoHash: hash, Since: time.Now(), Status: StChecking}
	delete(e.final, hash)
	e.saveWatchesLocked()
}

// Seed à la fin du lot (karat, 22/10/26) : pendant qu'un lot hache les
// releases suivantes, une release publiée et vérifiée à 100 % reste à l'arrêt,
// pour que son seed ne lise pas le disque en même temps que le hachage. La
// page du lot renouvelle ce bail tant qu'elle tourne et le lève à la fin ;
// page fermée ou D3X redémarré, il expire seul et les seeds démarrent.

// HeldDetail : publication vérifiée, gardée à l'arrêt jusqu'à la fin du lot.
const HeldDetail = "vérifiée à 100 %, démarre à la fin du lot"

// MaxHold : bail le plus long qu'une page peut demander d'un coup.
const MaxHold = 15 * time.Minute

// HoldPublished garde les publications vérifiées à l'arrêt pendant d ; 0
// lève le bail : elles démarrent au prochain passage de la surveillance.
func (e *Engine) HoldPublished(d time.Duration) {
	if d > MaxHold {
		d = MaxHold
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if d <= 0 {
		e.holdPub = time.Time{}
		return
	}
	e.holdPub = time.Now().Add(d)
}

// heldLocked : cet ajout est-il une publication à garder à l'arrêt pour
// l'instant ? Les cross-seeds ne sont jamais retenus.
func (e *Engine) heldLocked(w *watch) bool {
	return strings.HasPrefix(w.Key, "pub:") && time.Now().Before(e.holdPub)
}

// WatchOf renvoie l'état d'un ajout suivi, ou terminé depuis le lancement.
func (e *Engine) WatchOf(hash string) (WatchState, bool) {
	hash = strings.ToLower(hash)
	e.mu.Lock()
	defer e.mu.Unlock()
	if w := e.watches[hash]; w != nil {
		return WatchState{Status: w.Status, Progress: w.Progress, Detail: w.Detail}, true
	}
	st, ok := e.final[hash]
	return st, ok
}

// CategoryFor applique le modèle de catégorie des ajouts (idée de JohnnyJohn28,
// 26/10/26) : « {cat} » y est remplacé par la catégorie de la copie d'origine
// dans le client, pour ranger un cross-seed à côté d'elle (« {cat}.cross » :
// tv → tv.cross, movies → movies.cross). Une copie déjà rangée selon le modèle
// le garde telle quelle (pas de movies.cross.cross) ; sans catégorie d'origine
// (médiathèque, autre client), il reste le texte autour : « cross ».
func CategoryFor(tpl, src string) string {
	i := strings.Index(tpl, "{cat}")
	if i < 0 {
		return tpl
	}
	pre, suf := tpl[:i], strings.ReplaceAll(tpl[i+len("{cat}"):], "{cat}", "")
	src = strings.TrimSpace(src)
	if src == "" {
		return strings.Trim(pre+suf, " ./-_")
	}
	if (pre != "" || suf != "") && strings.HasPrefix(src, pre) && strings.HasSuffix(src, suf) && len(src) > len(pre)+len(suf) {
		return src
	}
	return pre + src + suf
}

// WithCategory : la configuration, catégorie du modèle résolue pour la copie
// d'origine src. Inchangée sans « {cat} ».
func WithCategory(cfg *config.Config, src string) *config.Config {
	if cfg == nil || !strings.Contains(cfg.Category, "{cat}") {
		return cfg
	}
	c := *cfg
	c.Category = CategoryFor(cfg.Category, src)
	return &c
}

// AddAndVerify ajoute un .torrent à l'arrêt sur des données existantes, au
// client tg, lance la vérification et le met sous surveillance. savePath est
// vu par ce client.
func (e *Engine) AddAndVerify(ctx context.Context, tg Target, cfg *config.Config, key, id string, raw []byte, savePath string) (string, error) {
	q := tg.Client
	t, err := torrent.Parse(raw)
	if err != nil {
		return "", errors.New("le .torrent reçu de V3X est illisible")
	}
	hash := strings.ToLower(t.InfoHash)
	if lst, err := q.List(ctx, []string{hash}); err == nil && len(lst) > 0 {
		return hash, ErrInClient
	}
	if err := q.Add(ctx, "[V3X] "+t.Name+".torrent", raw, client.AddOptions{SavePath: savePath, Category: cfg.Category, Tag: cfg.Tag}); err != nil {
		if errors.Is(err, client.ErrAlreadyThere) {
			return hash, ErrInClient
		}
		return "", err
	}
	found := false
	for i := 0; i < 30 && !found; i++ {
		if lst, err := q.List(ctx, []string{hash}); err == nil && len(lst) > 0 {
			found = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !found {
		return hash, errors.New("le client n'a pas confirmé l'ajout")
	}
	if err := q.Recheck(ctx, hash); err != nil {
		return hash, errors.New("vérification impossible à lancer : " + err.Error())
	}
	e.Watch(tg.ID, key, t.Name, id, hash)
	return hash, nil
}

// ErrInClient : ce torrent est déjà dans le client.
var ErrInClient = errors.New("ce torrent est déjà dans le client")

// Remove retire du client un ajout dont la vérification a échoué. Les
// fichiers ne sont jamais touchés.
func (e *Engine) Remove(key string) error {
	e.mu.Lock()
	var hash, cid string
	for h, w := range e.watches {
		if w.Key == key && w.Status == StFailed {
			hash, cid = h, w.Client
		}
	}
	e.mu.Unlock()
	if hash == "" {
		return errors.New("rien à retirer pour cette release")
	}
	env, err := e.clients()
	if err != nil {
		return err
	}
	t := env.Get(cid)
	if t == nil {
		return env.Unreachable(cid)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := t.Client.RemoveKeepFiles(ctx, hash); err != nil {
		// Déjà retiré du client à la main : certains clients refusent de
		// retirer un torrent qu'ils n'ont plus, la ligne passe quand même en
		// « Retiré » (Bobobobobo123, 25/10/26).
		lst, lerr := t.Client.List(ctx, []string{hash})
		if lerr != nil || hasHash(lst, hash) {
			return err
		}
	}
	e.mu.Lock()
	if w := e.watches[hash]; w != nil {
		w.Status, w.Detail = StRemoved, "retiré du client, fichiers intacts"
		journal.Add(journal.Event{Kind: "removed", Name: w.Name, InfoHash: hash, ID: w.ID, Detail: w.Detail})
	}
	e.mu.Unlock()
	e.forget(hash)
	e.mu.Lock()
	e.saveWatchesLocked()
	e.touch()
	e.mu.Unlock()
	return nil
}

// hasHash : ce torrent figure-t-il dans la liste du client (v1 ou v2) ?
func hasHash(lst []client.Torrent, h string) bool {
	for _, t := range lst {
		if strings.EqualFold(t.Hash, h) {
			return true
		}
		for _, x := range t.Hashes {
			if strings.EqualFold(x, h) {
				return true
			}
		}
	}
	return false
}

func (e *Engine) loadWatches() {
	p, err := config.Path("pending.json")
	if err != nil {
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var list []*watch
	if json.Unmarshal(b, &list) == nil {
		for _, w := range list {
			// Une fin de vérification vue avant l'arrêt de D3X n'a pas été
			// suivie pendant l'arrêt : sa stabilité se constate de nouveau,
			// le temps qu'un client relancé avec lui se pose (ticket n°3).
			w.EndAt = time.Time{}
			e.watches[w.InfoHash] = w
		}
	}
}

func (e *Engine) saveWatchesLocked() {
	list := make([]*watch, 0, len(e.watches))
	for _, w := range e.watches {
		list = append(list, w)
	}
	if b, err := json.Marshal(list); err == nil {
		_ = config.WriteFileAtomic("pending.json", b)
	}
}
