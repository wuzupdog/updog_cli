package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

type memorySecrets struct {
	mu     sync.Mutex
	values map[string]string
}

type hookSecrets struct {
	base        *memorySecrets
	afterSet    func()
	afterDelete func()
}

func (store *hookSecrets) Get(id string) (string, error) {
	return store.base.Get(id)
}

func (store *hookSecrets) Set(id, secret string) error {
	if err := store.base.Set(id, secret); err != nil {
		return err
	}
	if store.afterSet != nil {
		hook := store.afterSet
		store.afterSet = nil
		hook()
	}
	return nil
}

func (store *hookSecrets) Delete(id string) error {
	if err := store.base.Delete(id); err != nil {
		return err
	}
	if store.afterDelete != nil {
		hook := store.afterDelete
		store.afterDelete = nil
		hook()
	}
	return nil
}

func newMemorySecrets() *memorySecrets {
	return &memorySecrets{values: map[string]string{}}
}

func (store *memorySecrets) Get(id string) (string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, ok := store.values[id]
	if !ok {
		return "", errSecretNotFound
	}
	return value, nil
}

func (store *memorySecrets) Set(id, secret string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.values[id] = secret
	return nil
}

func (store *memorySecrets) Delete(id string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.values, id)
	return nil
}

type cliResult struct {
	status int
	stdout string
	stderr string
}

func runTestCLI(t *testing.T, configPath string, secrets secretStore, env map[string]string, input string, terminal bool, args ...string) cliResult {
	return runTestCLIWith(t, configPath, secrets, env, input, terminal, nil, args...)
}

func runTestCLIWith(
	t *testing.T,
	configPath string,
	secrets secretStore,
	env map[string]string,
	input string,
	terminal bool,
	configure func(*Options),
	args ...string,
) cliResult {
	t.Helper()
	var stdout, stderr bytes.Buffer
	options := Options{
		Version:          "0.3.0-test",
		Args:             args,
		In:               strings.NewReader(input),
		Out:              &stdout,
		Err:              &stderr,
		ConfigPath:       configPath,
		Secrets:          secrets,
		HTTPClient:       http.DefaultClient,
		InputIsTerminal:  func() bool { return terminal },
		OutputIsTerminal: func() bool { return terminal },
		ReadPassword: func() (string, error) {
			return strings.TrimSpace(input), nil
		},
		Getenv: func(name string) string { return env[name] },
		Getwd:  func() (string, error) { return "/work/mnm", nil },
	}
	if configure != nil {
		configure(&options)
	}
	status := Run(options)
	return cliResult{status: status, stdout: stdout.String(), stderr: stderr.String()}
}

func TestLoginStoresCredentialOutsideConfig(t *testing.T) {
	const apiKey = "updog_read_key_123"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/logs" || request.URL.Query().Get("limit") != "1" {
			t.Errorf("unexpected validation request: %s", request.URL.String())
		}
		if got := request.Header.Get("X-API-Key"); got != apiKey {
			t.Errorf("validation key = %q", got)
		}
		writer.Header().Set("Content-Type", "application/json")
		io.WriteString(writer, `{"data":[],"meta":{"pagination":{"total":0}}}`)
	}))
	defer server.Close()

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "updog", "config.json")
	secrets := newMemorySecrets()
	result := runTestCLI(t, configPath, secrets, nil, apiKey+"\n", false,
		"login", "--project", "mnm", "--url", server.URL+"/", "--token-stdin")

	if result.status != 0 {
		t.Fatalf("login status = %d, stderr = %s", result.status, result.stderr)
	}
	if strings.Contains(result.stdout, apiKey) || strings.Contains(result.stderr, apiKey) {
		t.Fatal("login output exposed API key")
	}
	configData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(configData, []byte(apiKey)) {
		t.Fatal("configuration file contains API key")
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %o", info.Mode().Perm())
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	entry := cfg.Projects["mnm"]
	if cfg.CurrentProject != "mnm" || entry.URL != server.URL {
		t.Fatalf("unexpected config: %#v", cfg)
	}
	stored, err := secrets.Get(entry.CredentialID)
	if err != nil || stored != apiKey {
		t.Fatalf("stored secret = %q, %v", stored, err)
	}

	var loginEnvelope map[string]any
	if err := json.Unmarshal([]byte(result.stdout), &loginEnvelope); err != nil {
		t.Fatalf("login output is not JSON: %v", err)
	}
}

