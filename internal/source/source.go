// Package source dit où sont les fichiers d'une release : sur ce PC, ou sur
// une seedbox jointe par SSH (l'agent y fait le travail sur place).
package source

import (
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/V3x0rV3x/D3X/internal/agent"
	"github.com/V3x0rV3x/D3X/internal/audio"
	"github.com/V3x0rV3x/D3X/internal/links"
	"github.com/V3x0rV3x/D3X/internal/maketorrent"
	"github.com/V3x0rV3x/D3X/internal/releases"
	"github.com/V3x0rV3x/D3X/internal/remote"
)

// Source : là où sont les fichiers.
type Source interface {
	ID() string    // local, ssh
	Label() string // « Ce PC », « user@hôte »
	// Key distingue les chemins des deux sources dans le journal.
	Key(path string) string
	Browse(ctx context.Context, path string, exclude []string) (*releases.Listing, error)
	List(ctx context.Context, dir string, exclude []string) ([]releases.Entry, error)
	// Entries : les releases de chemins choisis.
	Entries(ctx context.Context, paths []string, exclude []string) ([]releases.Entry, error)
	// Exists : le chemin existe-t-il (fichier ou dossier) ?
	Exists(ctx context.Context, path string) bool
	Scan(ctx context.Context, path string, exclude []string) (*maketorrent.Plan, error)
	Hash(ctx context.Context, p *maketorrent.Plan, exclude []string, createdBy string, progress maketorrent.Progress) ([]byte, bool, error)
	ReadAt(ctx context.Context, path string, off, n int64) ([]byte, error)
	// Audio lit les tags et la qualité des pistes (nil : pas d'album).
	Audio(ctx context.Context, p *maketorrent.Plan, exclude []string) (*audio.Album, error)
	Dir(path string) string
	Names(ctx context.Context, dir string) []string
}

// ---------- ce PC ----------

// Local : les fichiers de cette machine.
type Local struct{}

func (Local) ID() string          { return "local" }
func (Local) Label() string       { return "Ce PC" }
func (Local) Key(p string) string { return p }
func (Local) Dir(p string) string { return filepath.Dir(p) }
func (Local) Browse(_ context.Context, p string, ex []string) (*releases.Listing, error) {
	return releases.Browse(p, ex)
}
func (Local) List(_ context.Context, d string, ex []string) ([]releases.Entry, error) {
	return releases.List(d, ex)
}
func (Local) Entries(_ context.Context, p []string, ex []string) ([]releases.Entry, error) {
	return releases.Entries(p, ex), nil
}
func (Local) Exists(_ context.Context, p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
func (Local) Scan(_ context.Context, p string, ex []string) (*maketorrent.Plan, error) {
	return maketorrent.Scan(p, ex)
}
func (Local) Hash(ctx context.Context, p *maketorrent.Plan, _ []string, by string, pr maketorrent.Progress) ([]byte, bool, error) {
	return p.Hash(ctx, by, pr)
}
func (Local) Audio(_ context.Context, p *maketorrent.Plan, _ []string) (*audio.Album, error) {
	return audio.FromPlan(p), nil
}
func (Local) ReadAt(ctx context.Context, p string, off, n int64) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(maketorrent.Paced(ctx, io.NewSectionReader(f, off, n)))
}
func (Local) Names(_ context.Context, d string) []string {
	list, err := os.ReadDir(d)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		out = append(out, e.Name())
	}
	return out
}

// ---------- seedbox ----------

// Remote : les fichiers d'une seedbox, lus par l'agent.
type Remote struct{ C *remote.Client }

func (r Remote) ID() string          { return "ssh" }
func (r Remote) Label() string       { return r.C.Config().Label() }
func (r Remote) Key(p string) string { return "ssh://" + r.C.Config().Label() + p }

// Dir : dossier parent. Une seedbox Linux a des chemins en « / » (un « \ »
// peut faire partie d'un nom) ; seule la seedbox de démonstration, sous
// Windows, a des chemins à lettre de lecteur.
func (r Remote) Dir(p string) string {
	if strings.HasPrefix(p, "/") {
		return path.Dir(p)
	}
	return filepath.Dir(p)
}

func (r Remote) Browse(ctx context.Context, p string, ex []string) (*releases.Listing, error) {
	var l releases.Listing
	err := r.C.AgentJSON(ctx, "ls", agent.Request{Path: p, Exclude: ex}, &l)
	return &l, err
}

func (r Remote) List(ctx context.Context, d string, ex []string) ([]releases.Entry, error) {
	var l []releases.Entry
	err := r.C.AgentJSON(ctx, "list", agent.Request{Path: d, Exclude: ex}, &l)
	return l, err
}

func (r Remote) Entries(ctx context.Context, p []string, ex []string) ([]releases.Entry, error) {
	var l []releases.Entry
	err := r.C.AgentJSON(ctx, "entries", agent.Request{Paths: p, Exclude: ex}, &l)
	return l, err
}

// Exists : le dossier parent, listé par l'agent, contient-il ce nom ?
func (r Remote) Exists(ctx context.Context, p string) bool {
	l, err := r.Browse(ctx, path.Dir(p), nil)
	if err != nil {
		return false
	}
	for _, e := range l.Entries {
		if e.Name == path.Base(p) {
			return true
		}
	}
	return false
}

// Links crée (ou retire) des liens physiques sur la seedbox, par l'agent.
func (r Remote) Links(ctx context.Context, base string, pairs []links.Pair, remove bool) error {
	cmd := "link"
	if remove {
		cmd = "unlink"
	}
	var ok bool
	return r.C.AgentJSON(ctx, cmd, agent.Request{Base: base, Links: pairs}, &ok)
}

func (r Remote) Scan(ctx context.Context, p string, ex []string) (*maketorrent.Plan, error) {
	var plan maketorrent.Plan
	if err := r.C.AgentJSON(ctx, "scan", agent.Request{Path: p, Exclude: ex}, &plan); err != nil {
		return nil, err
	}
	return &plan, nil
}

// Hash fait hacher la release sur la seedbox (même code qu'en local, avec
// son propre cache là-bas) ; seul le .torrent revient.
func (r Remote) Hash(ctx context.Context, p *maketorrent.Plan, ex []string, by string, pr maketorrent.Progress) ([]byte, bool, error) {
	cached := false
	raw, err := r.C.Agent(ctx, "hash", agent.Request{Path: p.Root, Exclude: ex, PieceLength: p.PieceLength, CreatedBy: by}, pr, &cached)
	return raw, cached, err
}

func (r Remote) Audio(ctx context.Context, p *maketorrent.Plan, ex []string) (*audio.Album, error) {
	var a *audio.Album
	err := r.C.AgentJSON(ctx, "audio", agent.Request{Path: p.Root, Exclude: ex}, &a)
	return a, err
}

func (r Remote) ReadAt(ctx context.Context, p string, off, n int64) ([]byte, error) {
	return r.C.Agent(ctx, "read", agent.Request{Path: p, Offset: off, Size: n}, nil, nil)
}

func (r Remote) Names(ctx context.Context, d string) []string {
	l, err := r.Browse(ctx, d, nil)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(l.Entries))
	for _, e := range l.Entries {
		out = append(out, e.Name)
	}
	return out
}
