package server

import (
	"net/http"
	"testing"

	"github.com/V3x0rV3x/D3X/internal/batch"
)

func TestSeriesMemory(t *testing.T) {
	s := testServer(t, Options{})
	// Import depuis la page (clés calculées par la page), puis lecture.
	code, _, body := call(t, s, "POST", "/ui/series", map[string]interface{}{"entries": map[string]interface{}{"power": map[string]interface{}{"type": "tv", "id": 123, "title": "Power", "year": 2014, "posterUrl": nil}}})
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	code, out, _ := call(t, s, "GET", "/ui/series", nil)
	if code != http.StatusOK || out["power"] == nil {
		t.Fatalf("%d %v", code, out)
	}
	// Le lot retrouve la série par son titre, même accentué ou ponctué.
	if w := rememberedSeries("Power!"); w == nil || w.ID != "123" || w.Year != "2014" {
		t.Fatalf("%+v", w)
	}
	if rememberedSeries("Autre") != nil {
		t.Fatal("série inconnue")
	}
	// Un film n'est jamais retenu.
	rememberSeries("Film", batch.Work{Type: "movie", ID: "1"})
	if rememberedSeries("Film") != nil {
		t.Fatal("film retenu")
	}
	// 300 au plus : la plus ancienne part.
	for i := 0; i < 301; i++ {
		rememberSeries("Serie"+string(rune('A'+i%26))+string(rune('a'+i/26)), batch.Work{Type: "tv", ID: batch.Text(string(rune('0' + i%10)))})
	}
	if rememberedSeries("Power") != nil {
		t.Fatal("la plus ancienne devait partir")
	}
}
