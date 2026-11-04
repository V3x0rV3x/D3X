package crossseed

import (
	"sort"
	"strings"

	"github.com/V3x0rV3x/D3X/internal/client"
	"github.com/V3x0rV3x/D3X/internal/names"
	"github.com/V3x0rV3x/D3X/internal/pathmap"
)

// TagsOf : les étiquettes des torrents qui couvrent chaque chemin (vu par
// D3X), tous clients confondus. Un torrent couvre un chemin si son contenu,
// traduit du côté du client, est ce chemin, est dedans, ou le contient : une
// release du Lot peut être le contenu d'un torrent, un fichier d'un torrent
// (dossier d'une saison parcouru) ou un dossier qui en contient plusieurs
// (collection). Les deux côtés passent par names.FromDisk : sur Mac, sans
// règle de correspondance, D3X lit le chemin décomposé (NFD) du disque alors
// que le client donne le nom composé du .torrent, et ToClient ne recompose
// que dans une règle. Dédoublonnées sans la casse, triées. Un chemin sans torrent
// n'a pas de clé. Sert au Lot à écarter les releases d'un torrent noHL
// (foux, 04/11/26).
func TagsOf(paths []string, lists []Listed) map[string][]string {
	out := map[string][]string{}
	seen := map[string]map[string]bool{} // chemin → étiquettes déjà prises (minuscules)
	add := func(path string, tags []string) {
		if seen[path] == nil {
			seen[path] = map[string]bool{}
		}
		for _, tag := range tags {
			k := strings.ToLower(tag)
			if seen[path][k] {
				continue
			}
			seen[path][k] = true
			out[path] = append(out[path], tag)
		}
	}
	type tor struct {
		path string
		tags []string
		root bool // le contenu est un dossier de release : il couvre ce qui est dedans
	}
	for _, l := range lists {
		// Les chemins des torrents, normalisés une fois par client (normPath :
		// séparateurs unifiés, lecteur Windows sans la casse) : 4 600 torrents
		// sur 500 releases font 2,3 millions de comparaisons.
		tors := make([]tor, 0, len(l.List))
		for _, t := range l.List {
			if len(t.Tags) == 0 || strings.TrimSpace(t.ContentPath) == "" {
				continue
			}
			p := normPath(names.FromDisk(t.ContentPath))
			tors = append(tors, tor{p, t.Tags, releaseRoot(p, t)})
		}
		if len(tors) == 0 {
			continue
		}
		for _, p := range paths {
			cp := normPath(names.FromDisk(pathmap.ToClient(p, l.Target.PathMap)))
			if cp == "" {
				continue // un chemin vide « contiendrait » tout
			}
			for _, t := range tors {
				if underNorm(t.path, cp) || (t.root && underNorm(cp, t.path)) {
					add(p, t.tags)
				}
			}
		}
	}
	for _, tags := range out {
		sort.Slice(tags, func(i, j int) bool { return strings.ToLower(tags[i]) < strings.ToLower(tags[j]) })
	}
	return out
}

// releaseRoot : le contenu du torrent (p, normalisé) est sa propre racine, et
// couvre donc ce qui est dedans (un épisode d'un pack de saison). Un torrent
// multi-fichiers sans dossier racine de qBittorrent a pour contenu le dossier
// d'enregistrement lui-même (« /data/torrents/tv », voire « D:\ ») : il ne
// couvre pas tout ce dossier (revue du 04/11/26). Racine : le dernier élément
// est le nom du torrent, ou, quand le client donne le dossier d'enregistrement,
// le contenu n'est pas ce dossier (racine renommée dans le client).
func releaseRoot(p string, t client.Torrent) bool {
	if name := strings.TrimSpace(names.FromDisk(t.Name)); name != "" && strings.EqualFold(baseName(p), name) {
		return true
	}
	if sp := strings.TrimSpace(t.SavePath); sp != "" {
		return normPath(names.FromDisk(sp)) != p
	}
	return false
}

// underNorm : p est dir, ou dedans (à une frontière de dossier) ; les deux
// déjà passés par normPath (séparateurs unifiés, lecteur Windows en minuscules).
func underNorm(p, dir string) bool {
	return p == dir || (len(p) > len(dir) && p[len(dir)] == '/' && p[:len(dir)] == dir)
}

// GuessRules : les listes des clients, chacune avec sa correspondance de
// dossiers complétée, pour les dossiers des chemins qu'aucune règle ne couvre,
// de la règle devinée d'après ses torrents déjà rangés dans ce dossier (comme
// Publier : pathmap.SuggestNames). dirOf : le dossier parent d'un chemin, vu
// par D3X. Les listes données ne sont pas modifiées.
func GuessRules(lists []Listed, paths []string, dirOf func(string) string) []Listed {
	byDir := map[string][]string{} // dossier → noms des chemins qu'il contient
	var dirs []string
	for _, p := range paths {
		d := dirOf(p)
		if d == "" || d == p {
			continue
		}
		if _, ok := byDir[d]; !ok {
			dirs = append(dirs, d)
		}
		byDir[d] = append(byDir[d], baseName(p))
	}
	out := make([]Listed, len(lists))
	for i, l := range lists {
		out[i] = l
		var clientPaths []string
		for _, d := range dirs {
			if pathmap.Covers(d, l.Target.PathMap) {
				continue
			}
			if clientPaths == nil {
				for _, t := range l.List {
					if t.ContentPath != "" {
						clientPaths = append(clientPaths, t.ContentPath)
					}
				}
			}
			if rule, n := pathmap.SuggestNames(d, byDir[d], clientPaths); n > 0 && rule.To != "" {
				out[i].Target.PathMap = append(append([]pathmap.Rule(nil), out[i].Target.PathMap...), rule)
			}
		}
	}
	return out
}
