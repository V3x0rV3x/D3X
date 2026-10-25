package batch

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/V3x0rV3x/D3X/internal/journal"
	"github.com/V3x0rV3x/D3X/internal/source"
)

// Engine mène un lot : les releases cochées, une par une, avec les mêmes
// étapes que la vue Publier, dans D3X lui-même (la page ne fait que
// démarrer, arrêter et suivre). Un seul lot à la fois.
type Engine struct {
	deps  Deps
	pause time.Duration // entre deux publications (le site n'aime pas les rafales)

	mu     sync.Mutex
	run    *Run
	src    source.Source
	stopc  chan struct{}
	cancel context.CancelFunc
}

// New prépare le moteur.
func New(d Deps) *Engine { return &Engine{deps: d, pause: 7 * time.Second} }

// ErrStopped : le membre a arrêté le lot pendant une attente.
var ErrStopped = errors.New("lot arrêté")

var stateOfKind = map[string]string{"published": "published", "batch_ready": "ready", "batch_review": "review", "batch_simulated": "simulated", "batch_duplicate": "duplicate", "batch_ignored": "ignored", "batch_error": "error"}
var kindOfState = map[string]string{"review": "batch_review", "simulated": "batch_simulated", "duplicate": "batch_duplicate", "ignored": "batch_ignored", "error": "batch_error"}

// Start démarre un lot. Erreur si un lot tourne déjà.
func (e *Engine) Start(spec Spec) (*Run, error) {
	src, err := e.deps.Source(spec.Source)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	list, err := e.deps.Entries(ctx, src, spec.Paths)
	if err != nil {
		return nil, err
	}
	last := e.deps.LastByPath()
	r := &Run{Dir: spec.Dir, Source: spec.Source, Options: spec.Options, State: StRunning, StartedAt: time.Now()}
	for _, x := range list {
		en := &Entry{Entry: x, State: "pending"}
		if ev, ok := last[src.Key(x.Path)]; ok {
			ev := ev
			en.Last = &ev
			if st := stateOfKind[ev.Kind]; st != "" {
				en.State, en.Detail = st, ev.Detail
			}
		}
		if x.Error != "" {
			en.State, en.Detail = "error", x.Error
		}
		r.Entries = append(r.Entries, en)
	}
	e.mu.Lock()
	if e.run != nil && e.run.State != StDone {
		e.mu.Unlock()
		return nil, errors.New("un lot tourne déjà")
	}
	for _, en := range r.Entries {
		if todo(en) {
			r.Total++
		}
	}
	rctx, rcancel := context.WithCancel(context.Background())
	e.run, e.src, e.stopc, e.cancel = r, src, make(chan struct{}), rcancel
	out := e.copyLocked()
	e.mu.Unlock()
	go e.loop(rctx, r, src)
	return out, nil
}

// todo : la release est à traiter (comme la page : cochée et dans un état
// qui le permet ; ici, cochée = envoyée par la page).
func todo(en *Entry) bool {
	switch en.State {
	case "pending", "ready", "review", "simulated":
		return true
	}
	return false
}

// Stop arrête le lot : l'attente en cours est coupée, l'étape en cours se
// termine proprement, aucune release suivante n'est prise.
func (e *Engine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.run == nil || e.run.State != StRunning {
		return
	}
	e.run.State, e.run.Stopped = StStopping, true
	close(e.stopc)
	e.cancel()
}

// Running : un lot tourne (ou s'arrête).
func (e *Engine) Running() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.run != nil && e.run.State != StDone
}

// State : une copie du lot (en cours ou le dernier fini), nil s'il n'y en a jamais eu.
func (e *Engine) State() *Run {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.copyLocked()
}

func (e *Engine) copyLocked() *Run {
	if e.run == nil {
		return nil
	}
	c := *e.run
	c.Entries = make([]*Entry, len(e.run.Entries))
	for i, en := range e.run.Entries {
		x := *en
		if en.Last != nil {
			l := *en.Last
			x.Last = &l
		}
		c.Entries[i] = &x
	}
	return &c
}

func (e *Engine) stopped() bool {
	select {
	case <-e.stopc:
		return true
	default:
		return false
	}
}

// set modifie le run sous verrou.
func (e *Engine) set(f func(r *Run)) {
	e.mu.Lock()
	f(e.run)
	e.mu.Unlock()
}

func (e *Engine) progress(label, detail string) {
	e.set(func(r *Run) { r.Label, r.Detail = label, detail })
}

// sleep attend d, ou l'arrêt.
func (e *Engine) sleep(d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-e.stopc:
		return false
	}
}

// decide range la release et le note au journal (comme la route decision).
func (e *Engine) decide(src source.Source, en *Entry, state, detail, id string) {
	e.set(func(r *Run) { en.State, en.Detail = state, detail })
	if kind := kindOfState[state]; kind != "" {
		e.deps.Journal(journal.Event{Kind: kind, Name: en.Name, Path: src.Key(en.Path), Detail: detail, ID: id})
	}
}

const gib = float64(1 << 30)

func fmtNum(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func (e *Engine) loop(ctx context.Context, r *Run, src source.Source) {
	o := r.Options
	hold := o.SeedLater && !o.Sim
	holdDone := make(chan struct{})
	if hold {
		e.deps.HoldPublished(5 * time.Minute)
		go func() {
			t := time.NewTicker(time.Minute)
			defer t.Stop()
			for {
				select {
				case <-t.C:
					e.deps.HoldPublished(5 * time.Minute)
				case <-holdDone:
					return
				}
			}
		}()
	}
	for _, en := range r.Entries {
		if !todo(en) {
			continue
		}
		if e.stopped() {
			break
		}
		if o.Max > 0 && r.Published >= o.Max && !o.Sim {
			e.set(func(*Run) { en.Detail = "plafond de publications atteint" })
			continue
		}
		if o.MaxGiB > 0 && !o.Sim && float64(r.PublishedBytes+en.Size) > o.MaxGiB*gib {
			e.set(func(*Run) {
				en.Detail = "attend le lot suivant : dépasserait les " + fmtNum(o.MaxGiB) + " Gio publiés de ce lot"
			})
			continue
		}
		e.set(func(r *Run) { r.Current = en.Path })
		e.progress(fmt.Sprintf("Lot : %d / %d", r.Done+1, r.Total), en.Name)
		e.process(ctx, r, src, en)
		if o.WaitCheck && !o.Sim && en.State == "published" && en.Job != "" {
			e.waitChecked(r, en)
		}
		e.set(func(r *Run) { r.Done++ })
		if !o.Sim && en.State == "published" {
			e.sleep(e.pause)
		}
	}
	if hold {
		close(holdDone)
		e.deps.HoldPublished(0)
	}
	e.set(func(r *Run) {
		if hold {
			for _, en := range r.Entries {
				if en.Detail != "" {
					en.Detail = replaceHeld(en.Detail)
				}
			}
		}
		r.State, r.Current, r.Label, r.Detail, r.EndedAt = StDone, "", "", "", time.Now()
		e.cancel() // plus rien n'attend sur le contexte du lot
	})
}
