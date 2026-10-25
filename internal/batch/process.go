package batch

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/V3x0rV3x/D3X/internal/journal"
	"github.com/V3x0rV3x/D3X/internal/mediainfo"
	"github.com/V3x0rV3x/D3X/internal/musicbrainz"
	"github.com/V3x0rV3x/D3X/internal/publish"
	"github.com/V3x0rV3x/D3X/internal/source"
)

const held = "vérifiée à 100 %, démarre à la fin du lot"

func replaceHeld(s string) string { return strings.ReplaceAll(s, held, "seed lancé à la fin du lot") }

// waitJob interroge la publication jusqu'à done. hard : l'arrêt du lot
// n'interrompt pas l'attente (publication en cours : on attend sa fin).
func (e *Engine) waitJob(r *Run, id string, done func(*publish.Job) bool, hard bool) (*publish.Job, error) {
	for {
		j, ok := e.deps.PubGet(id)
		if !ok {
			return nil, fmt.Errorf("publication inconnue")
		}
		if done(j) {
			return j, nil
		}
		if !hard && e.stopped() {
			return j, ErrStopped
		}
		detail := ""
		if j.State == publish.StHashing {
			detail = "hachage " + strconv.Itoa(int(math.Round(float64(j.Hashed)/float64(max(j.Total, 1))*100))) + " %"
		}
		e.progress(fmt.Sprintf("Lot : %d / %d", r.Done+1, r.Total), detail)
		if hard {
			time.Sleep(700 * time.Millisecond)
		} else if !e.sleep(700 * time.Millisecond) {
			return j, ErrStopped
		}
	}
}

// mediaInfo : pendant le hachage, par le binaire ou par une page. "" si illisible.
func (e *Engine) mediaInfo(ctx context.Context, r *Run, j *publish.Job) string {
	if j.MainFile < 0 || j.MainFile >= len(j.Files) {
		return ""
	}
	req := mediainfo.Request{JobID: j.ID, FileIndex: j.MainFile, Size: j.Files[j.MainFile].Size, Source: r.Source}
	if p, ok := e.deps.PubFilePath(j.ID, j.MainFile); ok {
		req.Path = p
	}
	text, err := e.deps.MediaInfo(ctx, req)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(text)
}

// localNfo : le .nfo de la release, lu sur place (option « Reprendre le .nfo »).
func (e *Engine) localNfo(ctx context.Context, j *publish.Job) string {
	files := make([]File, len(j.Files))
	for i, f := range j.Files {
		files[i] = File{Rel: f.Rel, Size: f.Size}
	}
	i := pickNfo(j.Name, files)
	if i < 0 {
		return ""
	}
	b, _, err := e.deps.PubRead(ctx, j.ID, i, 0, files[i].Size)
	if err != nil {
		return ""
	}
	return strings.TrimRight(strings.ReplaceAll(decodeNfo(b), "\r\n", "\n"), " \t\r\n")
}

type mbWork struct {
	mb  *musicbrainz.Candidate
	why string
}

