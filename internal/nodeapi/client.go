// Package nodeapi talks to the authenticated local QDAY mining API.
package nodeapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxResponseSize = 64 << 20

// TemplateTransaction is one serialized transaction in a QDAY block template.
type TemplateTransaction struct {
	Data   string `json:"data"`
	TxID   string `json:"txid"`
	TxType string `json:"txtype"`
}

// StratumTemplate contains the extra data exposed for Sia Stratum bridges.
type StratumTemplate struct {
	Block        string   `json:"block"`
	MerkleBranch []string `json:"merklebranch"`
}

// Template is the transaction-aware block candidate returned by QDAY.
type Template struct {
	Header            string                `json:"header"`
	Commitment        string                `json:"commitment"`
	Transactions      []TemplateTransaction `json:"transactions"`
	PreviousBlockHash string                `json:"previousblockhash"`
	LongPollID        string                `json:"longpollid"`
	Target            string                `json:"target"`
	Height            uint32                `json:"height"`
	Timestamp         int64                 `json:"curtime"`
	Bits              string                `json:"bits"`
	Stratum           StratumTemplate       `json:"stratum"`
}

// Client is an authenticated client for one local QDAY node.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New constructs a QDAY node client.
func New(baseURL, token string) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("node URL must look like http://127.0.0.1:19770")
	}
	if token = strings.TrimSpace(token); token == "" {
		return nil, errors.New("QDAY API token is empty")
	}
	return &Client{
		baseURL: baseURL,
		token:   token,
		http: &http.Client{
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				MaxIdleConns:          4,
				MaxIdleConnsPerHost:   4,
				IdleConnTimeout:       90 * time.Second,
				ResponseHeaderTimeout: 45 * time.Second,
			},
		},
	}, nil
}

func (c *Client) post(ctx context.Context, path string, body any, result any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return err
	} else if len(payload) > maxResponseSize {
		return errors.New("QDAY node response exceeds 64 MiB")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(payload, &failure) == nil && failure.Error != "" {
			return errors.New(failure.Error)
		}
		return fmt.Errorf("QDAY node returned HTTP %d", resp.StatusCode)
	}
	if result == nil {
		return nil
	} else if err := json.Unmarshal(payload, result); err != nil {
		return fmt.Errorf("decode QDAY node response: %w", err)
	}
	return nil
}

// GetBlockTemplate returns immediately for an empty long-poll ID and waits for
// a changed tip, mempool or template timestamp for a current ID.
func (c *Client) GetBlockTemplate(ctx context.Context, longPollID string) (Template, error) {
	var result Template
	err := c.post(ctx, "/api/miner/getblocktemplate", struct {
		LongPollID string `json:"longpollid"`
	}{longPollID}, &result)
	return result, err
}

// SubmitBlock gives a complete Sia-encoded QDAY v2 block to the local node.
func (c *Client) SubmitBlock(ctx context.Context, block string) (string, error) {
	var result struct {
		Block string `json:"block"`
	}
	err := c.post(ctx, "/api/miner/submitblock", struct {
		Params []string `json:"params"`
	}{[]string{block}}, &result)
	return result.Block, err
}
