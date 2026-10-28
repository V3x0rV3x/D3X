package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/V3x0rV3x/D3X/internal/v3x"
)

func testPolicies() policyIndex {
	return indexPolicies([]v3x.TeamPolicy{
		{Name: "mHDgz", Tags: []string{"mHDgz"}, Policy: "forbidden"},
		{Name: "Team Accord", Tags: []string{"ASKT"}, Policy: "ask"},
		{Name: "Amis", Tags: []string{"AMIS"}, Policy: "forbidden", Allowed: true},
		{Name: "DreadTeam", Tags: []string{"DreadTeam"}, Policy: "banned", Reason: "encodes ratés"},
	})
}

func TestFindPolicy(t *testing.T) {
	idx := testPolicies()
	const big = 2 << 30
	tagged := "Le Temps D'un Week-end (1992) - 1080p FR EN x264 ac3 mHDgz.mkv"
	copies := []copySig{{tagged, big, 1}}
	for _, c := range []struct {
		names  []string
		size   int64
		files  int
		policy string
		detag  bool
	}{
		{[]string{"Film.2020.1080p.WEB.x264-mHDgz"}, 10, 1, "forbidden", false},
		{[]string{tagged}, big, 1, "forbidden", false},                             // tag séparé par un espace
		{[]string{"Le Temps D'un Week-end (1992).mkv"}, big, 1, "forbidden", true}, // detag
		{[]string{"", "Renommé à la main", "Le Temps D'un Week-end (1992)"}, big, 1, "forbidden", true},
		{[]string{"Le Temps D'un Week-end (1992).mkv"}, big, 2, "", false}, // autres fichiers
		{[]string{"Petit (1992).mkv"}, 50 << 20, 1, "", false},             // trop petit pour un detag
		{[]string{"Serie.S01.1080p.WEB.x264-ASKT"}, 10, 1, "ask", false},
		{[]string{"Film.2020.1080p.WEB.x264-AMIS"}, 10, 1, "", false}, // accord donné
		{[]string{"Film.2020.1080p.WEB.x264-DreadTeam.mkv"}, 10, 1, "banned", false},
		{[]string{"Libre.2021.1080p.WEB.x264-LIBRE"}, big + 1, 1, "", false},
		{[]string{"Le.Temps.1992.1080p.x264-AUTRE"}, big, 1, "forbidden", true}, // retag : même release sous un autre tag
	} {
		h, ok := findPolicy(idx, c.names, c.size, c.files, copies)
		if c.policy == "" {
			if ok {
				t.Errorf("%v : règle %+v, attendu aucune", c.names, h)
			}
			continue
		}
		if !ok || h.Policy != c.policy || (h.Detag != "") != c.detag {
			t.Errorf("%v : %+v (%v), attendu %s detag=%v", c.names, h, ok, c.policy, c.detag)
		}
	}
}

func TestPolicyTexts(t *testing.T) {
	for _, c := range []struct {
		h     policyHit
		label string
		in    string
	}{
		{policyHit{Team: "mHDgz", Policy: "forbidden", Detag: "X mHDgz.mkv"}, "Detag · Réupload interdit · mHDgz", "copie sans le tag de « X mHDgz.mkv »"},
		{policyHit{Team: "Team Accord", Policy: "ask"}, "Réupload sur accord · Team Accord", "demande son accord"},
		{policyHit{Team: "DreadTeam", Policy: "banned", Reason: "encodes ratés"}, "Groupe refusé sur V3X · DreadTeam", "(encodes ratés)"},
	} {
		if got := c.h.label(); got != c.label {
			t.Errorf("pastille %q, attendu %q", got, c.label)
		}
		m := c.h.motive()
		if !strings.Contains(m, c.in) || strings.ContainsRune(m+c.h.label(), '—') {
			t.Errorf("raison %q", m)
		}
	}
}

// Les voisines taguées du même dossier comptent comme copies ; seules celles
// dont le nom porte une règle sont mesurées.
func TestSiblingCopies(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string, size int64) {
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(size); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	mk("Film (1992) - 1080p x264 ac3 mHDgz.mkv", 150<<20)
	mk("Film (1992).mkv", 150<<20)
	mk("Autre.2020.mkv", 150<<20)
	mk("Film (1993) - 1080p x264 ac3 mHDgz.mkv", 151<<20) // taguée, autre taille
	got := siblingCopies(testPolicies(), dir, nil, 150<<20, 1)
	if len(got) != 1 || got[0].name != "Film (1992) - 1080p x264 ac3 mHDgz.mkv" || got[0].size != 150<<20 || got[0].files != 1 {
		t.Fatalf("voisines : %+v", got)
	}
	if got := siblingCopies(testPolicies(), dir, nil, 150<<20, 2); len(got) != 0 {
		t.Errorf("voisines pour une release de 2 fichiers : %+v", got)
	}
	if h, ok := findPolicy(testPolicies(), []string{"Film (1992).mkv"}, 150<<20, 1, got); !ok || h.Detag == "" {
		t.Errorf("detag voisin non vu : %+v", h)
	}
}
