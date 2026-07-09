package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDeviceLoginPollsAndStoresProjectCredential(t *testing.T) {
	const (
		deviceCode = "device_code_that_must_never_be_printed_123"
		userCode   = "BCDFG-HJKLM"
		accessKey  = "updog_device_read_key_123"
	)

	var polls atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.Header.Get("X-API-Key") != "" {
			t.Errorf("device request sent an API key header")
		}
		if request.Header.Get("User-Agent") != "updog/0.3.0-test" {
			t.Errorf("User-Agent = %q", request.Header.Get("User-Agent"))
		}

		switch request.URL.Path {
		case deviceAuthorizationPath:
			var params map[string]string
			if err := json.NewDecoder(request.Body).Decode(&params); err != nil {
				t.Errorf("decode start request: %v", err)
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			if params["client_id"] != deviceClientID || params["scope"] != deviceReadScope || params["device_name"] != "test workstation" {
				t.Errorf("start params = %#v", params)
			}
			writeDeviceStart(t, writer, server.URL, deviceCode, userCode, 5)
		case deviceTokenPath:
			var params map[string]string
			if err := json.NewDecoder(request.Body).Decode(&params); err != nil {
				t.Errorf("decode token request: %v", err)
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			if params["client_id"] != deviceClientID || params["grant_type"] != deviceGrantType || params["device_code"] != deviceCode {
				t.Errorf("token params = %#v", params)
			}
			if polls.Add(1) == 1 {
				writer.WriteHeader(http.StatusBadRequest)
				io.WriteString(writer, `{"error":"authorization_pending"}`)
				return
			}
			writeDeviceToken(writer, accessKey, 42, "M&M Production", "mnm")
		default:
			t.Errorf("unexpected path %q", request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	var sleeps []time.Duration
	configPath := filepath.Join(t.TempDir(), "updog", "config.json")
	secrets := newMemorySecrets()
	result := runTestCLIWith(t, configPath, secrets, nil, "", false, func(options *Options) {
		options.DeviceName = "test workstation"
		options.Sleep = func(_ context.Context, duration time.Duration) error {
			sleeps = append(sleeps, duration)
			return nil
		}
	}, "login", "--url", server.URL)

	if result.status != 0 {
		t.Fatalf("login status = %d, stderr = %s", result.status, result.stderr)
	}
	if fmt.Sprint(sleeps) != "[5s 5s]" {
		t.Fatalf("poll sleeps = %v", sleeps)
	}
	if !strings.Contains(result.stderr, server.URL+"/cli/authorize") || !strings.Contains(result.stderr, userCode) {
		t.Fatalf("instructions = %q", result.stderr)
	}
	assertNotInOutput(t, result, deviceCode, accessKey)

	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := cfg.Projects["mnm"]
	if !ok || cfg.CurrentProject != "mnm" {
		t.Fatalf("profile was not selected: %#v", cfg)
	}
	if entry.URL != server.URL || entry.ProjectID != 42 || entry.ProjectName != "M&M Production" || entry.ProjectSlug != "mnm" {
		t.Fatalf("stored metadata = %#v", entry)
	}
	stored, err := secrets.Get(entry.CredentialID)
	if err != nil || stored != accessKey {
		t.Fatalf("stored credential = %q, %v", stored, err)
	}
	configData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(configData), accessKey) || strings.Contains(string(configData), deviceCode) {
		t.Fatal("configuration contains a device secret")
	}

	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(result.stdout), &envelope); err != nil {
		t.Fatalf("stdout is not JSON: %v", err)
	}
	if envelope.Data["project"] != "mnm" || envelope.Data["project_slug"] != "mnm" {
		t.Fatalf("login result = %#v", envelope.Data)
	}
}

func TestDeviceLoginHonorsSlowDownAndRetryAfter(t *testing.T) {
	const deviceCode = "device_code_for_poll_timing_123456789"
	var polls atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Path == deviceAuthorizationPath {
			writeDeviceStart(t, writer, server.URL, deviceCode, "NPQRS-TVWXZ", 5)
			return
		}

		switch polls.Add(1) {
		case 1:
			writer.Header().Set("Retry-After", "8")
			writer.WriteHeader(http.StatusBadRequest)
			io.WriteString(writer, `{"error":"slow_down","interval":10}`)
		case 2:
			writer.Header().Set("Retry-After", "75")
			writer.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(writer, `{"error":"rate_limited"}`)
		default:
			writeDeviceToken(writer, "updog_slow_down_key", 7, "Worker", "worker")
		}
	}))
	defer server.Close()

	var sleeps []time.Duration
	result := runTestCLIWith(t, filepath.Join(t.TempDir(), "config.json"), newMemorySecrets(), nil, "", false, func(options *Options) {
		options.Sleep = func(_ context.Context, duration time.Duration) error {
			sleeps = append(sleeps, duration)
			return nil
		}
	}, "login", "--url", server.URL)
	if result.status != 0 {
		t.Fatalf("status = %d, stderr = %s", result.status, result.stderr)
	}
	if fmt.Sprint(sleeps) != "[5s 10s 1m15s]" {
		t.Fatalf("poll sleeps = %v", sleeps)
	}
}

