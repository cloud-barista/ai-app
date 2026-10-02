// Package llm is the outbound adapter for the Anthropic Messages API.
//
// It speaks the API over net/http rather than through the vendor SDK: the agent
// only needs the tool_use round trip, and the project standard prefers the
// standard library over a new dependency when the needed surface is this narrow.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/config"
)

const (
	messagesPath      = "/v1/messages"
	anthropicVersion  = "2023-06-01"
	maxErrorBodyBytes = 4 << 10
)

// ErrDisabled is returned when no API key is configured.
//
// The rest of the service works without one; only the natural language intent
// endpoint needs the model.
var ErrDisabled = errors.New("planning model is not configured")

// Client calls the Anthropic Messages API.
type Client struct {
	baseURL    string
	apiKey     string
	model      string
	maxTokens  int
	httpClient *http.Client
}

// NewClient builds a Client from configuration.
func NewClient(cfg config.LLMConfig) *Client {
	return &Client{
		baseURL:    cfg.BaseURL,
		apiKey:     cfg.APIKey,
		model:      cfg.Model,
		maxTokens:  cfg.MaxTokens,
		httpClient: &http.Client{Timeout: cfg.Timeout},
	}
}

// Enabled reports whether the client can reach the model.
func (c *Client) Enabled() bool { return c.apiKey != "" }

// Model reports the configured model identifier.
func (c *Client) Model() string { return c.model }

// APIError is a non-2xx response from the Messages API.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("messages api responded %d: %s", e.StatusCode, e.Message)
}

// CreateMessage sends one Messages API turn and returns the model response.
func (c *Client) CreateMessage(ctx context.Context, system string, messages []Message, tools []ToolDef) (*Response, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}

	body := Request{
		Model:     c.model,
		MaxTokens: c.maxTokens,
		System:    system,
		Messages:  messages,
		Tools:     tools,
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to encode messages request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+messagesPath, bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("failed to build messages request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("x-api-key", c.apiKey)

	// Model and turn count only: system prompts and messages carry operational detail.
	log.Debug().Str("model", c.model).Int("messages", len(messages)).Int("tools", len(tools)).
		Msg("Calling planning model")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call messages api: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &APIError{StatusCode: resp.StatusCode, Message: readErrorMessage(resp.Body)}
	}

	result := &Response{}
	if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
		return nil, fmt.Errorf("failed to decode messages response: %w", err)
	}
	return result, nil
}

// readErrorMessage extracts the API error message, falling back to raw text.
func readErrorMessage(body io.Reader) string {
	raw, err := io.ReadAll(io.LimitReader(body, maxErrorBodyBytes))
	if err != nil {
		return "no response body"
	}
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &payload) == nil && payload.Error.Message != "" {
		return payload.Error.Message
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return "empty response body"
	}
	return trimmed
}
