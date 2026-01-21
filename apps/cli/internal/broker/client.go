package broker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/org/cli/internal/ssm"
)

// Client is an HTTP client for the broker API
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewClient creates a new broker client
func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// SessionRequest is the request body for creating a session
type SessionRequest struct {
	Target    string `json:"target"`
	LocalPort int    `json:"localPort,omitempty"`
}

// GetSession requests a new SSM session from the broker
func (c *Client) GetSession(req *SessionRequest) (*ssm.SessionCredentials, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", c.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("broker returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var creds ssm.SessionCredentials
	if err := json.Unmarshal(respBody, &creds); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &creds, nil
}
