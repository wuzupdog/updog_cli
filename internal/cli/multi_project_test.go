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
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func projectTestConfig(t *testing.T, serverURL string, names ...string) (string, *memorySecrets) {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.json")
	secrets := newMemorySecrets()
	cfg := configFile{Version: configVersion, CurrentProject: names[0], Projects: map[string]project{}}
	for _, name := range names {
		cfg.Projects[name] = project{URL: serverURL, CredentialID: name}
		if err := secrets.Set(name, "updog_"+name); err != nil {
			t.Fatal(err)
		}
	}
	if err := saveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	return configPath, secrets
}

type multiProjectEnvelope struct {
	Data []projectResponse                         `json:"data"`
	Meta struct{ Projects, Succeeded, Failed int } `json:"meta"`
}

func decodeProjects(t *testing.T, result cliResult) multiProjectEnvelope {
	t.Helper()
	var envelope multiProjectEnvelope
	if err := json.Unmarshal([]byte(result.stdout), &envelope); err != nil {
		t.Fatalf("decode output: %v: %+v", err, result)
	}
	return envelope
}

func TestMultipleProjectsKeepCredentialsQueriesAndResponsesSeparate(t *testing.T) {
	var mu sync.Mutex
	counts := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/logs" || r.URL.Query().Get("q") != "timeout" || r.URL.Query().Get("limit") != "2" || r.URL.Query().Get("offset") != "3" {
			t.Errorf("unexpected request: %s", r.URL)
		}
		name := strings.TrimPrefix(r.Header.Get("X-API-Key"), "updog_")
		mu.Lock()
		counts[name]++
		mu.Unlock()
		fmt.Fprintf(w, `{"data":[{"message":%q}],"meta":{"pagination":{"total":8,"has_more":true},"custom":"preserved"}}`, name)
	}))
	defer server.Close()
	configPath, secrets := projectTestConfig(t, server.URL, "alpha", "beta", "unused")
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	result := runTestCLI(t, configPath, secrets, nil, "", false,
		"--project=beta", "logs", "search", "--project", "alpha", "--project", "beta", "--query", "timeout", "--limit", "2", "--offset", "3")
	if result.status != 0 {
		t.Fatalf("query failed: %+v", result)
	}
	envelope := decodeProjects(t, result)
	if envelope.Meta.Projects != 2 || envelope.Meta.Succeeded != 2 || envelope.Meta.Failed != 0 || len(envelope.Data) != 2 {
		t.Fatalf("unexpected envelope: %+v", envelope)
	}
	for i, name := range []string{"beta", "alpha"} {
		row := envelope.Data[i]
		if row.Project != name || row.URL != server.URL || !strings.Contains(string(row.Response), `"message":"`+name+`"`) || !strings.Contains(string(row.Response), `"custom":"preserved"`) {
			t.Errorf("unexpected project result: %+v", row)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if counts["alpha"] != 1 || counts["beta"] != 1 || len(counts) != 2 {
		t.Errorf("requests = %v", counts)
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("query changed current project/configuration")
	}
	if strings.Contains(result.stdout+result.stderr, "updog_alpha") || strings.Contains(result.stdout+result.stderr, "updog_beta") {
		t.Fatal("output exposed credentials")
	}
}

func TestAllProjectsSupportsEveryTelemetryCommand(t *testing.T) {
	for _, command := range []struct {
		args []string
		path string
		body string
	}{
		{[]string{"logs", "search"}, "/api/v1/logs", `{"data":[],"meta":{}}`},
		{[]string{"errors", "search"}, "/api/v1/errors", `{"data":[],"meta":{}}`},
		{[]string{"errors", "show", "42"}, "/api/v1/errors/42", `{"data":{"id":42},"meta":{}}`},
		{[]string{"hosts", "list"}, "/api/v1/hosts", `{"data":[],"meta":{}}`},
		{[]string{"hosts", "show", "zone-1"}, "/api/v1/hosts", `{"data":[{"hostname":"zone-1"}],"meta":{}}`},
	} {
		t.Run(strings.Join(command.args, "_"), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if len(command.args) == 3 && command.args[0] == "hosts" && r.URL.Query().Get("hostname") != "zone-1" {
					t.Errorf("missing hostname filter: %s", r.URL)
				}
				if r.URL.Path != command.path {
					t.Errorf("path = %s", r.URL.Path)
				}
				io.WriteString(w, command.body)
			}))
			defer server.Close()
			configPath, secrets := projectTestConfig(t, server.URL, "beta", "alpha")
			result := runTestCLI(t, configPath, secrets, nil, "", false, append(command.args, "--all-projects")...)
			if result.status != 0 {
				t.Fatalf("query failed: %+v", result)
			}
			envelope := decodeProjects(t, result)
			if len(envelope.Data) != 2 || envelope.Data[0].Project != "alpha" || envelope.Data[1].Project != "beta" {
				t.Fatalf("unexpected order: %+v", envelope)
			}
		})
	}
}

