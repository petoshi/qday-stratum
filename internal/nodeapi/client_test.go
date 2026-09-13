package nodeapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthenticatedMiningCalls(t *testing.T) {
	const token = "qday-test-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/miner/getblocktemplate":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["longpollid"] != "old" {
				t.Errorf("invalid template request: %v %#v", err, body)
			}
			json.NewEncoder(w).Encode(Template{LongPollID: "new", Height: 12})
		case "/api/miner/submitblock":
			var body struct {
				Params []string `json:"params"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Params) != 1 || body.Params[0] != "abcd" {
				t.Errorf("invalid submit request: %v %#v", err, body)
			}
			json.NewEncoder(w).Encode(map[string]string{"block": "accepted-block"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := New(server.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	template, err := client.GetBlockTemplate(context.Background(), "old")
	if err != nil || template.LongPollID != "new" || template.Height != 12 {
		t.Fatalf("template = %+v, %v", template, err)
	}
	blockID, err := client.SubmitBlock(context.Background(), "abcd")
	if err != nil || blockID != "accepted-block" {
		t.Fatalf("submit = %q, %v", blockID, err)
	}
}

func TestNodeErrorMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "wallet is locked"})
	}))
	defer server.Close()
	client, err := New(server.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetBlockTemplate(context.Background(), ""); err == nil || err.Error() != "wallet is locked" {
		t.Fatalf("unexpected error: %v", err)
	}
}
