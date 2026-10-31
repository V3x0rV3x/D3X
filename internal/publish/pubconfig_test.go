package publish

import (
	"testing"

	"github.com/V3x0rV3x/D3X/internal/config"
)

func TestPubConfig(t *testing.T) {
	cfg := &config.Config{Category: "cross", Tag: "D3X"}
	if got := pubConfig(cfg); got.Category != "cross" || got.Tag != "D3X" {
		t.Fatalf("sans réglage de publication : %+v", got)
	}
	cfg.PubCategory = " publi "
	got := pubConfig(cfg)
	if got.Category != "publi" || got.Tag != "D3X" || cfg.Category != "cross" {
		t.Fatalf("catégorie de publication : %+v (origine %+v)", got, cfg)
	}
	cfg.PubTag = " mes-up "
	got = pubConfig(cfg)
	if got.Category != "publi" || got.Tag != "mes-up" || cfg.Tag != "D3X" {
		t.Fatalf("étiquette de publication : %+v (origine %+v)", got, cfg)
	}
	cfg.PubCategory = ""
	got = pubConfig(cfg)
	if got.Category != "cross" || got.Tag != "mes-up" {
		t.Fatalf("étiquette seule : %+v", got)
	}
	if pubConfig(nil) != nil {
		t.Fatal("réglages absents : nil attendu")
	}
}
