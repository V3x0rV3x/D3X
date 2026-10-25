package server

import (
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/V3x0rV3x/D3X/internal/batch"
	"github.com/V3x0rV3x/D3X/internal/config"
	"github.com/V3x0rV3x/D3X/internal/names"
)

// Série choisie une fois (quebecman, 23/10/26) : la série TMDB choisie à la
// main pour une saison sert aux autres saisons, dans Publier et dans le Lot.
// Gardée dans le dossier de D3X (series.json) : le lot, qui tourne dans D3X,
// la voit aussi. La page y verse une fois ce qu'elle gardait dans son
// navigateur.

const seriesFile = "series.json"
const seriesMax = 300

type seriesPick struct {
	batch.Work
	At time.Time `json:"at"`
}

var seriesMu sync.Mutex

func loadSeries() map[string]seriesPick {
	out := map[string]seriesPick{}
	p, err := config.Path(seriesFile)
	if err != nil {
		return out
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return out
	}
	_ = json.Unmarshal(b, &out)
	return out
}

func saveSeries(m map[string]seriesPick) error {
	if len(m) > seriesMax {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if !m[keys[i]].At.Equal(m[keys[j]].At) {
				return m[keys[i]].At.After(m[keys[j]].At)
			}
			return keys[i] < keys[j] // même instant : ordre stable
		})
		for _, k := range keys[seriesMax:] {
			delete(m, k)
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(seriesFile, b)
}

// rememberSeries retient une série (jamais un film) sous la clé de son titre.
func rememberSeries(title string, w batch.Work) {
	rememberSeriesKey(names.Key(title), w)
}

func rememberSeriesKey(key string, w batch.Work) {
	rememberSeriesKeys(map[string]batch.Work{key: w})
}

// rememberSeriesKeys retient plusieurs séries d'un coup : un verrou, une
// lecture, une écriture. Seules les séries (pas les films) à clé assez longue
// sont gardées.
func rememberSeriesKeys(in map[string]batch.Work) {
	seriesMu.Lock()
	defer seriesMu.Unlock()
	m := loadSeries()
	now := time.Now().UTC()
	n := 0
	for k, w := range in {
		if w.Type != "tv" || len(k) < 2 {
			continue
		}
		m[k] = seriesPick{Work: w, At: now}
		n++
	}
	if n > 0 {
		_ = saveSeries(m)
	}
}

// rememberedSeries : la série retenue pour ce titre, nil sinon.
func rememberedSeries(title string) *batch.Work {
	key := names.Key(title)
	if len(key) < 2 {
		return nil
	}
	seriesMu.Lock()
	defer seriesMu.Unlock()
	p, ok := loadSeries()[key]
	if !ok {
		return nil
	}
	w := p.Work
	return &w
}

func (s *Server) routesSeries() {
	s.mux.HandleFunc("GET /ui/series", func(w http.ResponseWriter, r *http.Request) {
		seriesMu.Lock()
		m := loadSeries()
		seriesMu.Unlock()
		out := map[string]batch.Work{}
		for k, p := range m {
			out[k] = p.Work
		}
		writeJSON(w, http.StatusOK, out)
	})
	// Clés calculées par la page (seriesKeyOf) : import depuis le navigateur,
	// ou une série choisie dans Publier.
	s.mux.HandleFunc("POST /ui/series", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Entries map[string]batch.Work `json:"entries"`
		}
		if !decode(w, r, 1<<20, &in) {
			return
		}
		rememberSeriesKeys(in.Entries)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
}
