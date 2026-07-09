package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	deviceAuthorizationPath = "/api/v1/cli/device-authorizations"
	deviceTokenPath         = "/api/v1/cli/device-authorizations/token"
	deviceClientID          = "updog_cli"
	deviceGrantType         = "urn:ietf:params:oauth:grant-type:device_code"
	deviceReadScope         = "errors:read logs:read"
	maxDevicePollInterval   = 60 * time.Second
)

type deviceAuthorization struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type deviceToken struct {
	AccessToken string        `json:"access_token"`
	TokenType   string        `json:"token_type"`
	Scope       string        `json:"scope"`
	Project     deviceProject `json:"project"`
}

type deviceProject struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type deviceOAuthError struct {
	Error    string `json:"error"`
	Interval int    `json:"interval"`
}

func (a *app) deviceLogin(globals globalOptions, baseURL string) error {
	client := apiClient{baseURL: baseURL, version: a.version, httpClient: a.httpClient}
	body, err := client.postJSON(a.context, deviceAuthorizationPath, map[string]any{
		"client_id":   deviceClientID,
		"scope":       deviceReadScope,
		"device_name": a.deviceName,
	})
	if err != nil {
		return deviceStartError(err)
	}

	var authorization deviceAuthorization
	if err := json.Unmarshal(body, &authorization); err != nil {
		return deviceError("Updog returned an invalid device authorization response")
	}
	if err := validateDeviceAuthorization(baseURL, authorization); err != nil {
		return deviceError("Updog returned an invalid device authorization response")
	}

	fmt.Fprintln(a.err, "To authorize this CLI with read-only access:")
	fmt.Fprintf(a.err, "  Open: %s\n", authorization.VerificationURI)
	fmt.Fprintf(a.err, "  Enter code: %s\n", authorization.UserCode)
	fmt.Fprintln(a.err, "Waiting for approval...")

	token, err := a.pollForDeviceToken(client, authorization)
	if err != nil {
		return err
	}

	projectName := globals.project
	if projectName == "" {
		projectName = token.Project.Slug
		if validateProjectName(projectName) != nil {
			projectName = "project-" + strconv.FormatInt(token.Project.ID, 10)
		}
	}

	metadata := project{
		ProjectID:   token.Project.ID,
		ProjectName: token.Project.Name,
		ProjectSlug: token.Project.Slug,
	}
	return a.persistLogin(globals, projectName, baseURL, token.AccessToken, metadata)
}

func (a *app) pollForDeviceToken(client apiClient, authorization deviceAuthorization) (deviceToken, error) {
	deadline := a.now().Add(time.Duration(authorization.ExpiresIn) * time.Second)
	interval := time.Duration(authorization.Interval) * time.Second
	wait := interval

	for {
		now := a.now()
		if !now.Before(deadline) {
			return deviceToken{}, deviceError("device authorization expired; run updog login again")
		}
		if remaining := deadline.Sub(now); wait > remaining {
			wait = remaining
		}
		if err := a.sleep(a.context, wait); err != nil {
			return deviceToken{}, deviceError("device authorization canceled")
		}
		if !a.now().Before(deadline) {
			return deviceToken{}, deviceError("device authorization expired; run updog login again")
		}

		body, err := client.postJSON(a.context, deviceTokenPath, map[string]string{
			"client_id":   deviceClientID,
			"grant_type":  deviceGrantType,
			"device_code": authorization.DeviceCode,
		})
		if err == nil {
			var token deviceToken
			if json.Unmarshal(body, &token) != nil || validateDeviceToken(token) != nil {
				return deviceToken{}, deviceError("Updog returned an invalid token response")
			}
			return token, nil
		}

		var responseErr *apiError
		if !errors.As(err, &responseErr) {
			interval = boundedLocalPollInterval(interval * 2)
			wait = interval
			continue
		}

		oauthErr := parseDeviceOAuthError(responseErr.Body)
		switch oauthErr.Error {
		case "authorization_pending":
			wait = interval
			continue
		case "slow_down":
			interval += 5 * time.Second
			if serverInterval := time.Duration(oauthErr.Interval) * time.Second; serverInterval > interval {
				interval = serverInterval
			}
			wait = interval
			if retryAfter := parseRetryAfter(responseErr.RetryAfter, a.now()); retryAfter > wait {
				wait = retryAfter
			}
			continue
		case "access_denied":
			return deviceToken{}, deviceError("device authorization was denied")
		case "expired_token":
			return deviceToken{}, deviceError("device authorization expired; run updog login again")
		case "invalid_grant":
			return deviceToken{}, deviceError("device authorization is no longer valid; run updog login again")
		case "temporarily_unavailable":
			wait = interval
			continue
		}

		if responseErr.StatusCode == http.StatusTooManyRequests {
			wait = interval
			if retryAfter := parseRetryAfter(responseErr.RetryAfter, a.now()); retryAfter > wait {
				wait = retryAfter
			}
			continue
		}
		if responseErr.StatusCode >= 500 {
			wait = interval
			continue
		}
		return deviceToken{}, deviceError("device authorization failed")
	}
}

