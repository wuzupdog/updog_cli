package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
)

const defaultUpdogURL = "https://wuzupdog.com"
const maxResponseBytes = 64 << 20

type apiClient struct {
	baseURL    string
	apiKey     string
	version    string
	httpClient *http.Client
}

type apiError struct {
	StatusCode int
	Body       []byte
	RetryAfter string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("Updog API returned HTTP %d", e.StatusCode)
}

func (c apiClient) get(ctx context.Context, path string, query url.Values) ([]byte, error) {
	endpoint := c.baseURL + path
	if encoded := query.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	return c.do(req)
}

func (c apiClient) postJSON(ctx context.Context, path string, value any) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req)
}

func (c apiClient) do(req *http.Request) ([]byte, error) {
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}
	req.Header.Set("User-Agent", "updog/"+c.version)

	// API redirects are unexpected, and following a 307/308 could forward a
	// device code or API key to another origin. Use a shallow client copy so a
	// caller-supplied client keeps its transport and timeout without mutating it.
	httpClient := *c.httpClient
	httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request Updog: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Updog response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("Updog response exceeds %d MiB", maxResponseBytes>>20)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &apiError{
			StatusCode: resp.StatusCode,
			Body:       body,
			RetryAfter: resp.Header.Get("Retry-After"),
		}
	}
	return body, nil
}

func normalizeBaseURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = defaultUpdogURL
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("invalid Updog URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("Updog URL must begin with http:// or https://")
	}
	if parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("Updog URL must contain only a scheme, host, optional port, and path")
	}
	if parsed.Scheme == "http" && !loopbackHost(parsed.Hostname()) {
		return "", errors.New("Updog URL must use HTTPS unless the host is loopback")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validateAPIKey(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("API key is required")
	}
	if !strings.HasPrefix(value, "updog_") {
		return "", errors.New("API key must begin with updog_")
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return "", errors.New("API key contains invalid whitespace or control characters")
		}
	}
	return value, nil
}

func compactJSON(body []byte) []byte {
	var out bytes.Buffer
	if err := json.Compact(&out, body); err != nil {
		return bytes.TrimSpace(body)
	}
	return out.Bytes()
}

func defaultHTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}
