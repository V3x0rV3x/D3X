package mediainfo

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Page : une page ouverte calcule le MediaInfo. Le serveur publie la demande
// (Pending, visible dans l'état du lot et dans /ui/status) ; la première page
// qui la voit lit le fichier par morceaux comme pour Publier et rend le
// texte (Deliver). Sans page, la demande attend : le lot est en pause.
type Page struct {
	mu   sync.Mutex
	req  *Request
	done chan string
}

func NewPage() *Page { return &Page{} }

// Pending : la demande en attente, nil s'il n'y en a pas.
func (p *Page) Pending() *Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.req == nil {
		return nil
	}
	r := *p.req
	return &r
}

// Deliver : une page rend le MediaInfo du job (texte vide : illisible).
// Faux si aucune demande n'attend ce job.
func (p *Page) Deliver(jobID, text string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.req == nil || p.req.JobID != jobID {
		return false
	}
	p.done <- text
	p.req, p.done = nil, nil
	return true
}

// Analyze attend la réponse d'une page, ou l'annulation du contexte (arrêt du lot).
func (p *Page) Analyze(ctx context.Context, req Request) (string, error) {
	p.mu.Lock()
	if p.req != nil {
		p.mu.Unlock()
		return "", errors.New("un MediaInfo est déjà en attente")
	}
	req.Path = "" // jamais vers la page
	done := make(chan string, 1)
	p.req, p.done = &req, done
	p.mu.Unlock()
	select {
	case text := <-done:
		return text, nil
	case <-ctx.Done():
		p.mu.Lock()
		if p.done == done {
			p.req, p.done = nil, nil
		}
		p.mu.Unlock()
		return "", fmt.Errorf("mediainfo : lot arrêté : %w", ctx.Err())
	}
}
