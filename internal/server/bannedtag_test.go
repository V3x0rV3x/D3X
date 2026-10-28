package server

import (
	"testing"

	"github.com/V3x0rV3x/D3X/internal/crossseed"
	"github.com/V3x0rV3x/D3X/internal/v3x"
)

func TestBannedTagOf(t *testing.T) {
	list := []v3x.TeamPolicy{
		{Name: "Dread-Team", Policy: "banned"},
		{Name: "EXTREME", Policy: "banned"},
		{Name: "TOXIC", Policy: "banned"},
	}
	for _, tc := range []struct {
		name, want string
	}{
		{"Helter.Skelter.2012.VOSTFR.1080p.BluRay.x264-DreadTeam", "Dread-Team"},
		{"Film.2020.1080p-Dread-Team.mkv", "Dread-Team"},
		{"The.Humanity.Bureau.2017.MULTi.1080p.BluRay.x264.AC3-EXTREME", "EXTREME"},
		{"Jackass.Extreme", ""},
		{"Film.2020.1080p.WEB-NOTOXIC", ""},
		{"Film [TOXIC]", "TOXIC"},
		{"Film - TOXIC (Titre original)", "TOXIC"},
	} {
		got := ""
		if b := bannedTagOf(tc.name, list); b != nil {
			got = b.Name
		}
		if got != tc.want {
			t.Errorf("%s : %q, attendu %q", tc.name, got, tc.want)
		}
	}
}

func TestDetagHits(t *testing.T) {
	byTag := map[string]policyHit{"mhdgz": {Team: "mHDgz", Policy: "forbidden"}}
	const size = 4 << 30
	rows := []crossseed.Row{
		// Ekhstaz, 27/10/26 : la copie taguée (tag séparé par un espace) est chez lui.
		{Key: "a", Name: "Le Temps D'un Week-end (1992) - 1080p FR EN x264 ac3 mHDgz.mkv", Size: size, Files: 1, Status: crossseed.StInClient},
		{Key: "b", Name: "Le Temps D'un Week-end (1992).mkv", Size: size, Files: 1, Status: crossseed.StNone},
		// Même taille, autre nombre de fichiers : une autre release.
		{Key: "c", Name: "Autre (2001)", Size: size, Files: 3, Status: crossseed.StNone},
		// Trop petit pour conclure sur la taille seule.
		{Key: "d", Name: "Petit mHDgz.mkv", Size: 10 << 20, Files: 1, Status: crossseed.StInClient},
		{Key: "e", Name: "Petit.mkv", Size: 10 << 20, Files: 1, Status: crossseed.StNone},
	}
	hits := map[string]policyHit{}
	for _, r := range rows {
		if r.Status != crossseed.StNone {
			continue
		}
		if h, ok := policyOf(r.Name, nil, byTag); ok {
			hits[r.Key] = h
		}
	}
	detagHits(rows, hits, nil, byTag)
	if h, ok := hits["b"]; !ok || h.Team != "mHDgz" || h.Policy != "forbidden" || h.Detag != rows[0].Name {
		t.Errorf("detag : %+v", hits["b"])
	}
	for _, k := range []string{"c", "e"} {
		if h, ok := hits[k]; ok {
			t.Errorf("%s ne devrait pas être visée : %+v", k, h)
		}
	}
	if h, ok := policyOf(rows[0].Name, nil, byTag); !ok || h.Team != "mHDgz" {
		t.Errorf("tag séparé par un espace non reconnu : %+v", h)
	}
}