func TestProjectFailuresPreserveSuccessfulResultsAndRetryMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("X-API-Key") {
		case "updog_limited":
			w.Header().Set("Retry-After", "30")
			w.Header().Set("RateLimit-Limit", "60")
			w.Header().Set("RateLimit-Remaining", "0")
			w.Header().Set("RateLimit-Reset", "123")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":{"code":"rate_limited"}}`)
		case "updog_malformed":
			io.WriteString(w, "not json")
		default:
			io.WriteString(w, `{"data":[],"meta":{}}`)
		}
	}))
	defer server.Close()
	configPath, secrets := projectTestConfig(t, server.URL, "good", "limited", "malformed")
	result := runTestCLI(t, configPath, secrets, nil, "", false, "--all-projects", "logs", "search")
	envelope := decodeProjects(t, result)
	if result.status != 1 || envelope.Meta.Succeeded != 1 || envelope.Meta.Failed != 2 {
		t.Fatalf("unexpected result: %+v %+v", result, envelope)
	}
	failure := envelope.Data[1].Error
	if failure == nil || failure.RetryAfter != "30" || failure.RateLimitLimit != "60" || failure.RateLimitRemaining != "0" || failure.RateLimitReset != "123" || !strings.Contains(string(failure.Body), "rate_limited") {
		t.Fatalf("missing rate limit details: %+v", failure)
	}
	terminal := runTestCLI(t, configPath, secrets, nil, "", true, "--project", "good", "--project", "limited", "logs", "search")
	if terminal.status != 1 || !strings.Contains(terminal.stdout, "Project: good") || !strings.Contains(terminal.stderr, "Project: limited") || !strings.Contains(terminal.stderr, "Retry-After: 30") {
		t.Fatalf("terminal result: %+v", terminal)
	}
}

func TestMissingProjectCredentialDoesNotHideOtherProjects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"data":[]}`) }))
	defer server.Close()
	configPath, secrets := projectTestConfig(t, server.URL, "good", "missing")
	if err := secrets.Delete("missing"); err != nil {
		t.Fatal(err)
	}
	result := runTestCLI(t, configPath, secrets, nil, "", false, "--all-projects", "logs", "search")
	envelope := decodeProjects(t, result)
	if result.status != 2 || envelope.Meta.Succeeded != 1 || envelope.Data[1].Error == nil || envelope.Data[1].Error.ExitCode != 2 {
		t.Fatalf("unexpected result: %+v %+v", result, envelope)
	}
}

func TestMultipleProjectSelectionRejectsUnsafeOrUnsupportedCombinations(t *testing.T) {
	for _, args := range [][]string{
		{"--project", "alpha", "--all-projects", "logs"},
		{"--project=", "logs"},
		{"--project", "alpha", "--project", "bad/name", "logs"},
		{"--all-projects", "login"},
		{"--project", "alpha", "--project", "beta", "logout"},
		{"--all-projects", "projects", "use", "alpha"},
		{"--all-projects", "auth", "status"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			result := runTestCLI(t, filepath.Join(t.TempDir(), "config.json"), newMemorySecrets(), nil, "", false, args...)
			if result.status != 2 || result.stdout != "" {
				t.Fatalf("unexpected result: %+v", result)
			}
		})
	}
	for _, args := range [][]string{{"--all-projects", "logs"}, {"--project", "alpha", "--project", "beta", "logs"}} {
		result := runTestCLI(t, filepath.Join(t.TempDir(), "config.json"), newMemorySecrets(), map[string]string{"UPDOG_API_KEY": "updog_env"}, "", false, args...)
		if result.status != 2 || !strings.Contains(result.stderr, "UPDOG_API_KEY") {
			t.Fatalf("unexpected result: %+v", result)
		}
	}
	empty := runTestCLI(t, filepath.Join(t.TempDir(), "config.json"), newMemorySecrets(), nil, "", false, "--all-projects", "logs")
	if empty.status != 2 || !strings.Contains(empty.stderr, "no projects configured") {
		t.Fatalf("unexpected result: %+v", empty)
	}
}

func TestSingleProjectOutputRemainsUnwrapped(t *testing.T) {
	const body = `{"data":[],"meta":{"pagination":{"total":0}}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
	defer server.Close()
	configPath, secrets := projectTestConfig(t, server.URL, "alpha")
	for _, args := range [][]string{{"logs"}, {"--project", "alpha", "logs"}, {"--project", "alpha", "--project=alpha", "logs"}} {
		result := runTestCLI(t, configPath, secrets, nil, "", false, args...)
		if result.status != 0 || strings.TrimSpace(result.stdout) != body {
			t.Fatalf("single project changed: %+v", result)
		}
	}
	all := runTestCLI(t, configPath, secrets, nil, "", false, "--all-projects", "logs")
	if envelope := decodeProjects(t, all); all.status != 0 || len(envelope.Data) != 1 || envelope.Data[0].Project != "alpha" {
		t.Fatalf("all-projects must keep stable envelope: %+v", all)
	}
}

