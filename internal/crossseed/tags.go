package crossseed

import (
	"sort"
	"strings"

	"github.com/V3x0rV3x/D3X/internal/pathmap"
)

// TagsOf : les étiquettes des torrents qui couvrent chaque chemin (vu par
// D3X), tous clients confondus. Un torrent couvre un chemin si son contenu,
// traduit du côté du client, est ce chemin, est dedans, ou le contient : une
// release du Lot peut être le contenu d'un torrent, un fichier d'un torrent
// (dossier d'une saison parcouru) ou un dossier qui en contient plusieurs
// (collection). Dédoublonnées sans la casse, triées. Un chemin sans torrent
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
	}
	for _, l := range lists {
		// Les chemins des torrents, normalisés une fois : 4 600 torrents sur
		// 500 releases font 2,3 millions de comparaisons.
		tors := make([]tor, 0, len(l.List))
		for _, t := range l.List {
			if len(t.Tags) == 0 || strings.TrimSpace(t.ContentPath) == "" {
				continue
			}
			tors = append(tors, tor{normSlash(t.ContentPath), t.Tags})
		}
		if len(tors) == 0 {
			continue
		}
		for _, p := range paths {
			cp := normSlash(pathmap.ToClient(p, l.Target.PathMap))
			for _, t := range tors {
				if underNorm(t.path, cp) || underNorm(cp, t.path) {
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

// normSlash : séparateurs unifiés, sans séparateur final (comme under).
func normSlash(p string) string { return strings.TrimRight(strings.ReplaceAll(p, `\`, "/"), "/") }

// underNorm : p est dir, ou dedans ; les deux déjà passés par normSlash. Un
// chemin de lecteur Windows (« d: ») se compare sans la casse, comme samePath.
func underNorm(p, dir string) bool {
	if p == dir || strings.HasPrefix(p+"/", dir+"/") {
		return true
	}
	if len(dir) < 2 || dir[1] != ':' {
		return false
	}
	lp, ld := strings.ToLower(p), strings.ToLower(dir)
	return lp == ld || strings.HasPrefix(lp+"/", ld+"/")
}