func TestLogsUseSelectedProjectAndEncodeFilters(t *testing.T) {
	const apiKey = "updog_mnm_read_key"
	var received *http.Request
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		received = request.Clone(request.Context())
		writer.Header().Set("Content-Type", "application/json")
		io.WriteString(writer, `{"data":[],"meta":{"pagination":{"limit":25,"offset":50,"total":0,"has_more":false}}}`)
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "config.json")
	secrets := newMemorySecrets()
	cfg := configFile{Version: 1, CurrentProject: "mnm", Projects: map[string]project{
		"mnm": {URL: server.URL, CredentialID: "mnm-credential"},
	}}
	if err := saveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	if err := secrets.Set("mnm-credential", apiKey); err != nil {
		t.Fatal(err)
	}

	result := runTestCLI(t, configPath, secrets, nil, "", false,
		"--project", "mnm", "logs", "search",
		"--query", "space & slash/plus+", "--trace-id", "trace/123", "--limit", "25", "--offset=50")
	if result.status != 0 {
		t.Fatalf("status = %d, stderr = %s", result.status, result.stderr)
	}
	if received == nil {
		t.Fatal("server received no request")
	}
	if received.Header.Get("X-API-Key") != apiKey {
		t.Fatalf("wrong API key header: %q", received.Header.Get("X-API-Key"))
	}
	if received.URL.Query().Get("q") != "space & slash/plus+" || received.URL.Query().Get("trace_id") != "trace/123" {
		t.Fatalf("unexpected query: %s", received.URL.RawQuery)
	}
	if strings.TrimSpace(result.stdout) != `{"data":[],"meta":{"pagination":{"limit":25,"offset":50,"total":0,"has_more":false}}}` {
		t.Fatalf("unexpected stdout: %s", result.stdout)
	}
}

func TestEnvironmentKeyWorksWithoutLogin(t *testing.T) {
	const apiKey = "updog_ci_key"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-API-Key") != apiKey {
			t.Errorf("API key = %q", request.Header.Get("X-API-Key"))
		}
		io.WriteString(writer, `{"data":[],"meta":{"pagination":{"total":0}}}`)
	}))
	defer server.Close()

	result := runTestCLI(t, filepath.Join(t.TempDir(), "missing.json"), newMemorySecrets(), map[string]string{
		"UPDOG_API_KEY": apiKey,
		"UPDOG_URL":     server.URL,
	}, "", false, "errors", "search", "--status", "unresolved")
	if result.status != 0 {
		t.Fatalf("status = %d, stderr = %s", result.status, result.stderr)
	}
}

func TestEnvironmentKeyRejectsAnExplicitProjectProfile(t *testing.T) {
	result := runTestCLI(t, filepath.Join(t.TempDir(), "config.json"), newMemorySecrets(), map[string]string{
		"UPDOG_API_KEY": "updog_ci_key",
	}, "", false, "--project", "production", "logs", "search")

	if result.status != 2 || !strings.Contains(result.stderr, "cannot be combined") {
		t.Fatalf("result = %#v", result)
	}
}

func TestBaseURLRequiresTLSExceptOnLoopback(t *testing.T) {
	valid := []string{
		"https://wuzupdog.com",
		"http://localhost:4000",
		"http://worker.localhost:4000",
		"http://127.0.0.1:4000",
		"http://[::1]:4000",
	}
	for _, value := range valid {
		if _, err := normalizeBaseURL(value); err != nil {
			t.Errorf("normalizeBaseURL(%q): %v", value, err)
		}
	}

	for _, value := range []string{"http://example.com", "http://192.0.2.10:4000"} {
		if _, err := normalizeBaseURL(value); err == nil || !strings.Contains(err.Error(), "HTTPS") {
			t.Errorf("normalizeBaseURL(%q) error = %v", value, err)
		}
	}
}

func TestStoredRemoteHTTPProfileIsRejectedAfterUpgrade(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := configFile{Version: configVersion, CurrentProject: "legacy", Projects: map[string]project{
		"legacy": {URL: "http://192.0.2.10:4000", CredentialID: "legacy-credential"},
	}}
	if err := saveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	secrets := newMemorySecrets()
	if err := secrets.Set("legacy-credential", "updog_legacy_key"); err != nil {
		t.Fatal(err)
	}

	result := runTestCLI(t, configPath, secrets, nil, "", false, "logs", "search")
	if result.status != 2 || !strings.Contains(result.stderr, "HTTPS") || !strings.Contains(result.stderr, "login again") {
		t.Fatalf("result = %#v", result)
	}
}

