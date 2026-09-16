package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/petoshi/qday-stratum/internal/nodeapi"
)

func TestResolveNodeURL(t *testing.T) {
	dir := t.TempDir()
	token := filepath.Join(dir, "api.token")

	if got, err := resolveNodeURL("", token); err != nil || got != standaloneNodeURL {
		t.Fatalf("standalone fallback: got %q, %v", got, err)
	}
	if got, err := resolveNodeURL("http://127.0.0.1:12345", token); err != nil || got != "http://127.0.0.1:12345" {
		t.Fatalf("explicit endpoint: got %q, %v", got, err)
	}

	valid := []byte(`{"format":1,"url":"http://127.0.0.1:43821","pid":123,"genesis":"ignored"}`)
	if err := os.WriteFile(filepath.Join(dir, "node.json"), valid, 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveNodeURL("", token); err != nil || got != "http://127.0.0.1:43821" {
		t.Fatalf("desktop discovery: got %q, %v", got, err)
	}
}

func TestResolveNodeURLRejectsUnsafeDiscovery(t *testing.T) {
	dir := t.TempDir()
	token := filepath.Join(dir, "api.token")
	for _, endpoint := range []string{
		`{"format":1,"url":"https://127.0.0.1:43821"}`,
		`{"format":1,"url":"http://localhost:43821"}`,
		`{"format":1,"url":"http://192.0.2.1:43821"}`,
		`{"format":1,"url":"http://127.0.0.1:43821/path"}`,
		`{"format":2,"url":"http://127.0.0.1:43821"}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, "node.json"), []byte(endpoint), 0600); err != nil {
			t.Fatal(err)
		}
		if got, err := resolveNodeURL("", token); err == nil {
			t.Fatalf("accepted unsafe endpoint %s as %q", endpoint, got)
		}
	}
}

func TestDiscoveredNodeClientFollowsWalletRestart(t *testing.T) {
	const tokenValue = "test-token"
	server := func(height uint32) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+tokenValue {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			json.NewEncoder(w).Encode(nodeapi.Template{Height: height})
		}))
	}
	a, b := server(1), server(2)
	defer a.Close()
	defer b.Close()

	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "api.token")
	writeEndpoint := func(url string) {
		t.Helper()
		body, err := json.Marshal(localNodeEndpoint{Format: 1, URL: url})
		if err != nil {
			t.Fatal(err)
		} else if err := os.WriteFile(filepath.Join(dir, "node.json"), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeEndpoint(a.URL)
	client, err := newDiscoveredNodeClient("", tokenFile, tokenValue)
	if err != nil {
		t.Fatal(err)
	}
	if template, err := client.GetBlockTemplate(context.Background(), ""); err != nil || template.Height != 1 {
		t.Fatalf("first wallet: height %d, %v", template.Height, err)
	}
	writeEndpoint(b.URL)
	if template, err := client.GetBlockTemplate(context.Background(), ""); err != nil || template.Height != 2 {
		t.Fatalf("restarted wallet: height %d, %v", template.Height, err)
	}
}
