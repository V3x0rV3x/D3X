// Package pathmap traduit un chemin vu par D3X en chemin vu par le client
// torrent, quand les deux ne tournent pas au même endroit (qBittorrent sur un
// NAS ou dans Docker : « \\NAS\data\films » ici, « /data/films » là-bas).
package pathmap

import (
	"os"
	"strings"

	"github.com/V3x0rV3x/D3X/internal/names"
)

// Rule : le dossier From (côté D3X) est le dossier To (côté client).
type Rule struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func isWin(p string) bool {
	return strings.Contains(p, `\`) || (len(p) >= 2 && p[1] == ':')
}

func norm(p string) string {
	return strings.TrimRight(strings.ReplaceAll(p, `\`, "/"), "/")
}

// prefix remplace from par to au début de p, à une frontière de dossier.
// « / » et « \ » se valent ; la suite prend le séparateur de to ; un côté
// Windows se compare sans la casse.
func prefix(p, from, to string) (string, bool) {
	np, nf := norm(p), norm(from)
	if nf == "" || len(np) < len(nf) || (len(np) > len(nf) && np[len(nf)] != '/') {
		return "", false
	}
	head := np[:len(nf)]
	if head != nf && !(isWin(from) && strings.EqualFold(head, nf)) {
		return "", false
	}
	rest := np[len(nf):]
	if rest == "" {
		return to, true
	}
	if isWin(to) {
		rest = strings.ReplaceAll(rest, "/", `\`)
	}
	return strings.TrimRight(to, `/\`) + rest, true
}

// ToClient traduit un chemin de D3X pour le client (règle au préfixe le plus
// long ; sans règle, le chemin est rendu tel quel).
func ToClient(p string, rules []Rule) string { return apply(p, rules, true) }

// Covers dit si une règle traduit p (son dossier D3X est p ou le contient).
func Covers(p string, rules []Rule) bool {
	for _, r := range rules {
		if r.From == "" || r.To == "" {
			continue
		}
		if _, ok := prefix(p, r.From, r.To); ok {
			return true
		}
	}
	return false
}

// FromClient fait l'inverse.
func FromClient(p string, rules []Rule) string { return apply(p, rules, false) }

func apply(p string, rules []Rule, toClient bool) string {
	out, best := p, -1
	for _, r := range rules {
		from, to := r.From, r.To
		if !toClient {
			from, to = to, from
		}
		if from == "" || to == "" || len(norm(from)) <= best {
			continue
		}
		if m, ok := prefix(p, from, to); ok {
			out, best = m, len(norm(from))
		}
	}
	return out
}

// Suggest devine la règle pour un dossier local : si le client seede déjà des
// torrents rangés dans ce même dossier (même nom d'entrée), on lit où il les
// voit. Renvoie la règle la mieux appuyée et le nombre de torrents qui la
// confirment. clientPaths : chemins de contenu des torrents du client.
func Suggest(localDir string, clientPaths []string) (Rule, int) {
	entries, err := os.ReadDir(localDir)
	if err != nil {
		return Rule{}, 0
	}
	list := make([]string, 0, len(entries))
	for _, e := range entries {
		list = append(list, e.Name())
	}
	return SuggestNames(localDir, list, clientPaths)
}

// SuggestNames fait comme Suggest, avec les noms du dossier déjà lus (dossier
// d'une seedbox, lu par l'agent). Sur un Mac, les noms du dossier (accents
// décomposés) et ceux du client (souvent composés) se comparent recomposés.
func SuggestNames(localDir string, entryNames []string, clientPaths []string) (Rule, int) {
	here := map[string]bool{}
	for _, n := range entryNames {
		here[names.FromDisk(n)] = true
	}
	votes := map[string]int{}
	for _, cp := range clientPaths {
		n := norm(cp)
		i := strings.LastIndex(n, "/")
		if i <= 0 || !here[names.FromDisk(n[i+1:])] {
			continue
		}
		parent := cp[:len(strings.TrimRight(cp, `/\`))-len(n[i+1:])]
		parent = strings.TrimRight(parent, `/\`)
		if parent == "" {
			parent = "/"
		}
		votes[parent]++
	}
	var best string
	for p, v := range votes {
		if v > votes[best] || (v == votes[best] && p < best) {
			best = p
		}
	}
	if best == "" {
		return Rule{}, 0
	}
	// Déjà le même chemin des deux côtés : aucune règle nécessaire.
	if strings.EqualFold(names.FromDisk(norm(best)), names.FromDisk(norm(localDir))) {
		return Rule{}, votes[best]
	}
	return Rule{From: localDir, To: best}, votes[best]
}
