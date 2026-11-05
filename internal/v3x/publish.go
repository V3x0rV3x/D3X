package v3x

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"sort"
)

// Ces routes demandent une clé avec le droit « Upload ». La fiche est bâtie
// par le site lui-même (le même générateur que sa page d'upload) : une fiche
// publiée avec D3X est identique à une fiche faite sur V3X.

// PublishContext lit le contexte du membre (équipe, catégories, langues,
// modèles de présentation), relayé tel quel à la page.
func (c *Client) PublishContext(ctx context.Context) (json.RawMessage, error) {
	var raw json.RawMessage
	err := c.doJSON(ctx, nil, http.MethodGet, "/api/d3x/context", nil, &raw)
	return raw, err
}

// TMDB relaie une des routes TMDB du site (search, lookup, details, show-kind).
func (c *Client) TMDB(ctx context.Context, kind string, q url.Values) (json.RawMessage, error) {
	switch kind {
	case "search", "lookup", "details", "show-kind":
	default:
		return nil, fmt.Errorf("route TMDB inconnue : %s", kind)
	}
	var raw json.RawMessage
	err := c.doJSON(ctx, nil, http.MethodGet, "/api/d3x/tmdb/"+kind+"?"+q.Encode(), nil, &raw)
	return raw, err
}

// IGDB relaie une des routes jeux vidéo du site (search, details).
func (c *Client) IGDB(ctx context.Context, kind string, q url.Values) (json.RawMessage, error) {
	switch kind {
	case "search", "details":
	default:
		return nil, fmt.Errorf("route jeux inconnue : %s", kind)
	}
	var raw json.RawMessage
	err := c.doJSON(ctx, nil, http.MethodGet, "/api/d3x/igdb/"+kind+"?"+q.Encode(), nil, &raw)
	return raw, err
}

// Versions relaie les versions déjà sur V3X du même contenu (name, tmdb,
// size, category) et les REPACK de la release.
func (c *Client) Versions(ctx context.Context, q url.Values) (json.RawMessage, error) {
	clean := url.Values{}
	for _, k := range []string{"name", "tmdb", "size", "category"} {
		if v := q.Get(k); v != "" {
			clean.Set(k, v)
		}
	}
	var raw json.RawMessage
	err := c.doJSON(ctx, nil, http.MethodGet, "/api/d3x/versions?"+clean.Encode(), nil, &raw)
	return raw, err
}

// FicheRequest : de quoi bâtir la fiche.
type FicheRequest struct {
	ReleaseName  string        `json:"releaseName"`
	MediaInfo    string        `json:"mediainfo"`
	TmdbRef      *string       `json:"tmdbRef,omitempty"`
	MbRef        *string       `json:"mbRef,omitempty"`   // édition MusicBrainz (musique)
	IgdbID       *int64        `json:"igdbId,omitempty"`  // jeu vidéo choisi dans la recherche
	BookRef      *string       `json:"bookRef,omitempty"` // livre : lien Open Library ou ISBN
	Download     FicheDownload `json:"download"`
	CategorySlug *string       `json:"categorySlug,omitempty"`
	TemplateID   *string       `json:"templateId,omitempty"`
}

// FicheDownload décrit la release (encart « Téléchargement » de la présentation).
type FicheDownload struct {
	Name      string `json:"name"`
	TotalSize int64  `json:"totalSize"`
	FileCount int    `json:"fileCount"`
}

// Fiche demande au site la fiche proposée (nom, catégorie, présentation…).
func (c *Client) Fiche(ctx context.Context, req FicheRequest) (json.RawMessage, error) {
	var raw json.RawMessage
	err := c.doJSON(ctx, nil, http.MethodPost, "/api/d3x/fiche", req, &raw)
	return raw, err
}

// Preview rend une présentation BBCode en HTML, comme sur le site.
func (c *Client) Preview(ctx context.Context, bbcode string) (string, error) {
	var res struct {
		HTML string `json:"html"`
	}
	err := c.doJSON(ctx, nil, http.MethodPost, "/api/d3x/preview", map[string]string{"bbcode": bbcode, "format": "standard"}, &res)
	return res.HTML, err
}

