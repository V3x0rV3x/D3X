package crossseed

import (
	"strings"
	"testing"

	"github.com/V3x0rV3x/D3X/internal/client"
)

func TestForeignDir(t *testing.T) {
	linux := []client.Torrent{{ContentPath: "/share/Series2/From.S01"}, {ContentPath: "/share/Films/Le Pari (1997)"}}
	windows := []client.Torrent{{ContentPath: `D:\Torrents\From.S01`}, {ContentPath: `\\NAS\Films\Le Pari`}}
	cases := []struct {
		dir  string
		list []client.Torrent
		want bool
	}{
		// Le cas de ftpktf : partage réseau de ce PC, qBittorrent sous Linux.
		{`\\NAS-Jo\Series\From`, linux, true},
		{`Z:\Series\From`, linux, true},
		// Client sur ce PC : ses torrents sont déjà sous des chemins Windows.
		{`\\NAS-Jo\Series\From`, windows, false},
		// Chemin déjà traduit pour le client.
		{`/share/Series/From`, linux, false},
		// Client vide : rien pour conclure.
		{`\\NAS-Jo\Series\From`, nil, false},
		// Majorité Windows avec un torrent Linux égaré : pas concerné.
		{`Z:\x`, append(append([]client.Torrent{}, windows...), linux[0]), false},
	}
	for _, c := range cases {
		if got := ForeignDir(c.dir, c.list); got != c.want {
			t.Errorf("ForeignDir(%q, %d torrents) = %v, voulu %v", c.dir, len(c.list), got, c.want)
		}
	}
	msg := ForeignDirError(`\\NAS-Jo\Series`, "qBittorrent").Error()
	if !strings.Contains(msg, "Dossiers du client") || !strings.Contains(msg, `\\NAS-Jo\Series`) {
		t.Errorf("message incomplet : %s", msg)
	}
}
