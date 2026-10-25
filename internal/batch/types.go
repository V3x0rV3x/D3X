package batch

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/V3x0rV3x/D3X/internal/journal"
	"github.com/V3x0rV3x/D3X/internal/releases"
)

// Text : un champ du site qui arrive en chaîne ou en nombre (année, id,
// saison), gardé tel quel en texte ; null, absent ou autre type : vide.
type Text string

func (t *Text) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	switch {
	case s == "":
		*t = ""
	case s[0] == '"':
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*t = Text(v)
	case s[0] == '-' || s[0] >= '0' && s[0] <= '9':
		*t = Text(s)
	default: // null, booléen, tableau, objet : vide
		*t = ""
	}
	return nil
}

func (t Text) String() string { return string(t) }

// Work : une œuvre TMDB (résultat de recherche du site, ou série mémorisée).
type Work struct {
	Type          string  `json:"type"` // movie, tv
	ID            Text    `json:"id"`
	Title         string  `json:"title"`
	OriginalTitle string  `json:"originalTitle,omitempty"`
	Year          Text    `json:"year,omitempty"`
	PosterURL     *string `json:"posterUrl"`
}

// File : un fichier de la release (chemin dans la release, taille).
type File struct {
	Rel  string
	Size int64
}

// États d'un run.
const (
	StRunning  = "running"
	StStopping = "stopping"
	StDone     = "done"
)

// Options du lot, figées au démarrage.
type Options struct {
	Sim       bool    `json:"sim"`       // simulation : rien n'est publié
	Video     bool    `json:"video"`     // vidéos et albums seulement
	FixName   bool    `json:"fixName"`   // compléter le nom des albums avec leur qualité
	WaitCheck bool    `json:"waitCheck"` // attendre la vérification du client avant la suivante
	SeedLater bool    `json:"seedLater"` // démarrer le seed à la fin du lot
	KeepNfo   bool    `json:"keepNfo"`   // reprendre le .nfo de la release
	Cap       float64 `json:"cap"`       // Gio par release, 0 : sans plafond
	Max       int     `json:"max"`       // publications au plus, 0 : sans limite
	MaxGiB    float64 `json:"maxGib"`    // Gio publiés par lot, 0 : sans limite
}

// Entry : une release du lot et ce que le lot en a fait.
type Entry struct {
	releases.Entry
	Last     *journal.Event  `json:"last,omitempty"`
	State    string          `json:"state"` // pending, ready, review, simulated, published, duplicate, ignored, error
	Detail   string          `json:"detail"`
	Job      string          `json:"job,omitempty"`
	Active   bool            `json:"active,omitempty"`   // publiée et active (pas en attente de validation)
	Requests json.RawMessage `json:"requests,omitempty"` // demandes ouvertes auxquelles elle répond
}

// Run : un lot, en cours ou fini.
type Run struct {
	Dir            string    `json:"dir"`
	Source         string    `json:"source"`
	Options        Options   `json:"options"`
	Entries        []*Entry  `json:"entries"`
	State          string    `json:"state"`
	Stopped        bool      `json:"stopped"`
	Current        string    `json:"current,omitempty"`
	Label          string    `json:"label"`  // « Lot : 3 / 12 »
	Detail         string    `json:"detail"` // « hachage 42 % », « ton client vérifie X »
	Done           int       `json:"done"`
	Total          int       `json:"total"`
	Published      int       `json:"published"`
	PublishedBytes int64     `json:"publishedBytes"`
	StartedAt      time.Time `json:"startedAt"`
	EndedAt        time.Time `json:"endedAt,omitempty"`
}

// Spec : ce que la page envoie pour démarrer.
type Spec struct {
	Dir     string   `json:"path"`
	Source  string   `json:"source"`
	Paths   []string `json:"paths"`
	Options Options  `json:"options"`
}

// Fiche : la fiche proposée par V3X (les champs que le lot lit).
type Fiche struct {
	Title        string `json:"title"`
	Presentation string `json:"presentation"`
	NFO          string `json:"nfo"`
	Language     string `json:"language"`
	Season       Text   `json:"season"`
	TmdbQuery    string `json:"tmdbQuery"`
	Creator      string `json:"creator"`
	TeamRelease  bool   `json:"teamRelease"`
	Category     *struct {
		ID   Text   `json:"id"`
		Slug string `json:"slug"`
	} `json:"category"`
	Warnings []string `json:"warnings"`
	Analysis struct {
		Title  string `json:"title"`
		Tokens []struct {
			Kind  string `json:"kind"`
			Value Text   `json:"value"`
		} `json:"tokens"`
	} `json:"analysis"`
}

// yearToken : l'année lue dans le nom par le site.
func (f *Fiche) yearToken() Text {
	for _, t := range f.Analysis.Tokens {
		if t.Kind == "year" {
			return t.Value
		}
	}
	return ""
}

// seriesTitleOf : le titre de série d'une fiche (celui lu dans le nom).
func seriesTitleOf(f *Fiche) string {
	if f.Analysis.Title != "" {
		return f.Analysis.Title
	}
	return f.TmdbQuery
}

// FicheInput : ce que le lot envoie pour une fiche.
type FicheInput struct {
	ReleaseName  string
	MediaInfo    string
	TmdbRef      *string
	MbRef        *string
	CategorySlug *string
}

// Category du contexte du membre.
type Category struct {
	ID       Text       `json:"id"`
	Name     string     `json:"name"`
	Slug     string     `json:"slug"`
	Children []Category `json:"children"`
}

// Context : le contexte de publication du membre (ce que le lot lit).
type Context struct {
	User struct {
		AnonymousDefault bool            `json:"anonymousDefault"`
		Team             json.RawMessage `json:"team"`
	} `json:"user"`
	Categories []Category `json:"categories"`
}

// HasTeam : le membre est dans une team.
func (c *Context) HasTeam() bool {
	t := string(c.User.Team)
	return t != "" && t != "null" && t != "false"
}

// musicCategory : Audio ▸ Musique.
func (c *Context) musicCategory() (root, leaf *Category) {
	for i := range c.Categories {
		r := &c.Categories[i]
		if r.Slug != "audio" {
			continue
		}
		for k := range r.Children {
			if r.Children[k].Name == "Musique" {
				return r, &r.Children[k]
			}
		}
	}
	return nil, nil
}

// categoryName : « Films ▸ Film », ou le slug s'il est inconnu.
func (c *Context) categoryName(slug string) string {
	for _, r := range c.Categories {
		if r.Slug == slug {
			return r.Name
		}
		for _, k := range r.Children {
			if k.Slug == slug {
				return r.Name + " ▸ " + k.Name
			}
		}
	}
	return slug
}

// Prepared : la fiche gardée depuis Publier pour une release du lot.
type Prepared struct {
	Name    string
	Summary string
	Fields  map[string]string
}
