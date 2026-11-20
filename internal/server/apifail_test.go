package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/V3x0rV3x/D3X/internal/v3x"
)

// Un refus de V3X (clé sans le droit « Upload ») ne doit jamais arriver à la
// page en 401 : elle le prendrait pour une session D3X expirée.
func TestAPIFailNeverSends401(t *testing.T) {
	for _, tc := range []struct {
		in       *v3x.APIError
		contains string
	}{
		{&v3x.APIError{Status: http.StatusUnauthorized, Code: "unauthorized"}, "droit « Upload »"},
		{&v3x.APIError{Status: http.StatusForbidden, Code: "forbidden"}, "droit « Upload »"},
		{&v3x.APIError{Status: http.StatusForbidden, Code: "not_uploader"}, "not_uploader"},
	} {
		w := httptest.NewRecorder()
		apiFail(w, tc.in)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%v : code %d, attendu 403", tc.in, w.Code)
		}
		var body map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(body["error"], tc.contains) {
			t.Fatalf("%v : message %q sans %q", tc.in, body["error"], tc.contains)
		}
	}
}

// Une vérification ratée (limite d'appels, site en panne derrière sa page
// d'erreur HTML) arrive à la page en phrase lisible, avec son code HTTP :
// « Déjà sur V3X » l'affiche au lieu de se taire (Hypsis, 19/11/26).
func TestAPIFailReadable(t *testing.T) {
	for _, tc := range []struct {
		in       *v3x.APIError
		contains string
	}{
		{&v3x.APIError{Status: http.StatusTooManyRequests, Code: "Too Many Requests"}, "limite les appels"},
		{&v3x.APIError{Status: http.StatusBadGateway, Code: "<!DOCTYPE html><html><head><title>502</title>"}, "ne répond pas pour l'instant (HTTP 502)"},
		{&v3x.APIError{Status: http.StatusServiceUnavailable, Code: "internal"}, "ne répond pas"},
		{&v3x.APIError{Status: http.StatusBadRequest, Code: "validation"}, "validation"},
	} {
		w := httptest.NewRecorder()
		apiFail(w, tc.in)
		if w.Code != tc.in.Status {
			t.Fatalf("%v : code %d, attendu %d", tc.in, w.Code, tc.in.Status)
		}
		var body map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(body["error"], tc.contains) || strings.Contains(body["error"], "<") {
			t.Fatalf("%v : message %q, attendu %q", tc.in, body["error"], tc.contains)
		}
	}
}
