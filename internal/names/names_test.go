package names

import "testing"

func TestParse(t *testing.T) {
	for _, c := range []struct {
		in, title, year string
		tv              bool
	}{
		{"Taxi.1998.FRENCH.1080p.BluRay.x264.PopHD", "Taxi", "1998", false},
		{"Taxi (1998) MULTi [1080p] BluRay x264-PopHD.mkv", "Taxi", "1998", false},
		{"Le.Comte.de.Monte-Cristo.2024.MULTi.2160p.WEB.H265-GRP", "Le Comte de Monte-Cristo", "2024", false},
		{"The.Simpsons.S38E01.SUBFRENCH.1080p.WEB.H264-SUPPLY", "The Simpsons", "", true},
		{"Severance.2022.S02.MULTi.1080p.WEB.H264-GRP", "Severance", "2022", true},
		{"2012.2009.1080p.BluRay.x264-GRP", "2012", "2009", false},
		{"Spider-Man.Across.the.Spider-Verse.2023.1080p.WEB-DL", "Spider-Man Across the Spider-Verse", "2023", false},
		{"Malfaisante.FRENCH.WEB-GRP", "Malfaisante", "", false},
	} {
		r := Parse(c.in)
		if r.Title != c.title || r.Year != c.year || r.TV != c.tv {
			t.Errorf("%s : %+v, attendu %q %q %v", c.in, r, c.title, c.year, c.tv)
		}
	}
}

func TestSimilarity(t *testing.T) {
	if s := Similarity("Le Comte de Monte-Cristo", "le comte de monte cristo"); s != 1 {
		t.Errorf("%.2f", s)
	}
	if s := Similarity("L'Été meurtrier", "lete meurtrier"); s != 1 {
		t.Errorf("accents : %.2f", s)
	}
	if Similarity("Taxi", "Taxi Driver") >= 0.85 {
		t.Error("Taxi ≠ Taxi Driver")
	}
	if title, year := TitleOnly("Malfaisante (2026)"); title != "Malfaisante" || year != "2026" {
		t.Errorf("%q %q", title, year)
	}
}

// Un nom de release, ou un dossier rangé par une médiathèque (Hypsis, 27/10/26).
func TestLooksLikeRelease(t *testing.T) {
	for in, want := range map[string]bool{
		"1917.2019.MULTi.1080p.BluRay.x264-GROUPE.mkv":    true,
		"Film.2024.1080p.WEB.H264-D3X":                    true,
		"Taxi (1998) MULTi [1080p] BluRay x264-PopHD.mkv": true,
		"Malfaisante.FRENCH.WEB-GRP":                      true,
		"The.Simpsons.S38E01.SUBFRENCH.WEB-DL-SUPPLY":     true,
		"Film.Ancien.2003.FRENCH-GROUPE":                  true,
		"Artiste - Album (2020) [FLAC 24bit-96kHz]":       false,
		"1917 (2019)":              false,
		"Spider-Man (2002)":        false,
		"Le.Comte.de.Monte-Cristo": false,
		"The.Spider-Man.2002":      false,
		"Mes vacances":             false,
		"00001.m2ts":               false,
	} {
		if got := LooksLikeRelease(in); got != want {
			t.Errorf("%s : %v, attendu %v", in, got, want)
		}
	}
}
