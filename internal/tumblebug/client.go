// Package tumblebug is the outbound adapter for the CB-Tumblebug REST API.
package tumblebug

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

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/config"
)

const (
	headerCredentialHolder = "x-credential-holder"
	maxErrorBodyBytes      = 4 << 10
)

// Client calls the CB-Tumblebug REST API.
//
// Every call takes a context so that cancellation and deadlines propagate from
// the inbound request.
type Client struct {
	baseURL          string
	username         string
	password         string
	credentialHolder string
	httpClient       *http.Client
}

// NewClient builds a Client from configuration.
func NewClient(cfg config.TumblebugConfig) *Client {
	return &Client{
		baseURL:          cfg.BaseURL,
		username:         cfg.Username,
		password:         cfg.Password,
		credentialHolder: cfg.CredentialHolder,
		httpClient:       &http.Client{Timeout: cfg.Timeout},
	}
}

// APIError is a non-2xx response from CB-Tumblebug.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("tumblebug responded %d: %s", e.StatusCode, e.Message)
}

// StatusCode reports the upstream HTTP status for err, or 0 when err is not an APIError.
func StatusCode(err error) int {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode
	}
	return 0
}

// Do sends a request to CB-Tumblebug and decodes a JSON response into out.
// A nil body skips the request payload; a nil out discards the response payload.
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	endpoint := c.baseURL + "/" + strings.TrimLeft(path, "/")
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}

	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to encode request body: %w", err)
		}
		payload = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, payload)
	if err != nil {
		return fmt.Errorf("failed to build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(c.username, c.password)
	if c.credentialHolder != "" {
		req.Header.Set(headerCredentialHolder, c.credentialHolder)
	}

	// Path and method only: query values and bodies may carry operational detail.
	log.Debug().Str("method", method).Str("path", path).Msg("Calling CB-Tumblebug")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to call tumblebug %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{StatusCode: resp.StatusCode, Message: readErrorMessage(resp.Body)}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("failed to decode tumblebug response for %s %s: %w", method, path, err)
	}
	return nil
}

// readErrorMessage extracts the Tumblebug SimpleMsg message, falling back to raw text.
func readErrorMessage(body io.Reader) string {
	raw, err := io.ReadAll(io.LimitReader(body, maxErrorBodyBytes))
	if err != nil {
		return "no response body"
	}
	var simple struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &simple) == nil && simple.Message != "" {
		return simple.Message
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return "empty response body"
	}
	return trimmed
}