func validateDeviceAuthorization(baseURL string, authorization deviceAuthorization) error {
	if len(authorization.DeviceCode) < 20 || len(authorization.DeviceCode) > 2048 || unsafeCode(authorization.DeviceCode) {
		return errors.New("invalid device code")
	}
	if len(authorization.UserCode) < 4 || len(authorization.UserCode) > 32 || !displayableUserCode(authorization.UserCode) {
		return errors.New("invalid user code")
	}
	if authorization.ExpiresIn <= 0 || authorization.ExpiresIn > 3600 {
		return errors.New("invalid expiration")
	}
	if authorization.Interval <= 0 || authorization.Interval > int(maxDevicePollInterval/time.Second) {
		return errors.New("invalid polling interval")
	}
	if !sameOriginURL(baseURL, authorization.VerificationURI) {
		return errors.New("invalid verification URL")
	}
	if authorization.VerificationURIComplete != "" && !sameOriginURL(baseURL, authorization.VerificationURIComplete) {
		return errors.New("invalid complete verification URL")
	}
	return nil
}

func validateDeviceToken(token deviceToken) error {
	apiKey, err := validateAPIKey(token.AccessToken)
	if err != nil || apiKey != token.AccessToken {
		return errors.New("invalid access token")
	}
	if token.TokenType != "api_key" {
		return errors.New("invalid token type")
	}
	if !exactReadScope(token.Scope) {
		return errors.New("invalid scope")
	}
	if token.Project.ID <= 0 || unsafeProjectName(token.Project.Name) || validateProjectName(token.Project.Slug) != nil {
		return errors.New("invalid project")
	}
	return nil
}

func unsafeProjectName(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || len([]rune(value)) > 255 {
		return true
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func exactReadScope(value string) bool {
	fields := strings.Fields(value)
	if len(fields) != 2 {
		return false
	}
	found := map[string]bool{}
	for _, field := range fields {
		if field != "logs:read" && field != "errors:read" {
			return false
		}
		found[field] = true
	}
	return found["logs:read"] && found["errors:read"]
}

func unsafeCode(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return true
		}
	}
	return false
}

func displayableUserCode(value string) bool {
	for _, r := range value {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

func sameOriginURL(baseURL, candidate string) bool {
	base, baseErr := url.Parse(baseURL)
	parsed, candidateErr := url.Parse(candidate)
	if baseErr != nil || candidateErr != nil || !parsed.IsAbs() || parsed.User != nil || parsed.Fragment != "" {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	return strings.EqualFold(base.Scheme, parsed.Scheme) && strings.EqualFold(base.Host, parsed.Host)
}

func parseDeviceOAuthError(body []byte) deviceOAuthError {
	var result deviceOAuthError
	if json.Unmarshal(body, &result) != nil {
		return deviceOAuthError{}
	}
	return result
}

func deviceStartError(err error) error {
	var responseErr *apiError
	if !errors.As(err, &responseErr) {
		return deviceError("could not contact Updog to start device authorization")
	}

	oauthErr := parseDeviceOAuthError(responseErr.Body)
	switch oauthErr.Error {
	case "invalid_client":
		return deviceError("this CLI version is not accepted by the Updog server")
	case "invalid_scope":
		return deviceError("the Updog server refused the read-only CLI scopes")
	case "temporarily_unavailable":
		return deviceError("device authorization is temporarily unavailable")
	}
	if responseErr.StatusCode == http.StatusTooManyRequests {
		return &commandError{
			code:       1,
			message:    "device authorization is rate limited; try again later",
			retryAfter: responseErr.RetryAfter,
		}
	}
	return deviceError("could not start device authorization")
}

func deviceError(message string) error {
	return &commandError{code: 1, message: message}
}

func boundedLocalPollInterval(value time.Duration) time.Duration {
	if value < time.Second {
		return time.Second
	}
	if value > maxDevicePollInterval {
		return maxDevicePollInterval
	}
	return value
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil && date.After(now) {
		duration := date.Sub(now)
		if remainder := duration % time.Second; remainder != 0 {
			duration += time.Second - remainder
		}
		return duration
	}
	return 0
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func defaultDeviceName(version string) string {
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		hostname = "this computer"
	}
	hostname = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, hostname)
	value := fmt.Sprintf("%s (%s/%s; updog %s)", hostname, runtime.GOOS, runtime.GOARCH, version)
	runes := []rune(value)
	if len(runes) > 80 {
		value = string(runes[:80])
	}
	return value
}
