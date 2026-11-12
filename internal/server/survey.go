package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/V3x0rV3x/D3X/internal/config"
	"github.com/V3x0rV3x/D3X/internal/crossseed"
	"github.com/V3x0rV3x/D3X/internal/desktop"
)

// Surveillance : D3X relance l'analyse du client à intervalle régulier,
// signale ce qui est nouveau (releases à cross-seeder, hit and run en danger)
// et, si le membre l'a choisi, ajoute seul les releases identiques. Les
// ajouts suivent la même règle que d'habitude : à l'arrêt, vérifiés,
// démarrés seulement complets.

// autoAddMax : ajouts automatiques au plus par passage.
const autoAddMax = 50

// surveyState : ce que la surveillance a déjà vu (gardé entre deux lancements).
type surveyState struct {
	Baseline bool            `json:"baseline"` // premier passage fait : la suite est « nouveau »
	Seen     map[string]bool `json:"seen"`     // releases V3X déjà signalées (id)
	HnR      map[string]bool `json:"hnr"`      // alertes déjà données (info hash + jour)
}

type surveyLast struct {
	At     time.Time `json:"at"`
	New    int       `json:"new"`   // nouvelles releases à cross-seeder
	Added  int       `json:"added"` // ajoutées seules
	Danger int       `json:"danger"`
	Error  string    `json:"error,omitempty"`
	Warn   string    `json:"warn,omitempty"` // clients injoignables, l'analyse a fait sans eux
}

type survey struct {
	mu      sync.Mutex
	running bool
	last    surveyLast
	next    time.Time
	state   *surveyState
}

func loadSurvey() *surveyState {
	st := &surveyState{Seen: map[string]bool{}, HnR: map[string]bool{}}
	if p, err := config.Path("surveillance.json"); err == nil {
		if b, err := os.ReadFile(p); err == nil {
			_ = json.Unmarshal(b, st)
		}
	}
	if st.Seen == nil {
		st.Seen = map[string]bool{}
	}
	if st.HnR == nil {
		st.HnR = map[string]bool{}
	}
	return st
}

func (st *surveyState) save() {
	if b, err := json.Marshal(st); err == nil {
		_ = config.WriteFileAtomic("surveillance.json", b)
	}
}

// surveyReset : surveillance rallumée, on repart de l'état présent (ce qui
// est apparu pendant la pause n'est pas « nouveau »).
func (s *Server) surveyReset() {
	s.sv.mu.Lock()
	defer s.sv.mu.Unlock()
	s.sv.state = &surveyState{Seen: map[string]bool{}, HnR: map[string]bool{}}
	s.sv.state.save()
	s.sv.next = time.Time{}
	s.sv.last = surveyLast{}
}

// StayInTray : fenêtre fermée, D3X reste dans la zone de notification, pour
// surveiller le client ou parce que le membre l'a choisi (Réglages ▸ Zone de
// notification).
func (s *Server) StayInTray() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Watch.On || s.cfg.TrayStay
}

// surveyLoop : un tour par minute, une analyse quand l'intervalle est passé.
func (s *Server) surveyLoop() {
	time.Sleep(20 * time.Second)
	for {
		s.surveyTick(context.Background())
		time.Sleep(time.Minute)
	}
}

func (s *Server) surveyTick(ctx context.Context) {
	s.mu.Lock()
	w := s.cfg.Watch
	s.mu.Unlock()
	s.sv.mu.Lock()
	if !w.On {
		s.sv.next = time.Time{}
		s.sv.mu.Unlock()
		return
	}
	if s.sv.next.IsZero() {
		// Premier tour après l'activation (ou le lancement) : tout de suite.
		s.sv.next = time.Now()
	}
	due := !time.Now().Before(s.sv.next) && !s.sv.running
	s.sv.mu.Unlock()
	if !due {
		return
	}
	// Jamais par-dessus le travail du membre : analyse, ajouts, publication.
	if s.engine.Scanning() || s.Busy() {
		return
	}
	// Un client injoignable n'arrête pas la surveillance : l'analyse le
	// retente, et travaille avec les autres.
	if err := s.ready(); err != nil {
		return
	}
	s.sv.mu.Lock()
	s.sv.running = true
	s.sv.mu.Unlock()
	last := s.surveyRun(ctx, w)
	s.sv.mu.Lock()
	s.sv.running = false
	s.sv.last = last
	s.sv.next = time.Now().Add(time.Duration(w.Every()) * time.Minute)
	s.sv.mu.Unlock()
}

