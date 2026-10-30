package names

import (
	"runtime"

	"golang.org/x/text/unicode/norm"
)

// FromDisk : un nom lu sur le disque de cette machine, écrit comme le verront
// les autres. macOS rend les noms en Unicode décomposé (NFD : « e » suivi de
// l'accent), même ceux d'un partage réseau Windows, quand Windows et Linux les
// écrivent composés (NFC : « é ») : le .torrent fabriqué sur un Mac ne
// retrouvait pas les fichiers sur le PC qui les partage (ilfaitchaud,
// 30/10/26). macOS retrouve un fichier sous ses deux formes, le nom est donc
// recomposé sur un Mac seulement ; ailleurs, un nom est une suite d'octets
// exacte et reste tel quel.
func FromDisk(name string) string { return fromDisk(runtime.GOOS, name) }

func fromDisk(goos, name string) string {
	if goos != "darwin" {
		return name
	}
	return norm.NFC.String(name)
}
