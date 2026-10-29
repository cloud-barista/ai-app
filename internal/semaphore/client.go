// Package semaphore is the outbound adapter for the Semaphore UI REST API
// (v2.12), which runs the deployment playbooks against a node over SSH.
//
// Field names follow the live v2.12.17 API, checked against a running
// container: a static inventory carries its text in "inventory" and its SSH key
// in "ssh_key_id", an ssh key carries its private key in "ssh.private_key", and
// a task carries its extra variables as a JSON string in "environment".
package semaphore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/redact"
)

const (
	// requestTimeout bounds one API call. A task runs for minutes, but no
	// single call to Semaphore has a reason to.
	requestTimeout    = 30 * time.Second
	maxErrorBodyBytes = 4 << 10
)

// Client calls the Semaphore REST API.
//
// It authenticates with an API token when one is configured, and otherwise
// logs in with the user name and password and keeps the session cookie.
type Client struct {
	baseURL      string
	token        string
	username     string
	password     string
	project      string
	playbookDir  string
	pollInterval time.Duration
	timeout      time.Duration
	httpClient   *http.Client

	loginMu  sync.Mutex
	loggedIn bool
}

// NewClient builds a Client from configuration.
func NewClient(cfg config.SemaphoreConfig) *Client {
	// cookiejar.New never fails without options.
	jar, _ := cookiejar.New(nil)
	return &Client{
		baseURL:      strings.TrimRight(cfg.URL, "/"),
		token:        cfg.Token,
		username:     cfg.Username,
		password:     cfg.Password,
		project:      cfg.Project,
		playbookDir:  cfg.PlaybookDir,
		pollInterval: cfg.PollInterval,
		timeout:      cfg.Timeout,
		httpClient:   &http.Client{Timeout: requestTimeout, Jar: jar},
	}
}

var (
	// ErrNotConfigured is returned by every call when no Semaphore URL is set.
	ErrNotConfigured = errors.New("semaphore is not configured: set AIAPP_SEMAPHORE_URL")
	// ErrNoCredentials is returned when neither a token nor a user name and
	// password are set.
	ErrNoCredentials = errors.New("semaphore credentials are not set: set AIAPP_SEMAPHORE_TOKEN, " +
		"or AIAPP_SEMAPHORE_USERNAME and AIAPP_SEMAPHORE_PASSWORD")
)

// APIError is a non-2xx response from Semaphore.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("semaphore responded %d: %s", e.StatusCode, e.Message)
}

// StatusCode reports the upstream HTTP status for err, or 0 when err is not an APIError.
func StatusCode(err error) int {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode
	}
	return 0
}

// Login opens a session with the configured user name and password. It does
// nothing when an API token is configured.
func (c *Client) Login(ctx context.Context) error {
	if c.token != "" {
		return nil
	}
	c.loginMu.Lock()
	defer c.loginMu.Unlock()
	return c.loginLocked(ctx)
}

func (c *Client) loginLocked(ctx context.Context) error {
	if c.username == "" || c.password == "" {
		return ErrNoCredentials
	}
	body := map[string]string{"auth": c.username, "password": c.password}
	if err := c.send(ctx, http.MethodPost, "/api/auth/login", body, nil); err != nil {
		return fmt.Errorf("failed to log in to semaphore as %q: %w", c.username, err)
	}
	c.loggedIn = true
	return nil
}

// ensureSession logs in once when no token is configured.
func (c *Client) ensureSession(ctx context.Context) error {
	if c.token != "" {
		return nil
	}
	c.loginMu.Lock()
	defer c.loginMu.Unlock()
	if c.loggedIn {
		return nil
	}
	return c.loginLocked(ctx)
}

// relogin replaces an expired session. It is a no-op with a token, where a
// 401 means the token itself is wrong.
func (c *Client) relogin(ctx context.Context) error {
	c.loginMu.Lock()
	defer c.loginMu.Unlock()
	c.loggedIn = false
	return c.loginLocked(ctx)
}

// Do sends an authenticated request to Semaphore and decodes a JSON response
// into out. A nil body skips the request payload; a nil out discards the
// response payload. An expired session is renewed once.
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	if err := c.ensureSession(ctx); err != nil {
		return err
	}
	err := c.send(ctx, method, path, body, out)
	if c.token == "" && StatusCode(err) == http.StatusUnauthorized {
		if lerr := c.relogin(ctx); lerr != nil {
			return lerr
		}
		err = c.send(ctx, method, path, body, out)
	}
	return err
}

func (c *Client) send(ctx context.Context, method, path string, body, out any) error {
	if c.baseURL == "" {
		return ErrNotConfigured
	}
	endpoint := c.baseURL + "/" + strings.TrimLeft(path, "/")

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
		return fmt.Errorf("failed to build request: %w", errors.New(redact.Error(err, c.baseURL)))
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	// Path and method only: bodies carry private keys.
	log.Debug().Str("method", method).Str("path", path).Msg("Calling Semaphore")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to call semaphore %s %s: %s", method, path, redact.Error(err, c.baseURL))
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
		return fmt.Errorf("failed to decode semaphore response for %s %s: %w", method, path, err)
	}
	return nil
}

// readErrorMessage extracts the Semaphore {"error": ...} message, falling back to raw text.
func readErrorMessage(body io.Reader) string {
	raw, err := io.ReadAll(io.LimitReader(body, maxErrorBodyBytes))
	if err != nil {
		return "no response body"
	}
	var simple struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &simple) == nil && simple.Error != "" {
		return simple.Error
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return "empty response body"
	}
	return trimmed
}
