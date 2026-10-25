// Package mediainfo calcule le MediaInfo d'un fichier d'une publication :
// par le binaire mediainfo quand il existe (image Docker, ou installé sur le
// PC), sinon par une page ouverte de D3X, qui le calcule avec mediainfo.js
// comme pour Publier.
package mediainfo

import "context"

// Request : le fichier principal d'une publication.
type Request struct {
	JobID     string `json:"jobId"`
	FileIndex int    `json:"fileIndex"`
	Size      int64  `json:"size"`
	Source    string `json:"source"` // local, ssh
	Path      string `json:"-"`      // chemin réel (source locale) ; jamais vers la page
}

// Provider calcule le MediaInfo en texte (même forme que mediainfo.js en
// mode text). Chaîne vide : illisible.
type Provider interface {
	Analyze(ctx context.Context, req Request) (string, error)
}

// Chooser : le binaire pour une source locale s'il existe, la page sinon.
type Chooser struct {
	Exec *Exec
	Page *Page
}

func (c *Chooser) For(req Request) Provider {
	if c.Exec != nil && req.Source == "local" {
		return c.Exec
	}
	return c.Page
}

func (c *Chooser) Analyze(ctx context.Context, req Request) (string, error) {
	return c.For(req).Analyze(ctx, req)
}