func TestCredentialChangesRollBackWhenConfigPersistenceFails(t *testing.T) {
	t.Run("login restores the prior key", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "config.json")
		cfg := configFile{Version: configVersion, CurrentProject: "mnm", Projects: map[string]project{
			"mnm": {URL: "https://old.example", CredentialID: "mnm-credential"},
		}}
		if err := saveConfig(configPath, cfg); err != nil {
			t.Fatal(err)
		}

		base := newMemorySecrets()
		if err := base.Set("mnm-credential", "updog_old_key"); err != nil {
			t.Fatal(err)
		}
		secrets := &hookSecrets{base: base, afterSet: func() {
			if err := os.Remove(configPath); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(configPath, 0o700); err != nil {
				t.Fatal(err)
			}
		}}
		app, err := newApp(Options{ConfigPath: configPath, Secrets: secrets, Out: io.Discard, Err: io.Discard})
		if err != nil {
			t.Fatal(err)
		}

		if err := app.persistLogin(globalOptions{}, "mnm", "https://new.example", "updog_new_key", project{}); err == nil {
			t.Fatal("persistLogin succeeded despite config failure")
		}
		stored, err := base.Get("mnm-credential")
		if err != nil || stored != "updog_old_key" {
			t.Fatalf("credential after rollback = %q, %v", stored, err)
		}
	})

	t.Run("logout restores the deleted key", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "config.json")
		cfg := configFile{Version: configVersion, CurrentProject: "mnm", Projects: map[string]project{
			"mnm": {URL: "https://example.test", CredentialID: "mnm-credential"},
		}}
		if err := saveConfig(configPath, cfg); err != nil {
			t.Fatal(err)
		}

		base := newMemorySecrets()
		if err := base.Set("mnm-credential", "updog_old_key"); err != nil {
			t.Fatal(err)
		}
		secrets := &hookSecrets{base: base, afterDelete: func() {
			if err := os.Remove(configPath); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(configPath, 0o700); err != nil {
				t.Fatal(err)
			}
		}}
		app, err := newApp(Options{ConfigPath: configPath, Secrets: secrets, Out: io.Discard, Err: io.Discard})
		if err != nil {
			t.Fatal(err)
		}

		if err := app.logout(globalOptions{}, nil); err == nil {
			t.Fatal("logout succeeded despite config failure")
		}
		stored, err := base.Get("mnm-credential")
		if err != nil || stored != "updog_old_key" {
			t.Fatalf("credential after rollback = %q, %v", stored, err)
		}
	})
}

func TestProjectsListAndUse(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := configFile{Version: 1, CurrentProject: "api", Projects: map[string]project{
		"mnm": {URL: "https://mnm.example", CredentialID: "mnm"},
		"api": {URL: "https://api.example", CredentialID: "api"},
	}}
	if err := saveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	secrets := newMemorySecrets()

	list := runTestCLI(t, configPath, secrets, nil, "", false, "projects", "list")
	if list.status != 0 || !strings.Contains(list.stdout, `"name":"api"`) || !strings.Contains(list.stdout, `"name":"mnm"`) {
		t.Fatalf("projects list failed: status=%d stdout=%s stderr=%s", list.status, list.stdout, list.stderr)
	}
	if strings.Index(list.stdout, `"name":"api"`) > strings.Index(list.stdout, `"name":"mnm"`) {
		t.Fatal("projects are not sorted")
	}

	use := runTestCLI(t, configPath, secrets, nil, "", false, "projects", "use", "mnm")
	if use.status != 0 {
		t.Fatalf("projects use failed: %s", use.stderr)
	}
	cfg, err := loadConfig(configPath)
	if err != nil || cfg.CurrentProject != "mnm" {
		t.Fatalf("current project = %q, err=%v", cfg.CurrentProject, err)
	}
}

func TestLogoutDeletesCredentialAndProfile(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := configFile{Version: 1, CurrentProject: "mnm", Projects: map[string]project{
		"mnm": {URL: "https://example.test", CredentialID: "mnm-secret"},
	}}
	if err := saveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	secrets := newMemorySecrets()
	secrets.Set("mnm-secret", "updog_secret")

	result := runTestCLI(t, configPath, secrets, nil, "", false, "logout")
	if result.status != 0 {
		t.Fatalf("logout failed: %s", result.stderr)
	}
	if _, err := secrets.Get("mnm-secret"); !errors.Is(err, errSecretNotFound) {
		t.Fatalf("credential still exists: %v", err)
	}
	cfg, _ = loadConfig(configPath)
	if len(cfg.Projects) != 0 || cfg.CurrentProject != "" {
		t.Fatalf("profile still exists: %#v", cfg)
	}
}

