package crossseed

import (
	"context"
	"testing"
	"time"

	"github.com/V3x0rV3x/D3X/internal/client"
)

// Client simulé : seule la liste, le démarrage et la vérification comptent.
type stepClient struct {
	client.Client
	starts, rechecks int
}

func (s *stepClient) Start(context.Context, string) error   { s.starts++; return nil }
func (s *stepClient) Stop(context.Context, string) error    { return nil }
func (s *stepClient) Recheck(context.Context, string) error { s.rechecks++; return nil }

// Un client qui retient l'ajout pour le prouver (Hydranos) le dit « en
// vérification » aussi longtemps qu'il le veut : step ne compte aucun délai
// sur un torrent en vérification, ne relance rien, et ne démarre qu'à la fin,
// une fois complet.
func TestStepLongChecking(t *testing.T) {
	isolatedConfig(t)
	const h = "0123456789abcdef0123456789abcdef01234567"
	e := &Engine{rows: map[string]*Row{}, watches: map[string]*watch{}, final: map[string]WatchState{}}
	// Ajouté il y a dix minutes (bien au-delà de recheckGap et missGrace).
	e.watches[h] = &watch{Key: "k", Name: "Release.X", Client: "c1", InfoHash: h, Status: StChecking, Since: time.Now().Add(-10 * time.Minute)}
	q := &stepClient{}
	ctx := context.Background()
	state := func() string {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.watches[h].Status
	}
	checking := map[string]client.Torrent{h: {Hash: h, Checking: true, Progress: 0}}
	for i := 0; i < 5; i++ {
		e.step(ctx, q, h, checking)
		if state() != StChecking || q.starts != 0 || q.rechecks != 0 {
			t.Fatalf("passe %d : %s, démarrages %d, revérifications %d", i, state(), q.starts, q.rechecks)
		}
	}
	// La retenue se termine : complet, arrêté (il a été ajouté ainsi) → démarré.
	done := map[string]client.Torrent{h: {Hash: h, Progress: 1, Stopped: true}}
	e.step(ctx, q, h, done)
	if q.starts != 1 || q.rechecks != 0 {
		t.Fatalf("fin de retenue : démarrages %d, revérifications %d", q.starts, q.rechecks)
	}
	if st, ok := e.WatchOf(h); !ok || st.Status != StSeeding {
		t.Fatalf("fin de retenue : %v %v", st, ok)
	}
}
