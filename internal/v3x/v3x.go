// Package v3x parle à l'API de V3X avec la clé personnelle du membre.
//
// C'est le SEUL paquet de D3X qui contacte le site. Il n'envoie que des
// empreintes (pour le cross-seed) ou ce que le membre choisit de publier :
// jamais un chemin, un nom de fichier local ni les identifiants du client.
package v3x

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultBase est l'adresse de l'API de V3X.
const DefaultBase = "https://api.v3x.club"

// Client parle à l'API de V3X.
type Client struct {
	Base string
	Key  string
	hc   *http.Client
	ua   string

	mu        sync.Mutex
	pauseTill time.Time // pause imposée par le site (429), partagée par tous les appels
}

// New crée un client. ua identifie D3X et sa version auprès du site.
func New(base, key, ua string) *Client {
	if base == "" {
		base = DefaultBase
	}
	return &Client{Base: strings.TrimRight(base, "/"), Key: strings.TrimSpace(key), hc: &http.Client{Timeout: 90 * time.Second}, ua: ua}
}

// APIError est une erreur renvoyée par le site.
type APIError struct {
	Status int
	Code   string
}

func (e *APIError) Error() string {
	switch {
	case e.Status == http.StatusUnauthorized:
		return "clé API refusée : vérifie-la dans Réglages ▸ Intégrations sur V3X (droit « Torznab » au minimum)"
	case e.Status == http.StatusForbidden && strings.HasPrefix(e.Code, "ratio under floor"):
		return "V3X refuse le .torrent : ton ratio est sous le plancher (les téléchargements sont limités, le seed les débloque)"
	case e.Status == http.StatusForbidden && strings.HasPrefix(e.Code, "hit and run"):
		return "V3X refuse le .torrent : trop de hit and run non régularisés, remets-les en seed d'abord"
	case e.Status == http.StatusTooManyRequests:
		return "V3X limite les appels, réessaie dans un moment"
	}
	return fmt.Sprintf("V3X : %s (HTTP %d)", e.Code, e.Status)
}

// Retry : un 429 attend la pause demandée puis réessaie (6 essais au plus).
// Réservé aux traitements en lot : une action cliquée renvoie l'erreur tout de
// suite au lieu d'un bouton qui tourne des minutes. Passé à l'appel, ou porté
// par son contexte (WithRetry) pour les appels qui n'en prennent pas.
type Retry struct {
	OnWait func(d time.Duration) // prévenir la page (« reprise dans N s »)
}

const maxPause = 2 * time.Minute

// RetryBudget : l'attente la plus longue qu'un Retry ajoute à un appel (une
// pause déjà en cours, puis une par nouvel essai, maxPause chacune au plus).
const RetryBudget = 6 * maxPause

type retryKey struct{}

// WithRetry : les appels au site faits avec ce contexte attendent et
// réessaient sur un 429, même ceux qui ne prennent pas de Retry (contexte de
// publication, TMDB, fiche, versions, envoi d'une release…). Pour le Lot,
// qui enchaîne les appels sans personne devant (Encrypted_Shards, 05/11/26 :
// erreurs en série vers la 60e release) ; jamais pour une action cliquée.
func WithRetry(ctx context.Context, r *Retry) context.Context {
	if r == nil {
		return ctx
	}
	return context.WithValue(ctx, retryKey{}, r)
}

// retryOf : r, sinon celui que porte le contexte ; nil : un 429 remonte tout de suite.
func retryOf(ctx context.Context, r *Retry) *Retry {
	if r != nil {
		return r
	}
	r, _ = ctx.Value(retryKey{}).(*Retry)
	return r
}

// Timeout : le délai à donner à un appel au site qui prend d au plus. Avec un
// Retry dans le contexte, les pauses demandées par le site s'y ajoutent
// (RetryBudget) : sans elles, le délai couperait l'attente au lieu de la
// laisser reprendre l'appel. Sans Retry : d, inchangé.
func Timeout(ctx context.Context, d time.Duration) time.Duration {
	if retryOf(ctx, nil) != nil {
		return d + RetryBudget
	}
	return d
}

