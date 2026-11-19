package desktop

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// fakeICO : un .ico de trois images (16, 32 et 256 px) dont chaque contenu est
// un octet qui dit sa taille.
func fakeICO(sizes ...int) []byte {
	var head, body bytes.Buffer
	binary.Write(&head, binary.LittleEndian, [3]uint16{0, 1, uint16(len(sizes))})
	off := 6 + 16*len(sizes)
	for _, s := range sizes {
		w := byte(s)
		if s >= 256 {
			w = 0
		}
		head.Write([]byte{w, w, 0, 0})
		binary.Write(&head, binary.LittleEndian, [2]uint16{1, 32})
		binary.Write(&head, binary.LittleEndian, [2]uint32{1, uint32(off + body.Len())})
		body.WriteByte(byte(s % 251))
	}
	return append(head.Bytes(), body.Bytes()...)
}

func TestIcoImage(t *testing.T) {
	ico := fakeICO(16, 32, 256)
	for _, c := range []struct{ size, want int }{
		{16, 16}, {20, 32}, {32, 32}, {48, 256}, {300, 256}, {8, 16},
	} {
		got := icoImage(ico, c.size)
		if len(got) != 1 || int(got[0]) != c.want%251 {
			t.Errorf("taille %d : image %v, attendu %d", c.size, got, c.want)
		}
	}
	if icoImage([]byte("pas un ico"), 32) != nil {
		t.Error("un fichier illisible doit rendre nil")
	}
	// L'icône embarquée de D3X doit avoir une image pour les tailles usuelles.
	for _, s := range []int{16, 24, 32, 48} {
		if len(icoImage(Icon, s)) == 0 {
			t.Errorf("icon.ico sans image pour %d px", s)
		}
	}
}