func TestDeviceLoginStopsOnTerminalOAuthErrors(t *testing.T) {
	tests := []struct {
		name    string
		code    string
		message string
	}{
		{name: "denied", code: "access_denied", message: "was denied"},
		{name: "expired", code: "expired_token", message: "expired"},
		{name: "invalid", code: "invalid_grant", message: "no longer valid"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const deviceCode = "device_code_terminal_error_123456789"
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				if request.URL.Path == deviceAuthorizationPath {
					writeDeviceStart(t, writer, server.URL, deviceCode, "CDFGH-JKLMN", 5)
					return
				}
				writer.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(writer, `{"error":%q}`, test.code)
			}))
			defer server.Close()

			result := runTestCLIWith(t, filepath.Join(t.TempDir(), "config.json"), newMemorySecrets(), nil, "", false, immediateSleep, "login", "--url", server.URL)
			if result.status != 1 || result.stdout != "" || !strings.Contains(result.stderr, test.message) {
				t.Fatalf("result = %#v", result)
			}
			assertNotInOutput(t, result, deviceCode)
		})
	}
}

func TestDeviceLoginRejectsMalformedResponsesWithoutLeakingSecrets(t *testing.T) {
	t.Run("start response", func(t *testing.T) {
		const deviceCode = "device_code_in_bad_start_response_12345"
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			json.NewEncoder(writer).Encode(map[string]any{
				"device_code":               deviceCode,
				"user_code":                 "BCDFG-HJKLM",
				"verification_uri":          "https://attacker.example/authorize",
				"verification_uri_complete": "https://attacker.example/authorize?code=BCDFG-HJKLM",
				"expires_in":                600,
				"interval":                  5,
			})
		}))
		defer server.Close()

		result := runTestCLI(t, filepath.Join(t.TempDir(), "config.json"), newMemorySecrets(), nil, "", false, "login", "--url", server.URL)
		if result.status != 1 || !strings.Contains(result.stderr, "invalid device authorization response") {
			t.Fatalf("result = %#v", result)
		}
		assertNotInOutput(t, result, deviceCode)
	})

	t.Run("token response", func(t *testing.T) {
		const (
			deviceCode = "device_code_in_bad_token_response_12345"
			accessKey  = "updog_access_token_that_must_not_leak bad"
		)
		var server *httptest.Server
		server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			if request.URL.Path == deviceAuthorizationPath {
				writeDeviceStart(t, writer, server.URL, deviceCode, "FGHJK-LMNPQ", 5)
				return
			}
			writeDeviceToken(writer, accessKey, 8, "API", "api")
		}))
		defer server.Close()

		result := runTestCLIWith(t, filepath.Join(t.TempDir(), "config.json"), newMemorySecrets(), nil, "", false, immediateSleep, "login", "--url", server.URL)
		if result.status != 1 || !strings.Contains(result.stderr, "invalid token response") {
			t.Fatalf("result = %#v", result)
		}
		assertNotInOutput(t, result, deviceCode, accessKey)
	})
}

func TestDeviceLoginUsesExplicitLocalAlias(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Path == deviceAuthorizationPath {
			writeDeviceStart(t, writer, server.URL, "device_code_local_alias_1234567890", "HJKLM-NPQRS", 5)
			return
		}
		writeDeviceToken(writer, "updog_alias_key", 9, "MNM", "mnm")
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "config.json")
	result := runTestCLIWith(t, configPath, newMemorySecrets(), nil, "", false, immediateSleep,
		"login", "--project", "production", "--url", server.URL)
	if result.status != 0 {
		t.Fatalf("result = %#v", result)
	}
	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Projects["production"]; !ok || cfg.CurrentProject != "production" {
		t.Fatalf("config = %#v", cfg)
	}
}