func (s *Server) surveyRun(ctx context.Context, w config.Watch) surveyLast {
	out := surveyLast{At: time.Now()}
	if err := s.engine.StartScan(); err != nil {
		out.Error = err.Error()
		return out
	}
	for i := 0; i < 1800 && s.engine.Scanning(); i++ {
		time.Sleep(time.Second)
	}
	st := s.engine.State()
	if st.Scan.Phase == "error" {
		out.Error = st.Scan.Error
		return out
	}
	out.Warn = st.Scan.Warn
	s.sv.mu.Lock()
	if s.sv.state == nil {
		s.sv.state = loadSurvey()
	}
	state := s.sv.state
	s.sv.mu.Unlock()

	var fresh []crossseed.Row
	current := map[string]bool{}
	for _, r := range s.engine.Rows() {
		if r.Match == nil || (r.Status != crossseed.StAdd && r.Status != crossseed.StRenamed) {
			continue
		}
		current[r.Match.ID] = true
		if !state.Seen[r.Match.ID] {
			fresh = append(fresh, r)
		}
	}
	// Premier passage : l'état de départ, rien n'est « nouveau » (ce qui était
	// déjà là s'ajoute à la main, d'un clic).
	if !state.Baseline {
		state.Baseline = true
		state.Seen = current
		state.save()
		return out
	}
	for id := range current {
		state.Seen[id] = true
	}
	out.New = len(fresh)

	var keys []string
	if w.AutoAdd {
		for _, r := range fresh {
			if r.Status == crossseed.StAdd && r.Match.Kind == "exact" && len(keys) < autoAddMax {
				keys = append(keys, r.Key)
			}
		}
		if len(keys) > 0 {
			if n, err := s.engine.Apply(keys); err == nil {
				out.Added = n
			} else {
				log.Println("surveillance : ajout automatique impossible :", err)
			}
		}
	}

	// Hit and run : une alerte par release et par jour, quand le temps manque.
	var danger []string
	if view, err := s.buildSeed(ctx, true); err == nil {
		day := time.Now().Format("2006-01-02")
		for _, it := range view.Items {
			k := strings.ToLower(it.InfoHash) + "|" + day
			if it.Risk == "bad" && !it.HnR && !state.HnR[k] {
				state.HnR[k] = true
				danger = append(danger, it.Name)
			}
		}
		for k := range state.HnR {
			if !strings.HasSuffix(k, "|"+day) {
				delete(state.HnR, k)
			}
		}
	}
	out.Danger = len(danger)
	state.save()

	if w.Notify {
		s.surveyNotify(fresh, out, danger)
	}
	return out
}

func (s *Server) surveyNotify(fresh []crossseed.Row, out surveyLast, danger []string) {
	if s.opts.Remote {
		return // mode serveur : pas de bureau où notifier
	}
	if len(danger) > 0 {
		text := danger[0]
		if len(danger) > 1 {
			text = fmt.Sprintf("%s et %d autre(s)", danger[0], len(danger)-1)
		}
		if err := desktop.Notify("Hit and run en vue", text+" : plus assez de temps de seed avant l'échéance. Ouvre D3X ▸ Mon seed."); err != nil {
			log.Println("notification :", err)
		}
	}
	if out.New == 0 {
		return
	}
	var title, text string
	switch {
	case out.Added > 0 && out.Added == out.New:
		title = fmt.Sprintf("%d %s en seed pour V3X", out.Added, plural(out.Added, "release ajoutée", "releases ajoutées"))
		text = "Ajoutées à l'arrêt, vérifiées par ton client, démarrées seulement complètes."
	case out.Added > 0:
		title = fmt.Sprintf("%d %s pour V3X", out.New, plural(out.New, "nouvelle release", "nouvelles releases"))
		text = fmt.Sprintf("%d ajoutées seules, les autres attendent ton choix dans Cross-seed.", out.Added)
	default:
		title = fmt.Sprintf("%d %s à cross-seeder", out.New, plural(out.New, "nouvelle release", "nouvelles releases"))
		text = "Elles existent sur V3X à l'identique : ouvre D3X ▸ Cross-seed pour les ajouter."
	}
	if len(fresh) > 0 {
		name := fresh[0].Name
		if fresh[0].Match != nil {
			name = fresh[0].Match.Name
		}
		if len(name) > 80 {
			name = name[:77] + "..."
		}
		text = name + "\n" + text
	}
	if err := desktop.Notify(title, text); err != nil {
		log.Println("notification :", err)
	}
}

func plural(n int, one, many string) string {
	if n > 1 {
		return many
	}
	return one
}

// surveyOut : l'état de la surveillance pour la page.
type surveyOut struct {
	On        bool        `json:"on"`
	Minutes   int         `json:"minutes"`
	Notify    bool        `json:"notify"`
	AutoAdd   bool        `json:"autoAdd"`
	Autostart bool        `json:"autostart"`
	CanStart  bool        `json:"canAutostart"`
	Tray      bool        `json:"tray"` // fenêtre fermée, D3X reste dans la zone de notification
	Running   bool        `json:"running"`
	Next      *time.Time  `json:"next,omitempty"`
	Last      *surveyLast `json:"last,omitempty"`
}

func (s *Server) surveyState(w config.Watch) surveyOut {
	o := surveyOut{On: w.On, Minutes: w.Every(), Notify: w.Notify, AutoAdd: w.AutoAdd, CanStart: desktop.HasAutostart && !s.opts.Remote, Tray: desktop.HasTray && !s.opts.Remote}
	if o.CanStart {
		o.Autostart = desktop.Autostart()
	}
	s.sv.mu.Lock()
	defer s.sv.mu.Unlock()
	o.Running = s.sv.running
	if !s.sv.next.IsZero() && w.On {
		n := s.sv.next
		o.Next = &n
	}
	if !s.sv.last.At.IsZero() {
		l := s.sv.last
		o.Last = &l
	}
	return o
}
