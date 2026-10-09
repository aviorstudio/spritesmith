package client

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestProcessRetrievesDurableGenerationWithoutResubmission(t *testing.T) {
	posts := 0
	id := strings.Repeat("a", 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/prompt":
			posts++
			if r.Header.Get("Idempotency-Key") == "" {
				t.Error("missing logical operation key")
			}
			w.WriteHeader(202)
			json.NewEncoder(w).Encode(map[string]string{"id": id})
		case "/v1/generations/" + id:
			json.NewEncoder(w).Encode(map[string]string{"status": "succeeded"})
		case "/v1/generations/" + id + "/result":
			w.Header().Set("Content-Type", "image/png")
			io.WriteString(w, "synthetic-png")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	_, err := (Client{BaseURL: server.URL}).Process(Request{Mode: ModePrompt, Prompt: "fixture", OutputPath: filepath.Join(t.TempDir(), "result.png")})
	if err != nil || posts != 1 {
		t.Fatalf("job flow: %v posts=%d", err, posts)
	}
}

func TestVectorPromptDownloadsSVGWithoutResubmission(t *testing.T) {
	posts := 0
	id := strings.Repeat("b", 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("missing authentication")
		}
		switch r.URL.Path {
		case "/v1/prompt":
			posts++
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
			}
			if r.FormValue("kind") != "vector" || r.FormValue("prompt") != "a triangle" {
				t.Error("vector prompt missing")
			}
			w.WriteHeader(202)
			json.NewEncoder(w).Encode(map[string]string{"id": id})
		case "/v1/generations/" + id:
			json.NewEncoder(w).Encode(map[string]string{"status": "succeeded"})
		case "/v1/generations/" + id + "/result":
			if r.URL.Query().Get("format") != "svg" {
				t.Error("wrong artifact requested")
			}
			w.Header().Set("Content-Type", "image/svg+xml")
			io.WriteString(w, `<svg xmlns="http://www.w3.org/2000/svg"/>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	_, err := (Client{BaseURL: server.URL, Token: "fixture-token"}).Process(Request{Mode: ModePrompt, Kind: "vector", Format: "svg", Prompt: "a triangle", OutputPath: filepath.Join(t.TempDir(), "sprite.svg")})
	if err != nil || posts != 1 {
		t.Fatalf("vector flow: %v posts=%d", err, posts)
	}
}
