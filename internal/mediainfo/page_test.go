package mediainfo

import (
	"context"
	"errors"
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
	go func() {
		deadline := time.Now().Add(2 * time.Second)
		for p.Pending() == nil && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
	if _, err := p.Analyze(ctx, Request{JobID: "j2", Source: "local"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("annulation : context.Canceled attendu, reçu %v", err)
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

func TestPageSecondAnalyze(t *testing.T) {
	p := NewPage()
	done := make(chan string, 1)
	go func() {
		out, _ := p.Analyze(context.Background(), Request{JobID: "a", Source: "local"})
		done <- out
	}()
	deadline := time.Now().Add(2 * time.Second)
	for p.Pending() == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if _, err := p.Analyze(context.Background(), Request{JobID: "b", Source: "local"}); err == nil {
		t.Fatal("seconde demande : erreur attendue")
	}
	if r := p.Pending(); r == nil || r.JobID != "a" {
		t.Fatalf("la première demande doit rester en attente : %+v", r)
	}
	if !p.Deliver("a", "ok") {
		t.Fatal("réponse refusée")
	}
	if got := <-done; got != "ok" {
		t.Fatalf("résultat : %q", got)
	}
}

func TestPageDeliverTwice(t *testing.T) {
	p := NewPage()
	done := make(chan struct{})
	go func() {
		p.Analyze(context.Background(), Request{JobID: "a", Source: "local"})
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for p.Pending() == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !p.Deliver("a", "x") {
		t.Fatal("première réponse refusée")
	}
	<-done
	if p.Deliver("a", "x") {
		t.Fatal("seconde réponse : refus attendu")
	}
}
