package maketorrent

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// Vitesse de lecture des fichiers de ce PC (hachage, Médiathèque, MediaInfo).
// Des fichiers sur un NAS joint par le réseau (partage SMB) se lisaient aussi
// vite que la carte réseau le permet : le hachage d'une grosse release prenait
// tout le débit et coupait le reste, DNS compris (Mamaiii, 30/10/26). Toutes
// les lectures en cours se partagent la même limite.
var readLimit atomic.Int64 // octets par seconde, 0 = sans limite

var pace struct {
	sync.Mutex
	next time.Time
}

// limitChunk : taille d'une lecture quand la limite est posée, pour un débit
// régulier plutôt que des rafales d'une pièce entière (jusqu'à 16 Mio).
const limitChunk = 1 << 20

// SetReadLimit borne la vitesse de lecture, en Mo/s (0 = sans limite).
func SetReadLimit(mbps int) {
	if mbps < 0 {
		mbps = 0
	}
	pace.Lock()
	readLimit.Store(int64(mbps) * 1_000_000)
	pace.next = time.Time{} // l'ancien rythme ne retarde pas le nouveau
	pace.Unlock()
}

func limited() bool { return readLimit.Load() > 0 }

// throttle attend le moment où n octets de plus restent dans la limite. Une
// pause (rien à lire) ne s'accumule pas en crédit : la lecture suivante
// repart au rythme permis, sans rafale.
func throttle(ctx context.Context, n int) error {
	rate := readLimit.Load()
	if rate <= 0 || n <= 0 {
		return nil
	}
	d := time.Duration(float64(n) / float64(rate) * float64(time.Second))
	pace.Lock()
	now := time.Now()
	if pace.next.Before(now) {
		pace.next = now
	}
	wait := pace.next.Sub(now)
	pace.next = pace.next.Add(d)
	pace.Unlock()
	if wait <= 0 {
		return nil
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Paced enveloppe r pour qu'il respecte la limite de lecture (rien ne change
// sans limite).
func Paced(ctx context.Context, r io.Reader) io.Reader { return pacedReader{ctx, r} }

type pacedReader struct {
	ctx context.Context
	r   io.Reader
}

func (p pacedReader) Read(b []byte) (int, error) {
	if !limited() {
		return p.r.Read(b)
	}
	if len(b) > limitChunk {
		b = b[:limitChunk]
	}
	if err := throttle(p.ctx, len(b)); err != nil {
		return 0, err
	}
	return p.r.Read(b)
}
