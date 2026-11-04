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

// Seedbox sans correspondance de dossiers réglée : la règle est devinée
// d'après ses torrents déjà rangés dans le dossier, comme dans Publier, et
// TagsOf retrouve alors ses étiquettes. Un client dont une règle couvre déjà
// le dossier ne change pas.
func TestGuessRules(t *testing.T) {
	box := Listed{Target: Target{ID: "box"}, List: []client.Torrent{
		{ContentPath: "/home/box/dl/Film.A", Tags: []string{"noHL"}},
		{ContentPath: "/home/box/dl/Film.B", Tags: []string{"V3X"}},
		{ContentPath: "/home/box/other/Film.C"},
	}}
	pc := Listed{Target: Target{ID: "pc", PathMap: []pathmap.Rule{{From: `Z:\T`, To: "/data"}}}, List: []client.Torrent{
		{ContentPath: "/data/Film.A", Tags: []string{"pc"}},
	}}
	paths := []string{`Z:\T\Film.A`, `Z:\T\Film.B`, `Z:\T\Film.D`}
	dirOf := func(string) string { return `Z:\T` }
	lists := GuessRules([]Listed{box, pc}, paths, dirOf)
	if len(lists) != 2 || len(lists[0].Target.PathMap) != 1 || lists[0].Target.PathMap[0] != (pathmap.Rule{From: `Z:\T`, To: "/home/box/dl"}) {
		t.Fatalf("règle devinée : %+v", lists[0].Target.PathMap)
	}
	if len(lists[1].Target.PathMap) != 1 || len(box.Target.PathMap) != 0 {
		t.Fatalf("le client réglé change, ou l'entrée est modifiée : %+v %+v", lists[1].Target.PathMap, box.Target.PathMap)
	}
	got := TagsOf(paths, lists)
	want := map[string][]string{`Z:\T\Film.A`: {"noHL", "pc"}, `Z:\T\Film.B`: {"V3X"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TagsOf avec la règle devinée :\n%v\nattendu\n%v", got, want)
	}
	// Rien à deviner : aucun nom commun.
	if l := GuessRules([]Listed{box}, []string{`Z:\T\Autre`}, dirOf); len(l[0].Target.PathMap) != 0 {
		t.Fatalf("règle inventée : %+v", l[0].Target.PathMap)
	}
}

// Torrent multi-fichiers sans dossier racine (qBittorrent) : son contenu est
// le dossier d'enregistrement lui-même. Il ne couvre pas tout ce dossier, ni
// tout le disque (revue du 04/11/26) ; un vrai dossier de release (pack de
// saison), même renommé dans le client, couvre toujours ce qui est dedans.
func TestTagsOfSaveDir(t *testing.T) {
	pc := Listed{Target: Target{ID: "pc"}, List: []client.Torrent{
		{Name: "Pack.Sans.Racine", ContentPath: "/data/torrents/tv", SavePath: "/data/torrents/tv/", Tags: []string{"noHL"}},
		{Name: "Autre.Pack", ContentPath: `D:\`, SavePath: `D:\`, Tags: []string{"disque"}},
		{Name: "Show.S01", ContentPath: "/data/series/Show.S01", SavePath: "/data/series", Tags: []string{"pack"}},
		{Name: "Nom.Origine", ContentPath: "/data/renomme/Nouveau.Nom", SavePath: "/data/renomme", Tags: []string{"renomme"}},
		{Name: "Saison.S02", ContentPath: "/data/autres/Saison.S02", Tags: []string{"transmission"}}, // sans dossier d'enregistrement : le nom suffit
		{Name: "Sans.Nom.Commun", ContentPath: "/data/inconnu", Tags: []string{"inconnu"}},           // ni nom ni dossier d'enregistrement : prudence
	}}
	paths := []string{
		"/data/torrents/tv/Autre.Serie.S02",
		"/data/torrents/tv/Film.2020",
		"/data/torrents/tv",
		`D:\Films\Film.2021`,
		"/data/series/Show.S01/Show.S01E01.mkv",
		"/data/renomme/Nouveau.Nom/E01.mkv",
		"/data/autres/Saison.S02/E01.mkv",
		"/data/inconnu/Film.2022",
	}
	got := TagsOf(paths, []Listed{pc})
	want := map[string][]string{
		"/data/torrents/tv":                     {"noHL"}, // la release qui est ce dossier même reste couverte
		"/data/series/Show.S01/Show.S01E01.mkv": {"pack"},
		"/data/renomme/Nouveau.Nom/E01.mkv":     {"renomme"},
		"/data/autres/Saison.S02/E01.mkv":       {"transmission"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TagsOf :\n%v\nattendu\n%v", got, want)
	}
}
