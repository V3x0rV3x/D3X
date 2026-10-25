package batch

import (
	"encoding/json"
	"strings"
)

// Text : un champ du site qui arrive en chaîne ou en nombre (année, id,
// saison), gardé tel quel en texte ; null, absent ou autre type : vide.
type Text string

func (t *Text) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	switch {
	case s == "":
		*t = ""
	case s[0] == '"':
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*t = Text(v)
	case s[0] == '-' || s[0] >= '0' && s[0] <= '9':
		*t = Text(s)
	default: // null, booléen, tableau, objet : vide
		*t = ""
	}
	return nil
}

func (t Text) String() string { return string(t) }

// Work : une œuvre TMDB (résultat de recherche du site, ou série mémorisée).
type Work struct {
	Type          string  `json:"type"` // movie, tv
	ID            Text    `json:"id"`
	Title         string  `json:"title"`
	OriginalTitle string  `json:"originalTitle,omitempty"`
	Year          Text    `json:"year,omitempty"`
	PosterURL     *string `json:"posterUrl"`
}

// File : un fichier de la release (chemin dans la release, taille).
type File struct {
	Rel  string
	Size int64
}
