package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const grantedProjectJSON = `[{"id":11,"name":"API","slug":"api"},{"id":22,"name":"Worker","slug":"worker"}]`

func credentialServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	discoveries := &atomic.Int32{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case deviceAuthorizationPath:
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["project_selection"] != "multiple" {
				t.Error("login did not request browser multi-selection")
			}
			writeDeviceStart(t, w, server.URL, "multi_device_code_12345678901234567890", "BCDFG-HJKLM", 5)
		case deviceTokenPath:
			fmt.Fprintf(w, `{"access_token":"updog_shared","token_type":"api_key","scope":"errors:read hosts:read logs:read","project":{"id":11,"name":"API","slug":"api"},"projects":%s}`, grantedProjectJSON)
		case "/api/v1/projects":
			if r.Header.Get("X-API-Key") != "updog_shared" || r.Header.Get("X-Updog-Project-ID") != "" {
				t.Error("incorrect discovery headers")
			}
			discoveries.Add(1)
			fmt.Fprintf(w, `{"data":%s,"meta":{"total":2}}`, grantedProjectJSON)
		case "/api/v1/logs":
			if r.Header.Get("X-API-Key") != "updog_shared" {
				t.Error("query did not reuse issued credential")
			}
			id := r.Header.Get("X-Updog-Project-ID")
			if id == "" {
				w.WriteHeader(400)
				io.WriteString(w, `{"error":{"code":"project_selection_required"}}`)
				return
			}
			if id != "11" && id != "22" {
				t.Errorf("unexpected selected project %s", id)
			}
			fmt.Fprintf(w, `{"data":[{"message":%q}],"meta":{}}`, "project "+id)
		default:
			t.Errorf("unexpected request: %s", r.URL)
			w.WriteHeader(404)
		}
	}))
	return server, discoveries
}

func TestLoginStoresOneKeyForBrowserSelectedProjectsAndQueriesWithoutFlags(t *testing.T) {
	server, discoveries := credentialServer(t)
	defer server.Close()
	configPath := filepath.Join(t.TempDir(), "config.json")
	secrets := newMemorySecrets()
	login := runTestCLIWith(t, configPath, secrets, nil, "", false, func(o *Options) {
		o.Sleep = func(context.Context, time.Duration) error { return nil }
	}, "login", "--url", server.URL)
	if login.status != 0 {
		t.Fatalf("login failed: %+v", login)
	}
	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Projects) != 1 || cfg.CurrentProject != "default" || len(cfg.Projects["default"].Projects) != 2 || len(secrets.values) != 1 {
		t.Fatalf("expected one credential/profile with two grants: %+v", cfg)
	}
	if strings.Contains(login.stdout+login.stderr, "updog_shared") {
		t.Fatal("login exposed token")
	}
	for _, args := range [][]string{{"logs", "search"}, {"--all-projects", "logs", "search"}} {
		result := runTestCLI(t, configPath, secrets, nil, "", false, args...)
		if result.status != 0 {
			t.Fatalf("query failed: %+v", result)
		}
		envelope := decodeProjects(t, result)
		if envelope.Meta.Succeeded != 2 || envelope.Data[0].Project != "api" || envelope.Data[0].ProjectID != 11 || envelope.Data[1].Project != "worker" || envelope.Data[1].ProjectID != 22 {
			t.Fatalf("incorrect grants: %+v", envelope)
		}
	}
	if discoveries.Load() != 2 {
		t.Fatalf("must use server grants each command, got %d discoveries", discoveries.Load())
	}
	listing := runTestCLI(t, configPath, secrets, nil, "", true, "projects", "list")
	if !strings.Contains(listing.stdout, "API, Worker") {
		t.Fatalf("project listing hides grants: %+v", listing)
	}
}

func TestEnvironmentAndImportedKeysUseServerProjectGrants(t *testing.T) {
	server, _ := credentialServer(t)
	defer server.Close()
	configPath := filepath.Join(t.TempDir(), "config.json")
	secrets := newMemorySecrets()
	env := map[string]string{"UPDOG_API_KEY": "updog_shared", "UPDOG_URL": server.URL}
	for _, args := range [][]string{{"logs", "search"}, {"--all-projects", "logs", "search"}} {
		result := runTestCLI(t, configPath, secrets, env, "", false, args...)
		if result.status != 0 || decodeProjects(t, result).Meta.Succeeded != 2 {
			t.Fatalf("environment query failed: %+v", result)
		}
	}
	login := runTestCLI(t, configPath, secrets, nil, "updog_shared\n", false, "login", "--token-stdin", "--project", "shared", "--url", server.URL)
	if login.status != 0 {
		t.Fatalf("import failed: %+v", login)
	}
	result := runTestCLI(t, configPath, secrets, nil, "", false, "--all-projects", "logs", "search")
	if result.status != 0 || decodeProjects(t, result).Meta.Succeeded != 2 {
		t.Fatalf("imported query failed: %+v", result)
	}
}

func TestInvalidProjectGrantsFailClosed(t *testing.T) {
	for _, value := range []string{`[]`, `[ {"id":0,"name":"API","slug":"api"} ]`, `[ {"id":1,"name":"API","slug":"api"}, {"id":1,"name":"Other","slug":"other"} ]`, `[ {"id":1,"name":"API","slug":"../api"} ]`} {
		var projects []deviceProject
		if err := json.Unmarshal([]byte(value), &projects); err != nil {
			t.Fatal(err)
		}
		if validateGrantedProjects(projects) == nil {
			t.Fatalf("accepted invalid grants: %s", value)
		}
	}
	token := deviceToken{AccessToken: "updog_key", TokenType: "api_key", Scope: deviceReadScope, Project: deviceProject{ID: 1, Name: "API", Slug: "api"}, Projects: []deviceProject{{ID: 2, Name: "Worker", Slug: "worker"}}}
	if validateDeviceToken(token) == nil {
		t.Fatal("accepted mismatched primary project")
	}
}

func TestRevokedCredentialDoesNotQueryCachedProjectMetadata(t *testing.T) {
	var telemetry atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/projects" {
			telemetry.Add(1)
		}
		w.WriteHeader(401)
		io.WriteString(w, `{"error":"revoked"}`)
	}))
	defer server.Close()
	configPath, secrets := projectTestConfig(t, server.URL, "shared")
	cfg, _ := loadConfig(configPath)
	entry := cfg.Projects["shared"]
	if err := json.Unmarshal([]byte(grantedProjectJSON), &entry.Projects); err != nil {
		t.Fatal(err)
	}
	cfg.Projects["shared"] = entry
	if err := saveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	result := runTestCLI(t, configPath, secrets, nil, "", false, "--all-projects", "logs", "search")
	if result.status != 1 || telemetry.Load() != 0 {
		t.Fatalf("queried revoked grants: %+v", result)
	}
}
