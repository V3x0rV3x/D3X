package batch

import (
	"context"
	"encoding/json"
	"time"

	"github.com/V3x0rV3x/D3X/internal/journal"
	"github.com/V3x0rV3x/D3X/internal/mediainfo"
	"github.com/V3x0rV3x/D3X/internal/musicbrainz"
	"github.com/V3x0rV3x/D3X/internal/publish"
	"github.com/V3x0rV3x/D3X/internal/releases"
	"github.com/V3x0rV3x/D3X/internal/source"
)

// Deps : ce que le lot demande au reste de D3X. Le serveur les fournit ; les
// tests les simulent. Jamais de HTTP ici.
type Deps interface {
	Source(id string) (source.Source, error)
	Exclude() []string
	Entries(ctx context.Context, src source.Source, paths []string) ([]releases.Entry, error)
	LastByPath() map[string]journal.Event

	// Publication (publish.Manager).
	PubStart(src source.Source, path string) (*publish.Job, error)
	PubGet(id string) (*publish.Job, bool)
	PubCancel(id string)
	PubPublish(id string, fields map[string]string) error
	PubRead(ctx context.Context, id string, index int, off, n int64) ([]byte, int64, error)
	PubFilePath(id string, index int) (string, bool)

	// V3X.
	Context(ctx context.Context) (*Context, error)
	Fiche(ctx context.Context, jobID string, in FicheInput) (*Fiche, error)
	TMDBSearch(ctx context.Context, q string) ([]Work, error)
	// Redo : le nom d'un REPACK (ou PROPER) de cette release déjà sur V3X, vide sinon.
	Redo(ctx context.Context, name string, size int64, tmdbRef, categorySlug string) (string, error)
	// RequestsFor : les demandes ouvertes auxquelles le nom répond (JSON pour la page).
	RequestsFor(ctx context.Context, name string) (json.RawMessage, error)

	MBDecide(ctx context.Context, jobID string) (*musicbrainz.Result, error)
	Prepared(key string) (*Prepared, bool)
	DropPrepared(key string)
	RememberedSeries(title string) *Work
	Journal(e journal.Event)
	HoldPublished(d time.Duration)
	MediaInfo(ctx context.Context, req mediainfo.Request) (string, error)
}
