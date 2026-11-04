package crossseed

import (
	"reflect"
	"testing"

	"github.com/V3x0rV3x/D3X/internal/client"
	"github.com/V3x0rV3x/D3X/internal/pathmap"
)

// Un torrent couvre une release si son contenu est la release, un fichier
// dedans (dossier de saison parcouru) ou un dossier qu'elle contient
// (collection) ; les étiquettes de tous les torrents qui la couvrent, dans
// tous les clients, dédoublonnées sans la casse et triées.
func TestTagsOf(t *testing.T) {
	pc := Listed{Target: Target{ID: "pc"}, List: []client.Torrent{
		{ContentPath: "/data/Film.2020", Tags: []string{"noHL", "V3X"}},
		{ContentPath: "/data/Film.2020", Tags: []string{"v3x", "cross-seed"}}, // même contenu, autre tracker
		{ContentPath: "/data/Serie.S01/Serie.S01E02.mkv", Tags: []string{"LastSeed"}},
		{ContentPath: "/data/Collection/Un.2001", Tags: []string{"noHL"}},
		{ContentPath: "/data/Collection/Deux.2002", Tags: []string{"Inactive"}},
		{ContentPath: "", Tags: []string{"fantome"}}, // sans chemin : ignoré
		{ContentPath: "/data/Sans.Etiquette"},
		{ContentPath: "/data/Trailing/", Tags: []string{"noHL"}},
		{ContentPath: "/data/Film.2020.REPACK", Tags: []string{"autre"}}, // même début de nom, autre dossier
		{ContentPath: `D:\Films\Vieux.Film`, Tags: []string{"noHL"}},     // Windows : sans la casse
	}}
	box := Listed{Target: Target{ID: "box", PathMap: []pathmap.Rule{{From: `Z:\Torrents`, To: "/home/box/dl"}}}, List: []client.Torrent{
		{ContentPath: "/home/box/dl/Album.FLAC", Tags: []string{"noHL"}},
		{ContentPath: "/home/box/dl/Film.2020", Tags: []string{"TR4KER"}}, // pas le même Film.2020 : autre machine
	}}
	paths := []string{"/data/Film.2020", "/data/Serie.S01", "/data/Collection", "/data/Sans.Etiquette", "/data/Absente", `Z:\Torrents\Album.FLAC`, `d:\films\vieux.film`, "/data/Trailing", ""}
	got := TagsOf(paths, []Listed{pc, box})
	want := map[string][]string{
		"/data/Film.2020":        {"cross-seed", "noHL", "V3X"}, // v3x et V3X : une seule, la première vue
		"/data/Serie.S01":        {"LastSeed"},
		"/data/Collection":       {"Inactive", "noHL"},
		`Z:\Torrents\Album.FLAC`: {"noHL"}, // par la correspondance de dossiers de la seedbox
		`d:\films\vieux.film`:    {"noHL"},
		"/data/Trailing":         {"noHL"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TagsOf :\n%v\nattendu\n%v", got, want)
	}
	if got := TagsOf(paths, nil); len(got) != 0 {
		t.Fatalf("sans client : %v", got)
	}
}
