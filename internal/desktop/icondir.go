package desktop

import "encoding/binary"

// icoImage rend, dans un fichier .ico, l'image la mieux adaptée à size pixels :
// la plus petite au moins aussi grande, sinon la plus grande. Les octets rendus
// (PNG, ou BMP sans en-tête de fichier) sont ceux qu'attend
// CreateIconFromResourceEx. nil si le fichier est illisible.
func icoImage(ico []byte, size int) []byte {
	if len(ico) < 6 || binary.LittleEndian.Uint16(ico[2:]) != 1 {
		return nil
	}
	n := int(binary.LittleEndian.Uint16(ico[4:]))
	var best []byte
	bestSize := 0
	for i := 0; i < n; i++ {
		e := 6 + 16*i
		if e+16 > len(ico) {
			break
		}
		w := int(ico[e])
		if w == 0 {
			w = 256
		}
		ln := int(binary.LittleEndian.Uint32(ico[e+8:]))
		off := int(binary.LittleEndian.Uint32(ico[e+12:]))
		if ln <= 0 || off < 6 || off > len(ico)-ln {
			continue
		}
		better := best == nil ||
			(w >= size && (bestSize < size || w < bestSize)) ||
			(w < size && bestSize < size && w > bestSize)
		if better {
			best, bestSize = ico[off:off+ln], w
		}
	}
	return best
}