// BBCodeCSS renvoie la feuille de style du rendu BBCode du site.
func (c *Client) BBCodeCSS(ctx context.Context) ([]byte, error) {
	return c.send(ctx, nil, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/api/d3x/bbcode.css", nil)
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+c.Key)
		}
		return req, err
	})
}

// Uploaded est la réponse du site à une publication.
type Uploaded struct {
	ID       string `json:"id"`
	Status   string `json:"status"` // active, pending_review
	InfoHash string `json:"infoHash"`
	Bonus    int    `json:"uploadBonus"`
	Download string `json:"download"`
}

// UploadError garde le détail d'un refus (champ fautif, doublon…).
type UploadError struct {
	Status int
	Code   string
	Body   json.RawMessage
}

func (e *UploadError) Error() string { return fmt.Sprintf("V3X a refusé la publication : %s", e.Code) }

// Upload publie une release : le .torrent et les champs de la fiche, comme la
// page d'upload du site. Avec un Retry dans le contexte (le Lot), un 429, qui
// n'a rien publié, attend la pause demandée et renvoie la même release.
func (c *Client) Upload(ctx context.Context, torrentName string, raw []byte, fields map[string]string) (*Uploaded, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		_ = w.WriteField(k, fields[k])
	}
	fw, _ := w.CreateFormFile("file", torrentName)
	fw.Write(raw)
	w.Close()
	body := buf.Bytes()
	r := retryOf(ctx, nil)
	for attempt := 0; ; attempt++ {
		if r != nil {
			if err := c.waitPause(ctx, r); err != nil {
				return nil, err
			}
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+"/api/torrents", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+c.Key)
		req.Header.Set("Content-Type", w.FormDataContentType())
		req.Header.Set("User-Agent", c.ua)
		resp, err := c.hc.Do(req)
		if err != nil {
			return nil, unreachable(err)
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests && r != nil && attempt < 5 && c.pause(resp.Header, attempt) {
			continue
		}
		if resp.StatusCode >= 300 {
			var e struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(data, &e)
			if e.Error == "" {
				e.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
			}
			return nil, &UploadError{Status: resp.StatusCode, Code: e.Error, Body: data}
		}
		var u Uploaded
		if err := json.Unmarshal(data, &u); err != nil {
			return nil, err
		}
		return &u, nil
	}
}

// OwnTorrent récupère le .torrent d'une de ses propres releases, même en
// attente de validation (route de l'uploadeur, clé « Upload »).
func (c *Client) OwnTorrent(ctx context.Context, id string) ([]byte, error) {
	return c.send(ctx, &Retry{}, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/torrents/"+url.PathEscape(id)+"/download", nil)
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+c.Key)
		}
		return req, err
	})
}

// UploadImage héberge une image sur V3X (i.v3x.club), comme le bouton Image de
// la page d'upload du site, avec la clé API (droit « Upload ») ; rend son
// adresse. Demandé par CoprinusComatus (21/10/26) : la pochette d'un album
// (cover.jpg) quand MusicBrainz n'en propose pas.
func (c *Client) UploadImage(ctx context.Context, name string, data []byte) (string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, name))
	// Le site exige un type image/… : celui que les octets annoncent.
	h.Set("Content-Type", http.DetectContentType(data))
	fw, _ := w.CreatePart(h)
	fw.Write(data)
	w.Close()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+"/images/upload", bytes.NewReader(buf.Bytes()))
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("User-Agent", c.ua)
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", unreachable(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		URL   string `json:"url"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &out)
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return "", errors.New("V3X refuse : ta clé API doit avoir le droit « Upload » (sur V3X : Paramètres ▸ Intégrations)")
	case out.Error == "too_large" || resp.StatusCode == http.StatusRequestEntityTooLarge:
		return "", errors.New("image trop lourde pour V3X (16 Mio au plus)")
	case out.Error == "invalid_image":
		return "", errors.New("V3X ne reconnaît pas ce fichier comme une image")
	case out.Error == "daily_image_quota":
		return "", errors.New("trop d'images envoyées aujourd'hui avec cette clé : réessaie demain")
	case resp.StatusCode >= 300 || out.URL == "":
		if out.Error == "" {
			out.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return "", fmt.Errorf("V3X n'a pas hébergé l'image (%s)", out.Error)
	}
	return out.URL, nil
}
