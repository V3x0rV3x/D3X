package maketorrent

import (
	"path/filepath"
	"runtime"
	"strings"
)

// Noms qui finissent par un point ou une espace (« Agents of S.H.I.E.L.D. »,
// « Vol. 1. ») : ils sont permis sur un NAS ou une seedbox sous Linux, mais
// Windows les retire d'un chemin ordinaire. Le dossier ou le fichier devenait
// introuvable, D3X l'écartait comme « illisible » et fabriquait le .torrent
// sans lui, sans autre signe que le nombre d'écartés (Turias, 18/11/26 : un
// pack de 2,3 Tio réduit à 274 Gio). Le préfixe \\?\ demande à Windows de
// prendre le chemin tel quel.

// OSPath : le chemin à donner au système pour lister, lire ou ouvrir p (un
// chemin absolu). Inchangé hors de Windows, et sous Windows quand aucun nom
// du chemin ne finit par un point ou une espace.
func OSPath(p string) string { return osPath(runtime.GOOS, p) }

func osPath(goos, p string) string {
	if goos != "windows" {
		return p
	}
	q := strings.ReplaceAll(p, "/", `\`)
	if !trailingDotOrSpace(q) {
		return p
	}
	switch {
	case strings.HasPrefix(q, `\\?\`), strings.HasPrefix(q, `\\.\`):
		return q
	case strings.HasPrefix(q, `\\`): // partage réseau : \\nas\partage\…
		return `\\?\UNC\` + q[2:]
	case len(q) >= 3 && q[1] == ':' && q[2] == '\\': // lecteur : D:\…
		return `\\?\` + q
	}
	return p
}

// trailingDotOrSpace : un nom du chemin finit par un point ou une espace. Un
// segment « . » ou « .. » (chemin pas nettoyé) l'exclut : avec le préfixe,
// Windows ne les résoudrait plus.
func trailingDotOrSpace(p string) bool {
	found := false
	for _, seg := range strings.Split(p, `\`) {
		switch {
		case seg == "." || seg == "..":
			return false
		case strings.HasSuffix(seg, ".") || strings.HasSuffix(seg, " "):
			found = true
		}
	}
	return found
}

// AbsPath : filepath.Abs sans perdre le point ou l'espace qui finit un nom
// (sous Windows, Abs les retire : « Vol. 1. » devenait « Vol. 1 », un autre
// dossier ou aucun). Un chemin déjà absolu est seulement nettoyé.
func AbsPath(p string) (string, error) {
	if filepath.IsAbs(p) {
		return filepath.Clean(p), nil
	}
	return filepath.Abs(p)
}