// process traite une release, comme processEntry dans la page.
func (e *Engine) process(ctx context.Context, r *Run, src source.Source, en *Entry) {
	o := r.Options
	if en.State == "ready" {
		e.processReady(ctx, r, src, en)
		return
	}
	switch {
	case en.Exec:
		e.decide(src, en, "review", "contient un exécutable : jamais publié par un lot", "")
		return
	case o.Video && !en.Video && !en.Audio:
		e.decide(src, en, "review", "ni vidéo ni album (option « Vidéos et albums seulement »)", "")
		return
	case o.Cap > 0 && float64(en.Size) > o.Cap*gib:
		e.decide(src, en, "review", "au-delà du plafond de "+fmtNum(o.Cap)+" Gio : pas hachée", "")
		return
	case en.Flat:
		e.decide(src, en, "review", "dossier sans fichier à sa racine (collection, plusieurs releases ?)", "")
		return
	}

	j, err := e.deps.PubStart(src, en.Path)
	if err != nil {
		e.decide(src, en, "error", err.Error(), "")
		return
	}
	cancel := func() { e.deps.PubCancel(j.ID) }
	if j.Duplicate != nil {
		cancel()
		e.decide(src, en, "duplicate", dupText(j.Duplicate), j.Duplicate.ID)
		return
	}
	if j.Gone != nil {
		cancel()
		e.decide(src, en, "review", goneText(j.Gone)+" : jamais republiée par un lot, « Revoir » si la raison ne s'applique plus", "")
		return
	}

	// MediaInfo pendant le hachage.
	mi := e.mediaInfo(ctx, r, j)
	// Arrêt pendant une attente : le job est annulé et la release reste dans
	// son état, sans « Erreur » ni ligne au journal (la page la marquait en erreur).
	if e.stopped() {
		cancel()
		return
	}
	name := stripExt(j.Name, j.Single)
	album := j.Audio != nil && len(j.Audio.Tracks) > 0 && !en.Video

	cx, err := e.deps.Context(ctx)
	if err != nil {
		cancel()
		e.decide(src, en, "error", err.Error(), "")
		return
	}
	// Album : Audio ▸ Musique et l'édition MusicBrainz, si elle est certaine.
	var mc *Category
	var mbw *mbWork
	if album {
		_, mc = cx.musicCategory()
		res, err := e.deps.MBDecide(ctx, j.ID)
		switch {
		case err != nil:
			mbw = &mbWork{why: "MusicBrainz injoignable (" + err.Error() + ")"}
		case res.Certain && res.Best != nil:
			mbw = &mbWork{mb: res.Best, why: res.Reason}
		default:
			why := res.Reason
			if why == "" {
				why = "album introuvable sur MusicBrainz"
			}
			mbw = &mbWork{why: why}
		}
	}
	ficheOf := func(ref *string) (*Fiche, error) {
		in := FicheInput{ReleaseName: name, MediaInfo: mi, TmdbRef: ref}
		if mbw != nil && mbw.mb != nil {
			u := mbURL(mbw.mb.ID)
			in.MbRef = &u
		}
		if mc != nil {
			s := mc.Slug
			in.CategorySlug = &s
		}
		return e.deps.Fiche(ctx, j.ID, in)
	}
	f, err := ficheOf(nil)
	if err != nil {
		cancel()
		e.decide(src, en, "error", err.Error(), "")
		return
	}

	// Œuvre : série déjà choisie à la main, sinon TMDB, retenue seulement si certaine.
	year := f.yearToken()
	isSeries := f.Season != "" || reSeason.MatchString(name)
	var work *Work
	if isSeries && !album {
		work = e.deps.RememberedSeries(seriesTitleOf(f))
	}
	if work == nil && f.TmdbQuery != "" && !album {
		if res, err := e.deps.TMDBSearch(ctx, f.TmdbQuery); err == nil {
			// Films et séries seulement, comme la page.
			var mt []Work
			for _, w := range res {
				if w.Type == "movie" || w.Type == "tv" {
					mt = append(mt, w)
				}
			}
			title := f.Analysis.Title
			if title == "" {
				title = f.TmdbQuery
			}
			work = certainWork(mt, title, year, isSeries)
		}
	}
	if work != nil {
		ref := work.Type + "/" + string(work.ID)
		if f, err = ficheOf(&ref); err != nil {
			cancel()
			e.decide(src, en, "error", err.Error(), "")
			return
		}
	}

	// Album sans la qualité dans le nom : complété si l'option est cochée.
	renamed := false
	if album && o.FixName && j.Audio.Token != "" && contains(f.Warnings, "audio_naming") {
		if fixed := nameWithToken(name, j.Audio.Token); fixed != name {
			name, renamed = fixed, true
			if f, err = ficheOf(nil); err != nil {
				cancel()
				e.decide(src, en, "error", err.Error(), "")
				return
			}
		}
	}

	var reasons []string
	cat := f.Category
	if cat == nil {
		reasons = append(reasons, "catégorie à choisir")
	}
	// La page ne le notait que pour un film, une série ou une vidéo, et
	// plantait sinon plus bas (work.title sur null) : ici, à vérifier, proprement.
	if work == nil && !album {
		reasons = append(reasons, "œuvre TMDB incertaine")
	}
	if album && mbw.mb == nil {
		reasons = append(reasons, "album incertain : "+mbw.why)
	}
	if album && j.Audio.Mixed {
		reasons = append(reasons, "pistes de qualités différentes")
	}
	if en.Video && mi == "" {
		reasons = append(reasons, "MediaInfo illisible")
	}
	// Seuls les contrôles qui feraient refuser la publication arrêtent le lot.
	for _, w := range f.Warnings {
		if !blocking[w] {
			continue
		}
		if w == "audio_naming" && j.Audio != nil && j.Audio.Token != "" {
			reasons = append(reasons, "nom sans la qualité : « "+nameWithToken(name, j.Audio.Token)+" » ?")
		} else {
			reasons = append(reasons, strings.TrimSuffix(warnings[w], "."))
		}
	}
	// REPACK (ou PROPER) déjà sur V3X : la version d'origine ne part pas seule.
	tmdbRef, catSlug := "", ""
	if work != nil && !album {
		tmdbRef = work.Type + "/" + string(work.ID)
	}
	if cat != nil {
		catSlug = cat.Slug
	}
	if redo, err := e.deps.Redo(ctx, name, j.Total, tmdbRef, catSlug); err == nil && redo != "" {
		reasons = append(reasons, "un REPACK de cette release est déjà sur V3X : « "+redo+" »")
	}
	if len(reasons) > 0 {
		cancel()
		e.decide(src, en, "review", strings.Join(reasons, " · "), "")
		return
	}

	catName := cx.categoryName(cat.Slug)
	var workName string
	if album {
		workName = mbw.mb.Title
		if len(mbw.mb.Date) >= 4 {
			workName += " (" + mbw.mb.Date[:4] + ")"
		}
	} else {
		workName = work.Title
		if work.Year != "" {
			workName += " (" + string(work.Year) + ")"
		}
	}
	parts := []string{catName, workName}
	if renamed {
		parts = append(parts, "nom complété : "+name)
	}
	summary := strings.Join(parts, " · ")
	if o.Sim {
		cancel() // le .torrent reste en cache : le vrai lot ne rehachera pas
		e.decide(src, en, "simulated", "serait publiée : "+summary, "")
		return
	}

	j, err = e.waitJob(r, j.ID, func(x *publish.Job) bool { return x.State != publish.StHashing }, false)
	if err == ErrStopped {
		cancel()
		return
	}
	if err != nil || j.State != publish.StReady {
		cancel()
		msg := "le .torrent n'a pas pu être fabriqué"
		if err != nil {
			msg = err.Error()
		} else if j.Error != "" {
			msg = j.Error
		}
		e.decide(src, en, "error", msg, "")
		return
	}
	ownNfo := ""
	if o.KeepNfo {
		ownNfo = e.localNfo(ctx, j)
	}
	nfo := f.NFO
	if ownNfo != "" {
		nfo = nfoWithMediaInfo(ownNfo, mi)
	}
	fields := map[string]string{
		"categoryId":        string(cat.ID),
		"description":       f.Presentation,
		"descriptionFormat": "standard",
		"title":             f.Title,
		"releaseName":       name,
		"nfo":               nfo,
		"language":          f.Language,
		"anonymous":         strconv.FormatBool(cx.User.AnonymousDefault),
		"exclusive":         "false",
	}
	if album {
		fields["tmdbUrl"] = mbURL(mbw.mb.ID)
		creator := f.Creator
		if creator == "" {
			creator = mbw.mb.Artist
		}
		fields["creator"] = creator
	} else {
		fields["tmdbUrl"] = "https://www.themoviedb.org/" + work.Type + "/" + string(work.ID)
	}
	if cx.HasTeam() {
		fields["teamRelease"] = strconv.FormatBool(f.TeamRelease)
	}
	if f.Season != "" && strings.Contains(cat.Slug, "serie") {
		fields["season"] = string(f.Season)
	}
	e.publishWith(ctx, r, src, en, j, fields, summary)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// processReady : release gardée depuis Publier, publiée avec sa fiche, sans
// rien redécider.
func (e *Engine) processReady(ctx context.Context, r *Run, src source.Source, en *Entry) {
	key := src.Key(en.Path)
	p, ok := e.deps.Prepared(key)
	if !ok {
		e.decide(src, en, "review", "fiche gardée introuvable : « Revoir » pour la refaire", "")
		return
	}
	summary := p.Summary
	if summary == "" {
		summary = p.Fields["releaseName"]
	}
	if r.Options.Sim {
		e.set(func(*Run) { en.Detail = "serait publiée avec ta fiche : " + summary })
		return
	}
	j, err := e.deps.PubStart(src, en.Path)
	if err != nil {
		e.decide(src, en, "error", err.Error(), "")
		return
	}
	cancel := func() { e.deps.PubCancel(j.ID) }
	if j.Duplicate != nil {
		cancel()
		e.decide(src, en, "duplicate", dupText(j.Duplicate), j.Duplicate.ID)
		return
	}
	j, err = e.waitJob(r, j.ID, func(x *publish.Job) bool { return x.State != publish.StHashing }, false)
	if err == ErrStopped {
		cancel()
		return
	}
	if err != nil || j.State != publish.StReady {
		cancel()
		msg := "le .torrent n'a pas pu être fabriqué"
		if err != nil {
			msg = err.Error()
		} else if j.Error != "" {
			msg = j.Error
		}
		e.decide(src, en, "error", msg, "")
		return
	}
	if e.publishWith(ctx, r, src, en, j, p.Fields, summary) {
		e.deps.DropPrepared(key)
	}
}

// publishWith publie le job prêt avec ces champs ; vrai si V3X l'a acceptée.
func (e *Engine) publishWith(ctx context.Context, r *Run, src source.Source, en *Entry, j *publish.Job, fields map[string]string, summary string) bool {
	if err := e.deps.PubPublish(j.ID, fields); err != nil {
		e.decide(src, en, "error", err.Error(), "")
		return false
	}
	// L'envoi est parti : on attend sa réponse même si le lot s'arrête.
	j2, err := e.waitJob(r, j.ID, func(x *publish.Job) bool { return x.State != publish.StPublishing }, true)
	if err != nil {
		e.decide(src, en, "error", err.Error(), "")
		return false
	}
	if j2.Published == nil {
		e.decide(src, en, "error", pubErr(j2.Error, j2.ErrorBody), "")
		return false
	}
	name := fields["releaseName"]
	if name == "" {
		name = en.Name
	}
	var reqs []byte
	if raw, err := e.deps.RequestsFor(ctx, name); err == nil && len(raw) > 2 {
		reqs = raw
	}
	status := "en attente de validation"
	if j2.Published.Status == "active" {
		status = "publiée"
	}
	e.set(func(r *Run) {
		r.Published++
		r.PublishedBytes += en.Size
		en.Job = j.ID
		en.Last = &journal.Event{ID: j2.Published.ID}
		en.Active = j2.Published.Status == "active"
		en.Requests = reqs
		en.State = "published"
		en.Detail = status + " · " + summary
	})
	return true
}

// waitChecked : option « Attendre la vérification » : la suivante n'est prise
// qu'une fois celle-ci ajoutée au client et vérifiée. « Arrêter » coupe l'attente.
func (e *Engine) waitChecked(r *Run, en *Entry) {
	for {
		j, ok := e.deps.PubGet(en.Job)
		if !ok {
			return
		}
		s := j.Seed
		checking := j.State == publish.StSeeding || (s != nil && s.Status == "checking" && s.Detail == "")
		if !checking || j.SeedError != "" {
			add := ""
			switch {
			case j.SeedError != "":
				add = " · seed : " + j.SeedError
			case s != nil && s.Status == "seeding":
				add = " · vérifiée à 100 %, en seed"
			case s != nil && s.Detail != "":
				add = " · " + s.Detail
			}
			if add != "" {
				e.set(func(*Run) { en.Detail += add })
			}
			return
		}
		if e.stopped() {
			return
		}
		pct := ""
		if s != nil && s.Progress > 0 {
			pct = " (" + strconv.Itoa(int(math.Round(s.Progress*100))) + " %)"
		}
		e.progress(fmt.Sprintf("Lot : %d / %d", r.Done+1, r.Total), "ton client vérifie "+en.Name+pct+"…")
		if !e.sleep(2 * time.Second) {
			return
		}
	}
}