func TestDeviceLoginCancellationStopsBeforePolling(t *testing.T) {
	const deviceCode = "device_code_canceled_before_poll_123456"
	var polls atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Path == deviceAuthorizationPath {
			writeDeviceStart(t, writer, server.URL, deviceCode, "JKLMN-PQRST", 5)
			return
		}
		polls.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	result := runTestCLIWith(t, filepath.Join(t.TempDir(), "config.json"), newMemorySecrets(), nil, "", false, func(options *Options) {
		options.Sleep = func(_ context.Context, _ time.Duration) error { return context.Canceled }
	}, "login", "--url", server.URL)
	if result.status != 1 || !strings.Contains(result.stderr, "canceled") || polls.Load() != 0 {
		t.Fatalf("result = %#v, polls = %d", result, polls.Load())
	}
	assertNotInOutput(t, result, deviceCode)
}

func TestDeviceLoginDoesNotForwardDeviceCodeAcrossRedirects(t *testing.T) {
	const deviceCode = "device_code_redirect_guard_1234567890"
	var redirectedRequests atomic.Int32
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		redirectedRequests.Add(1)
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer redirectTarget.Close()

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Path == deviceAuthorizationPath {
			writeDeviceStart(t, writer, server.URL, deviceCode, "KLMNP-QRSTV", 5)
			return
		}
		writer.Header().Set("Location", redirectTarget.URL+"/capture")
		writer.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	result := runTestCLIWith(t, filepath.Join(t.TempDir(), "config.json"), newMemorySecrets(), nil, "", false, immediateSleep,
		"login", "--url", server.URL)
	if result.status != 1 || redirectedRequests.Load() != 0 {
		t.Fatalf("result = %#v, redirected requests = %d", result, redirectedRequests.Load())
	}
	assertNotInOutput(t, result, deviceCode)
}

func TestManualLoginStillUsesSecurePrompt(t *testing.T) {
	const accessKey = "updog_existing_manual_key"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/logs" || request.Header.Get("X-API-Key") != accessKey {
			t.Errorf("manual validation request = %s, key = %q", request.URL.String(), request.Header.Get("X-API-Key"))
		}
		io.WriteString(writer, `{"data":[],"meta":{"pagination":{"total":0}}}`)
	}))
	defer server.Close()

	result := runTestCLI(t, filepath.Join(t.TempDir(), "config.json"), newMemorySecrets(), nil, accessKey, true,
		"login", "--manual", "--project", "mnm", "--url", server.URL)
	if result.status != 0 || !strings.Contains(result.stderr, "Read-only API key:") {
		t.Fatalf("result = %#v", result)
	}
	assertNotInOutput(t, result, accessKey)
}

func immediateSleep(options *Options) {
	options.Sleep = func(_ context.Context, _ time.Duration) error { return nil }
}

func writeDeviceStart(t *testing.T, writer http.ResponseWriter, baseURL, deviceCode, userCode string, interval int) {
	t.Helper()
	if err := json.NewEncoder(writer).Encode(map[string]any{
		"device_code":               deviceCode,
		"user_code":                 userCode,
		"verification_uri":          baseURL + "/cli/authorize",
		"verification_uri_complete": baseURL + "/cli/authorize?user_code=" + userCode,
		"expires_in":                600,
		"interval":                  interval,
	}); err != nil {
		t.Errorf("encode device response: %v", err)
	}
}

func writeDeviceToken(writer http.ResponseWriter, accessKey string, projectID int64, projectName, projectSlug string) {
	json.NewEncoder(writer).Encode(map[string]any{
		"access_token": accessKey,
		"token_type":   "api_key",
		"scope":        deviceReadScope,
		"project": map[string]any{
			"id":   projectID,
			"name": projectName,
			"slug": projectSlug,
		},
	})
}

func assertNotInOutput(t *testing.T, result cliResult, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if strings.Contains(result.stdout, secret) || strings.Contains(result.stderr, secret) {
			t.Fatalf("output exposed secret %q", secret)
		}
	}
}