// pause : un 429 reçu à l'essai attempt (0 pour le premier). Vrai : la pause
// demandée, partagée par tous les appels, est posée et l'appel peut réessayer ;
// faux : elle dépasse maxPause, l'erreur remonte.
func (c *Client) pause(h http.Header, attempt int) bool {
	d := retryAfter(h, attempt)
	if d > maxPause {
		return false
	}
	c.mu.Lock()
	if till := time.Now().Add(d); till.After(c.pauseTill) {
		c.pauseTill = till
	}
	c.mu.Unlock()
	return true
}

// retryAfter lit la pause demandée. Une seconde de marge : certains serveurs
// répondent « Retry-After: 0 » pendant la dernière seconde d'une fenêtre encore
// fermée, et les essais partiraient en rafale.
func retryAfter(h http.Header, attempt int) time.Duration {
	if v := h.Get("Retry-After"); v != "" {
		if s, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return time.Duration(s)*time.Second + time.Second
		}
		if t, err := http.ParseTime(v); err == nil {
			return time.Until(t) + time.Second
		}
	}
	if v := h.Get("X-RateLimit-Reset"); v != "" {
		if s, err := strconv.ParseInt(v, 10, 64); err == nil {
			if s > 1e9 { // horodatage
				return time.Until(time.Unix(s, 0)) + time.Second
			}
			return time.Duration(s)*time.Second + time.Second
		}
	}
	return time.Duration(2<<attempt) * time.Second // 2 s, 4 s, 8 s…
}

