package mediainfo

import (
	"context"
	"testing"
	"time"
)

func TestPageDeliver(t *testing.T) {
	p := NewPage()
	req := Request{JobID: "j1", FileIndex: 0, Size: 10, Source: "local"}
	done := make(chan string, 1)
	go func() {
		out, err := p.Analyze(context.Background(), req)
		if err != nil {
			out = "ERR " + err.Error()
		}
		done <- out
	}()
	// La demande est visible pour la page.
	var pend *Request
	for i := 0; i < 50 && pend == nil; i++ {
		time.Sleep(10 * time.Millisecond)
		pend = p.Pending()
	}
	if pend == nil || pend.JobID != "j1" {
		t.Fatalf("demande absente : %+v", pend)
	}
	if p.Deliver("autre", "x") {
		t.Fatal("un job inconnu ne doit pas être accepté")
	}
	if !p.Deliver("j1", "General\nFormat : Matroska") {
		t.Fatal("réponse refusée")
	}
	if got := <-done; got != "General\nFormat : Matroska" {
		t.Fatalf("résultat : %q", got)
	}
	if p.Pending() != nil {
		t.Fatal("demande encore en attente")
	}
}

func TestPageCancelled(t *testing.T) {
	p := NewPage()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if _, err := p.Analyze(ctx, Request{JobID: "j2", Source: "local"}); err == nil {
		t.Fatal("annulation : erreur attendue")
	}
	if p.Pending() != nil {
		t.Fatal("demande à retirer après annulation")
	}
}

func TestChooser(t *testing.T) {
	page := NewPage()
	c := &Chooser{Exec: &Exec{Path: "/bin/true"}, Page: page}
	if c.For(Request{Source: "ssh"}) != page {
		t.Fatal("source distante : page attendue")
	}
	if c.For(Request{Source: "local"}) != c.Exec {
		t.Fatal("source locale : binaire attendu")
	}
	c.Exec = nil
	if c.For(Request{Source: "local"}) != page {
		t.Fatal("sans binaire : page attendue")
	}
}