func TestMultiProjectRequestsRunConcurrentlyWithinBoundAndHonorCancellation(t *testing.T) {
	var active, maximum atomic.Int32
	started := make(chan struct{}, 8)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for old := maximum.Load(); current > old; old = maximum.Load() {
			if maximum.CompareAndSwap(old, current) {
				break
			}
		}
		started <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
		}
		io.WriteString(w, `{"data":[]}`)
	}))
	defer server.Close()
	configPath, secrets := projectTestConfig(t, server.URL, "a", "b", "c", "d", "e", "f", "g", "h")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer close(release)
	done := make(chan cliResult, 1)
	go func() {
		done <- runTestCLIWith(t, configPath, secrets, nil, "", false, func(o *Options) { o.Context = ctx }, "--all-projects", "logs")
	}()
	for range projectConcurrency {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("requests did not run concurrently")
		}
	}
	cancel()
	select {
	case result := <-done:
		if result.status != 1 {
			t.Fatalf("canceled query succeeded: %+v", result)
		}
		if maximum.Load() > projectConcurrency {
			t.Fatalf("too many in-flight requests: %d", maximum.Load())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not stop workers")
	}
}

func TestMissingHostIsAProjectFailureInJSONAndTerminalOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") == "updog_missing" {
			io.WriteString(w, `{"data":[],"meta":{}}`)
			return
		}
		io.WriteString(w, `{"data":[{"hostname":"zone-1"}],"meta":{}}`)
	}))
	defer server.Close()
	configPath, secrets := projectTestConfig(t, server.URL, "missing", "present")
	for _, terminal := range []bool{false, true} {
		result := runTestCLI(t, configPath, secrets, nil, "", terminal, "--all-projects", "hosts", "show", "zone-1")
		if result.status != 1 {
			t.Fatalf("missing host must fail: %+v", result)
		}
		if terminal {
			if !strings.Contains(result.stdout, "Project: present") || !strings.Contains(result.stdout, "zone-1") || !strings.Contains(result.stderr, "host not found") {
				t.Fatalf("missing partial output: %+v", result)
			}
		} else {
			envelope := decodeProjects(t, result)
			if envelope.Meta.Failed != 1 || envelope.Meta.Succeeded != 1 || envelope.Data[0].Error.Message != "host not found" {
				t.Fatalf("unexpected envelope: %+v", envelope)
			}
		}
	}
}

func TestProjectsUseTheirOwnServerAndJSONCanBeForcedInTerminal(t *testing.T) {
	cfg := configFile{Version: configVersion, Projects: map[string]project{}}
	secrets := newMemorySecrets()
	for _, name := range []string{"alpha", "beta"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-API-Key") != "updog_"+name {
				t.Errorf("credential sent to wrong server")
			}
			fmt.Fprintf(w, `{"data":[{"message":%q}],"meta":{}}`, name)
		}))
		defer server.Close()
		cfg.Projects[name] = project{URL: server.URL, CredentialID: name}
		if err := secrets.Set(name, "updog_"+name); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := saveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	result := runTestCLI(t, configPath, secrets, nil, "", true, "--all-projects", "--json", "logs", "search")
	if result.status != 0 {
		t.Fatalf("query failed: %+v", result)
	}
	for _, row := range decodeProjects(t, result).Data {
		if row.URL != cfg.Projects[row.Project].URL || !strings.Contains(string(row.Response), row.Project) {
			t.Fatalf("server mismatch: %+v", row)
		}
	}
}