func (c *Client) waitPause(ctx context.Context, r *Retry) error {
	c.mu.Lock()
	d := time.Until(c.pauseTill)
	c.mu.Unlock()
	if d <= 0 {
		return nil
	}
	if r != nil && r.OnWait != nil {
		r.OnWait(d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		if r != nil && r.OnWait != nil {
			r.OnWait(0)
		}
		return nil
	}
}

func (c *Client) send(ctx context.Context, r *Retry, build func() (*http.Request, error)) ([]byte, error) {
	r = retryOf(ctx, r)
	for attempt := 0; ; attempt++ {
		if r != nil {
			if err := c.waitPause(ctx, r); err != nil {
				return nil, err
			}
		}
		req, err := build()
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", c.ua)
		resp, err := c.hc.Do(req)
		if err != nil {
			return nil, unreachable(err)
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests && r != nil && attempt < 5 {
			if !c.pause(resp.Header, attempt) {
				return nil, &APIError{Status: resp.StatusCode, Code: "rate_limited"}
			}
			continue
		}
		if resp.StatusCode >= 300 {
			var e struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(data, &e)
			if e.Error == "" {
				e.Error = strings.TrimSpace(string(data))
				if len(e.Error) > 200 {
					e.Error = e.Error[:200]
				}
			}
			return nil, &APIError{Status: resp.StatusCode, Code: e.Error}
		}
		return data, nil
	}
}

func (c *Client) doJSON(ctx context.Context, r *Retry, method, path string, body, out interface{}) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	data, err := c.send(ctx, r, func() (*http.Request, error) {
		var rd io.Reader
		if payload != nil {
			rd = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.Base+path, rd)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.Key)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return req, nil
	})
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// Me décrit le compte lié à la clé.
type Me struct {
	Username  string   `json:"username"`
	Ratio     *float64 `json:"ratio"`
	Seeding   int      `json:"seeding"`
	Leeching  int      `json:"leeching"`
	HitAndRun int      `json:"hitAndRun"`
	Freeleech bool     `json:"freeleech"`
	// Apparence choisie sur le site (fond : dark, light, blue ; accent).
	Appearance struct {
		Theme  *string `json:"theme"`
		Accent *string `json:"accent"`
	} `json:"appearance"`
}

// Me vérifie la clé et renvoie le compte.
func (c *Client) Me(ctx context.Context) (*Me, error) {
	var m Me
	if err := c.doJSON(ctx, nil, http.MethodGet, "/api/me", nil, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// MatchItem est une empreinte de torrent local à comparer au catalogue.
// Key est une clé locale opaque (renvoyée telle quelle) ; InfoHash reste vide
// pour un torrent d'un autre tracker (voir crossseed).
type MatchItem struct {
	Key         string `json:"key"`
	InfoHash    string `json:"infoHash,omitempty"`
	ContentHash string `json:"contentHash"`
	SizeHash    string `json:"sizeHash"`
	Total       int64  `json:"total"`
}

// Match est la release de V3X correspondant à un torrent local.
type Match struct {
	Kind     string `json:"kind"` // exact, renamed, self
	ID       string `json:"id"`
	Name     string `json:"name"`
	InfoHash string `json:"infoHash"`
	Seeders  int    `json:"seeders"`
	Leechers int    `json:"leechers"`
	Size     int64  `json:"size"`
	// Dead : sans seeder depuis 72 h ; la seeder de nouveau rapporte la prime
	// de résurrection (Prime jetons, une fois, après 24 h de seed).
	Dead  bool `json:"dead,omitempty"`
	Prime int  `json:"prime,omitempty"`
	// Gone (Kind « gone ») : aucune fiche en ligne, mais une fiche du même
	// contenu a été retirée, remplacée ou archivée (issue GitHub #1, peplade).
	Gone *Gone `json:"gone,omitempty"`
	// NameCheck : petite release rapprochée par les tailles seules (1.1.29) ;
	// D3X ne la garde que si le nom est le même, à la casse et à la
	// ponctuation près.
	NameCheck bool `json:"nameCheck,omitempty"`
}

// Gone : la fiche V3X retirée d'un contenu. Reason n'est donnée qu'à son
// propre uploadeur (Own), comme dans Mes uploads sur le site.
type Gone struct {
	Name   string `json:"name"`
	Status string `json:"status"` // removed, trumped, archived
	Own    bool   `json:"own,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Text : la phrase montrée au membre.
func (g *Gone) Text() string {
	verb := "retirée"
	switch g.Status {
	case "trumped":
		verb = "remplacée par une meilleure version"
	case "archived":
		verb = "archivée"
	}
	s := "une fiche V3X de ce contenu a été " + verb
	if g.Own && strings.TrimSpace(g.Reason) != "" {
		s += " (ta fiche, motif : " + strings.TrimSpace(g.Reason) + ")"
	}
	return s
}

// MatchBatchSize est le nombre maximal d'empreintes par appel.
const MatchBatchSize = 500

// MatchBatch compare jusqu'à 500 empreintes au catalogue.
func (c *Client) MatchBatch(ctx context.Context, r *Retry, items []MatchItem) (map[string]*Match, error) {
	var res struct {
		Results []struct {
			Key     string `json:"key"`
			Match   *Match `json:"match"`
			Removed *Gone  `json:"removed"`
		} `json:"results"`
		ReseedPrime int `json:"reseedPrime"`
	}
	if err := c.doJSON(ctx, r, http.MethodPost, "/api/d3x/match", map[string]interface{}{"items": items, "smallNames": true}, &res); err != nil {
		return nil, err
	}
	out := make(map[string]*Match, len(res.Results))
	for _, x := range res.Results {
		if x.Match != nil && x.Match.Dead {
			x.Match.Prime = res.ReseedPrime
		}
		if x.Match == nil && x.Removed != nil {
			x.Match = &Match{Kind: "gone", Name: x.Removed.Name, Gone: x.Removed}
		}
		out[x.Key] = x.Match
	}
	return out, nil
}

// DownloadTorrent récupère le .torrent V3X d'une release, à ta passkey.
// Passe par la route des indexeurs : la clé doit avoir le droit « Torznab ».
// La clé part en en-tête, jamais dans l'adresse.
func (c *Client) DownloadTorrent(ctx context.Context, r *Retry, id string) ([]byte, error) {
	return c.send(ctx, r, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/torznab/download?"+url.Values{"id": {id}}.Encode(), nil)
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+c.Key)
		}
		return req, err
	})
}

// unreachable : erreur réseau SANS l'adresse demandée (Go la recopie dans
// ses erreurs, et une adresse peut porter un identifiant).
func unreachable(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("V3X injoignable : %v", ue.Err)
	}
	return fmt.Errorf("V3X injoignable : %w", err)
}

// TorrentURL renvoie l'adresse de la fiche sur le site.
func TorrentURL(id string) string { return "https://v3x.club/torrents/" + id }