func TestAPIFailureWritesBodyToStderr(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Retry-After", "30")
		writer.Header().Set("RateLimit-Limit", "60")
		writer.Header().Set("RateLimit-Remaining", "0")
		writer.Header().Set("RateLimit-Reset", "1784491234")
		writer.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(writer, `{"error":{"code":"rate_limited","message":"slow down"}}`)
	}))
	defer server.Close()

	result := runTestCLI(t, filepath.Join(t.TempDir(), "config.json"), newMemorySecrets(), map[string]string{
		"UPDOG_API_KEY": "updog_rate_key",
		"UPDOG_URL":     server.URL,
	}, "", false, "logs", "search")
	if result.status != 1 || result.stdout != "" {
		t.Fatalf("status=%d stdout=%q", result.status, result.stdout)
	}
	if !strings.Contains(result.stderr, `"code":"rate_limited"`) ||
		!strings.Contains(result.stderr, "Retry-After: 30") ||
		!strings.Contains(result.stderr, "RateLimit-Limit: 60") ||
		!strings.Contains(result.stderr, "RateLimit-Remaining: 0") ||
		!strings.Contains(result.stderr, "RateLimit-Reset: 1784491234") {
		t.Fatalf("unexpected stderr: %s", result.stderr)
	}
}

func TestErrorShowUsesProjectScopedEndpointAndHumanDetail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/errors/42" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if request.URL.Query().Get("since") != "24h" || request.URL.Query().Get("limit") != "20" {
			t.Errorf("query = %q", request.URL.RawQuery)
		}
		io.WriteString(writer, `{"data":{"id":42,"status":"unresolved","last_seen_at":"2026-07-09T20:00:00Z","occurrence_count":3,"error_class":"ArgumentError","error_message":"bad argument","occurrences":[{"occurred_at":"2026-07-09T20:00:00Z","hostname":"web-1","message":"bad argument"}]},"meta":{"pagination":{"total":3,"limit":20,"offset":0,"has_more":true}}}`)
	}))
	defer server.Close()

	result := runTestCLI(t, filepath.Join(t.TempDir(), "config.json"), newMemorySecrets(), map[string]string{
		"UPDOG_API_KEY": "updog_error_key",
		"UPDOG_URL":     server.URL,
	}, "", true, "errors", "show", "42", "--since", "24h", "--limit", "20")
	if result.status != 0 || !strings.Contains(result.stdout, "#42 ArgumentError: bad argument") || !strings.Contains(result.stdout, "web-1") {
		t.Fatalf("error detail: status=%d stdout=%s stderr=%s", result.status, result.stdout, result.stderr)
	}

	invalid := runTestCLI(t, filepath.Join(t.TempDir(), "config.json"), newMemorySecrets(), nil, "", false, "errors", "show", "0")
	if invalid.status != 2 || !strings.Contains(invalid.stderr, "positive integer") {
		t.Fatalf("invalid ID result: %#v", invalid)
	}
}

func TestUsageAndConfigurationErrorsExitTwo(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	secrets := newMemorySecrets()

	missing := runTestCLI(t, configPath, secrets, nil, "", false, "logs", "search")
	if missing.status != 2 || !strings.Contains(missing.stderr, "updog login") {
		t.Fatalf("missing auth result: %#v", missing)
	}

	unsafe := runTestCLI(t, configPath, secrets, map[string]string{"UPDOG_API_KEY": "updog_bad\nkey"}, "", false, "errors")
	if unsafe.status != 2 || !strings.Contains(unsafe.stderr, "invalid whitespace") {
		t.Fatalf("unsafe key result: %#v", unsafe)
	}

	flagResult := runTestCLI(t, configPath, secrets, nil, "", false, "logs", "search", "--api-key", "never")
	if flagResult.status != 2 || !strings.Contains(flagResult.stderr, "flag provided but not defined") {
		t.Fatalf("key flag result: %#v", flagResult)
	}
}

func TestInteractiveOutputUsesTables(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		io.WriteString(writer, `{"data":[{"logged_at":"2026-07-09T20:00:00Z","level":"error","hostname":"web-1","message":"checkout failed"}],"meta":{"pagination":{"total":1,"limit":50,"offset":0,"has_more":false}}}`)
	}))
	defer server.Close()

	result := runTestCLI(t, filepath.Join(t.TempDir(), "config.json"), newMemorySecrets(), map[string]string{
		"UPDOG_API_KEY": "updog_table_key",
		"UPDOG_URL":     server.URL,
	}, "", true, "logs")
	if result.status != 0 || !strings.Contains(result.stdout, "TIME") || !strings.Contains(result.stdout, "checkout failed") {
		t.Fatalf("interactive output: status=%d stdout=%s stderr=%s", result.status, result.stdout, result.stderr)
	}
}
