package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/petoshi/qday-stratum/internal/nodeapi"
)

const standaloneNodeURL = "http://127.0.0.1:19770"

type localNodeEndpoint struct {
	Format int    `json:"format"`
	URL    string `json:"url"`
}

func validateLocalNodeURL(value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("invalid local QDAY node endpoint")
	}
	host, port, err := net.SplitHostPort(u.Host)
	n, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || host != "127.0.0.1" || n < 1 || n > 65535 {
		return errors.New("QDAY node endpoint must use literal loopback")
	}
	return nil
}

func resolveNodeURL(explicit, tokenFile string) (string, error) {
	if explicit = strings.TrimSpace(explicit); explicit != "" {
		return explicit, nil
	}
	path := filepath.Join(filepath.Dir(tokenFile), "node.json")
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return standaloneNodeURL, nil
	} else if err != nil {
		return "", fmt.Errorf("read QDAY node endpoint: %w", err)
	} else if len(b) > 4096 {
		return "", errors.New("invalid QDAY node endpoint file size")
	}
	var endpoint localNodeEndpoint
	if err := json.Unmarshal(b, &endpoint); err != nil || endpoint.Format != 1 {
		return "", errors.New("invalid QDAY node endpoint file")
	} else if err := validateLocalNodeURL(endpoint.URL); err != nil {
		return "", err
	}
	return endpoint.URL, nil
}

type discoveredNodeClient struct {
	explicit  string
	tokenFile string
	token     string

	mu     sync.Mutex
	url    string
	client *nodeapi.Client
}

func newDiscoveredNodeClient(explicit, tokenFile, token string) (*discoveredNodeClient, error) {
	c := &discoveredNodeClient{explicit: explicit, tokenFile: tokenFile, token: token}
	_, err := c.current()
	return c, err
}

func (c *discoveredNodeClient) current() (*nodeapi.Client, error) {
	url, err := resolveNodeURL(c.explicit, c.tokenFile)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil && c.url == url {
		return c.client, nil
	}
	client, err := nodeapi.New(url, c.token)
	if err != nil {
		return nil, err
	}
	c.url, c.client = url, client
	return c.client, nil
}

func (c *discoveredNodeClient) GetBlockTemplate(ctx context.Context, longPollID string) (nodeapi.Template, error) {
	client, err := c.current()
	if err != nil {
		return nodeapi.Template{}, err
	}
	return client.GetBlockTemplate(ctx, longPollID)
}

func (c *discoveredNodeClient) SubmitBlock(ctx context.Context, block string) (string, error) {
	client, err := c.current()
	if err != nil {
		return "", err
	}
	return client.SubmitBlock(ctx, block)
}
