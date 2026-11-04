package server

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/V3x0rV3x/D3X/internal/bencode"
	"github.com/V3x0rV3x/D3X/internal/torrent"
)

// Faux V3X pour la vue « Publier » du mode démo : mêmes routes et mêmes formes
// de réponse que le site (contexte, TMDB, fiche, aperçu, publication).

type demoPub struct {
	mu       sync.Mutex
	uploaded map[string][]byte // id → .torrent avec announce
	n        int
}

// demoRelease crée une fausse release à publier et renvoie son dossier parent.
func demoRelease(root string) string {
	parent := filepath.Join(root, "Releases")
	name := "Demonstration.2024.MULTi.1080p.WEB.H264-DEMO"
	dir := filepath.Join(parent, name)
	_ = os.MkdirAll(dir, 0o755)
	b := make([]byte, 24<<20)
	rand.New(rand.NewSource(42)).Read(b)
	_ = os.WriteFile(filepath.Join(dir, name+".mkv"), b, 0o644)
	_ = os.WriteFile(filepath.Join(dir, name+".nfo"), []byte("Release de démonstration\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, ".DS_Store"), []byte("x"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "extrafanart"), 0o755)
	_ = os.WriteFile(filepath.Join(parent, "Autre.Fichier.Seul.2023.1080p.WEB.H264.mkv"), b[:3<<20], 0o644)
	// Releases piégées pour le mode lot.
	soft := filepath.Join(parent, "Logiciel.Demo.v1.0")
	_ = os.MkdirAll(soft, 0o755)
	_ = os.WriteFile(filepath.Join(soft, "setup.exe"), b[:1<<20], 0o644)
	_ = os.WriteFile(filepath.Join(parent, "Livre.Demo.2022.epub"), b[:2<<20], 0o644)
	game := filepath.Join(parent, "Jeu.Demo.2024.PC-DEMO")
	_ = os.MkdirAll(game, 0o755)
	_ = os.WriteFile(filepath.Join(game, "setup_jeu_demo.exe"), b[:1<<20], 0o644)
	_ = os.WriteFile(filepath.Join(game, "setup_jeu_demo-1.bin"), b[:4<<20], 0o644)
	coll := filepath.Join(parent, "Collection.Demo")
	for _, n := range []string{"Film.Un.2001.1080p-DEMO", "Film.Deux.2002.1080p-DEMO"} {
		_ = os.MkdirAll(filepath.Join(coll, n), 0o755)
		_ = os.WriteFile(filepath.Join(coll, n, n+".mkv"), b[:2<<20], 0o644)
	}
	demoMusicRelease(parent)
	return parent
}

// demoSeedbox range des releases dans le dossier personnel de la seedbox
// de démonstration.
func demoSeedbox(home string) {
	dl := filepath.Join(home, "downloads")
	b := make([]byte, 6<<20)
	rand.New(rand.NewSource(7)).Read(b)
	serie := filepath.Join(dl, "Serie.Demo.S01.MULTi.1080p.WEB.H264-DEMO")
	_ = os.MkdirAll(serie, 0o755)
	for i := 1; i <= 3; i++ {
		n := fmt.Sprintf("Serie.Demo.S01E%02d.MULTi.1080p.WEB.H264-DEMO.mkv", i)
		_ = os.WriteFile(filepath.Join(serie, n), b[i<<10:], 0o644)
	}
	doc := filepath.Join(dl, "Documentaire.Demo.2021.2160p.WEB.H265-DEMO")
	_ = os.MkdirAll(doc, 0o755)
	_ = os.WriteFile(filepath.Join(doc, "Documentaire.Demo.2021.2160p.WEB.H265-DEMO.mkv"), b, 0o644)
	_ = os.WriteFile(filepath.Join(doc, "Documentaire.Demo.2021.2160p.WEB.H265-DEMO.nfo"), []byte("Release de démonstration\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(home, "watch"), 0o755)
}

var demoMovies = []map[string]interface{}{
	{"type": "movie", "id": 900001, "title": "Démonstration", "originalTitle": "Demonstration", "year": 2024, "posterUrl": nil, "overview": "Un film qui n'existe pas, pour essayer D3X.", "genreIds": []int{18}},
	{"type": "tv", "id": 900002, "title": "Démonstration, la série", "year": 2023, "posterUrl": nil, "overview": "", "genreIds": []int{18}},
}

func (d *demoPub) handle(w http.ResponseWriter, r *http.Request) bool {
	js := func(v interface{}) { w.Header().Set("Content-Type", "application/json"); json.NewEncoder(w).Encode(v) }
	switch {
	case r.URL.Path == "/api/d3x/context":
		js(map[string]interface{}{
			"user": map[string]interface{}{"username": "membre-demo", "role": "uploader", "canHtml": true, "anonymousDefault": false,
				"team": map[string]interface{}{"name": "Équipe Démo", "tag": "DEMO", "tagAlt": nil, "banner": "[center][b]Équipe Démo[/b][/center]", "nfoAscii": nil}},
			"categories": []map[string]interface{}{
				{"id": 1, "name": "Films", "slug": "films", "children": []map[string]interface{}{{"id": 11, "name": "Film", "slug": "films-videos-film"}, {"id": 12, "name": "Animation", "slug": "films-videos-animation"}}},
				{"id": 2, "name": "Séries", "slug": "series", "children": []map[string]interface{}{{"id": 21, "name": "Série TV", "slug": "films-videos-serie-tv"}}},
				{"id": 3, "name": "Ebooks", "slug": "ebook", "children": []map[string]interface{}{{"id": 31, "name": "Livres", "slug": "ebook-livres"}}},
				{"id": 4, "name": "Audio", "slug": "audio", "children": []map[string]interface{}{{"id": 41, "name": "Musique", "slug": "audio-musique"}, {"id": 42, "name": "Podcast Radio", "slug": "audio-podcast-radio"}}},
				{"id": 5, "name": "Jeux vidéo", "slug": "jeux-video", "children": []map[string]interface{}{{"id": 51, "name": "PC", "slug": "jeux-video-pc"}}},
			},
			"languages": []string{"", "Multi (FR inclus)", "Français", "VFQ", "VOSTFR", "VO", "Muet"},
			"templates": []map[string]string{{"id": "sobre", "name": "Sobre", "body": ""}, {"id": "complet", "name": "Complet", "body": ""}},
		})
	case r.URL.Path == "/api/d3x/tmdb/search", r.URL.Path == "/api/d3x/tmdb/lookup":
		js(map[string]interface{}{"results": demoMovies})
	case r.URL.Path == "/api/d3x/igdb/search":
		js(map[string]interface{}{"configured": true, "results": []map[string]interface{}{
			{"id": 900101, "name": "Jeu de démonstration", "year": "2024", "coverUrl": nil, "platforms": []string{"PC", "PS5"}},
			{"id": 900102, "name": "Jeu de démonstration 2", "year": "2026", "coverUrl": nil, "platforms": []string{"PC"}},
		}})
	case r.URL.Path == "/api/d3x/fiche":
		var in struct {
			ReleaseName string  `json:"releaseName"`
			MediaInfo   string  `json:"mediainfo"`
			TmdbRef     *string `json:"tmdbRef"`
			MbRef       *string `json:"mbRef"`
			IgdbID      *int64  `json:"igdbId"`
			BookRef     *string `json:"bookRef"`
			Category    *string `json:"categorySlug"`
			Download    struct {
				Name      string `json:"name"`
				TotalSize int64  `json:"totalSize"`
				FileCount int    `json:"fileCount"`
			} `json:"download"`
			TemplateID *string `json:"templateId"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		if in.MbRef != nil || (in.Category != nil && *in.Category == "audio-musique") {
			js(demoMusicFiche(in.ReleaseName, in.MbRef))
			return true
		}
		year := regexp.MustCompile(`(19|20)\d{2}`).FindString(in.ReleaseName)
		title := strings.ReplaceAll(strings.SplitN(in.ReleaseName, "."+year, 2)[0], ".", " ")
		team := strings.HasSuffix(in.ReleaseName, "-DEMO")
		pres := strings.TrimSpace(bbSection("Téléchargement") + bbTable([][2]string{{"Release", in.ReleaseName}, {"Taille", demoSize(in.Download.TotalSize)}, {"Fichiers", fmt.Sprint(in.Download.FileCount)}}))
		var cat interface{}
		if in.TmdbRef != nil {
			pres = demoFilmFiche(in.ReleaseName, in.Download.TotalSize, in.Download.FileCount, team)
			cat = map[string]interface{}{"id": 11, "slug": "films-videos-film"}
		}
		if in.TemplateID != nil && *in.TemplateID == "complet" {
			pres += "\n\n[spoiler=MediaInfo][code]" + in.MediaInfo + "[/code][/spoiler]"
		}
		warnings := []string{}
		if in.MediaInfo == "" && (in.Category == nil || !strings.HasPrefix(*in.Category, "jeux-video")) && in.IgdbID == nil {
			warnings = append(warnings, "mediainfo_missing")
		}
		// Jeu choisi : la fiche jeu du site (jaquette, genres, config requise).
		var game interface{}
		if in.IgdbID != nil {
			name, y := "Jeu de démonstration", "2024"
			if *in.IgdbID == 900102 {
				name, y = "Jeu de démonstration 2", "2026"
			}
			title = name + " (" + y + ")"
			pres = "[center][size=6][b]" + title + "[/b][/size][/center]\n\n" + bbSection("Informations") + bbTable([][2]string{{"Genres", "Aventure, Stratégie"}, {"Développeur", "Studio Démo"}, {"Plateformes", "PC, PS5"}}) + "\n" + bbSection("Configuration requise") + "Système : Windows 10\nMémoire : 8 Go\n\n" + bbSection("Téléchargement") + bbTable([][2]string{{"Release", in.ReleaseName}, {"Taille", demoSize(in.Download.TotalSize)}, {"Fichiers", fmt.Sprint(in.Download.FileCount)}})
			game = map[string]interface{}{"id": *in.IgdbID, "name": name, "year": y, "title": title, "posterUrl": nil}
		}
		// Livre : un ISBN de démonstration, tout le reste est « introuvable ».
		var book interface{}
		if in.BookRef != nil {
			if strings.ReplaceAll(*in.BookRef, "-", "") == "9782070360024" {
				title = "Albert Camus - L'Étranger (1942)"
				pres = "[center][size=6][b]L'Étranger[/b][/size]\n[i]Albert Camus[/i][/center]\n\n" + bbSection("Synopsis") + "Aujourd'hui, maman est morte…\n\n" + bbSection("Informations") + bbTable([][2]string{{"Auteur", "Albert Camus"}, {"Année", "1942"}, {"ISBN", "9782070360024"}})
				book = map[string]interface{}{"title": title, "bookTitle": "L'Étranger", "author": "Albert Camus", "year": "1942", "posterUrl": nil}
				cat = map[string]interface{}{"id": 31, "slug": "ebook-livres"}
			} else {
				warnings = append(warnings, "book_not_found")
			}
		}
		js(map[string]interface{}{
			"game":          game,
			"book":          book,
			"analysis":      map[string]interface{}{"title": title, "tokens": []map[string]string{{"kind": "year", "value": year}, {"kind": "resolution", "value": "1080p"}, {"kind": "source", "value": "WEB"}, {"kind": "codec", "value": "H264"}, {"kind": "group", "value": "DEMO"}}, "warnings": []string{}, "score": 90},
			"language":      "Multi (FR inclus)",
			"season":        nil,
			"suggestedName": strings.Replace(in.ReleaseName, "WEB.H264", "WEB.EAC3.5.1.H264", 1),
			"title":         title,
			"tmdbQuery":     title + " " + year,
			"category":      cat,
			"teamRelease":   team,
			"nfo":           "Release de démonstration\n\n" + in.MediaInfo,
			"presentation":  pres,
			"warnings":      warnings,
		})
	case r.URL.Path == "/api/d3x/versions":
		// Deux versions du même film, dont une de presque la même taille, et
		// un REPACK de la release elle-même.
		name := r.URL.Query().Get("name")
		size, _ := strconv.ParseInt(r.URL.Query().Get("size"), 10, 64)
		if r.URL.Query().Get("tmdb") == "" || size == 0 {
			js(map[string]interface{}{"versions": []interface{}{}, "redo": []interface{}{}})
			return true
		}
		repack := strings.Replace(name, ".1080p.", ".REPACK.1080p.", 1)
		redo := map[string]interface{}{"id": "00000071-0000-4000-8000-000000000071", "name": repack, "size": size + 4096, "seeders": 6, "leechers": 1, "createdAt": "2026-10-20T10:00:00Z", "close": true, "redo": true}
		js(map[string]interface{}{
			"versions": []map[string]interface{}{
				redo,
				{"id": "00000072-0000-4000-8000-000000000072", "name": strings.Replace(name, "-DEMO", "-AUTRE", 1), "size": size * 102 / 100, "seeders": 12, "leechers": 0, "createdAt": "2026-09-12T10:00:00Z", "close": true, "redo": false},
				{"id": "00000073-0000-4000-8000-000000000073", "name": strings.Replace(name, ".1080p.", ".2160p.", 1), "size": size * 4, "seeders": 3, "leechers": 2, "createdAt": "2026-08-30T10:00:00Z", "close": false, "redo": false},
			},
			"redo": []interface{}{redo},
		})
	case r.URL.Path == "/api/d3x/preview":
		var in struct{ BBCode string }
		json.NewDecoder(r.Body).Decode(&in)
		js(map[string]string{"html": demoBBCodeHTML(in.BBCode)})
	case r.URL.Path == "/api/d3x/bbcode.css":
		w.Header().Set("Content-Type", "text/css")
		io.WriteString(w, demoBBCodeCSS)
	case r.URL.Path == "/api/torrents" && r.Method == http.MethodPost:
		r.ParseMultipartForm(8 << 20)
		f, _, err := r.FormFile("file")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			js(map[string]string{"error": "no_file"})
			return true
		}
		raw, _ := io.ReadAll(f)
		p, err := torrent.Parse(raw)
		if err != nil || r.FormValue("rightsDeclared") != "true" || r.FormValue("categoryId") == "" {
			w.WriteHeader(http.StatusBadRequest)
			js(map[string]string{"error": "validation"})
			return true
		}
		d.mu.Lock()
		d.n++
		id := fmt.Sprintf("%08x-1111-4000-8000-%012x", 0xd3d3, d.n)
		dec, _ := bencode.Decode(raw)
		root := dec.Root.(bencode.Dict)
		root["announce"] = []byte("https://announce.v3x.club/passkey-demo/announce")
		out, _ := bencode.Encode(root)
		d.uploaded[id] = out
		d.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		js(map[string]interface{}{"id": id, "status": "active", "infoHash": p.InfoHash, "uploadBonus": 400, "download": "/torrents/" + id + "/download"})
	case strings.HasPrefix(r.URL.Path, "/torrents/") && strings.HasSuffix(r.URL.Path, "/download"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/torrents/"), "/download")
		d.mu.Lock()
		b := d.uploaded[id]
		d.mu.Unlock()
		if b == nil {
			w.WriteHeader(http.StatusNotFound)
			return true
		}
		w.Write(b)
	default:
		return false
	}
	return true
}
