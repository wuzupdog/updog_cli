package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"sync"
)

const projectConcurrency = 4

func (g *globalOptions) addProject(name string) error {
	if err := validateProjectName(name); err != nil {
		return usageError(err.Error())
	}
	if !slices.Contains(g.projects, name) {
		g.projects = append(g.projects, name)
	}
	g.project = g.projects[0]
	return nil
}

func (g globalOptions) multipleProjects() bool {
	return g.allProjects || len(g.projects) > 1
}

func (g globalOptions) validateCommand(args []string) error {
	if !g.multipleProjects() || hasHelp(args) {
		return nil
	}
	switch args[0] {
	case "logs", "errors", "hosts", "help":
		return nil
	default:
		return usageError("multiple projects are supported only for hosts, logs, and errors")
	}
}

type projectFailure struct {
	Message            string          `json:"message"`
	ExitCode           int             `json:"exit_code"`
	Body               json.RawMessage `json:"body,omitempty"`
	RetryAfter         string          `json:"retry_after,omitempty"`
	RateLimitLimit     string          `json:"rate_limit_limit,omitempty"`
	RateLimitRemaining string          `json:"rate_limit_remaining,omitempty"`
	RateLimitReset     string          `json:"rate_limit_reset,omitempty"`
}

type projectResponse struct {
	Project   string          `json:"project"`
	ProjectID int64           `json:"project_id,omitempty"`
	URL       string          `json:"url,omitempty"`
	Response  json.RawMessage `json:"response,omitempty"`
	Error     *projectFailure `json:"error,omitempty"`
}

func (a *app) selectedProjects(g globalOptions) ([]string, error) {
	if a.getenv("UPDOG_API_KEY") != "" {
		return nil, configError("multiple projects cannot be combined with UPDOG_API_KEY; use stored project profiles")
	}
	if !g.allProjects {
		return g.projects, nil
	}
	cfg, err := loadConfig(a.configPath)
	if err != nil {
		return nil, configError(err.Error())
	}
	names := projectNames(cfg)
	if len(names) == 0 {
		return nil, configError("no projects configured; run updog login for each project")
	}
	return names, nil
}

func (a *app) getProjectsAndRender(g globalOptions, path string, query url.Values, kind string) error {
	names, err := a.selectedProjects(g)
	if err != nil {
		return err
	}
	responses := a.requestProjects(names, path, query, kind)
	return a.renderProjectResponses(g, responses, kind)
}

func (a *app) renderProjectResponses(g globalOptions, responses []projectResponse, kind string) error {
	var err error
	failed, exitCode := projectFailureCounts(responses)
	if g.json || !a.outputIsTerminal() {
		err = writeJSON(a.out, map[string]any{
			"data": responses,
			"meta": map[string]int{"projects": len(responses), "succeeded": len(responses) - failed, "failed": failed},
		})
	} else {
		err = a.renderProjects(responses, kind)
	}
	if err != nil {
		return err
	}
	if failed > 0 {
		return &commandError{code: exitCode, message: fmt.Sprintf("%d of %d projects failed", failed, len(responses))}
	}
	return nil
}

func (a *app) requestProjects(names []string, path string, query url.Values, kind string) []projectResponse {
	auths := make([]resolvedAuth, 0, len(names))
	responses := make([]projectResponse, 0, len(names))
	// Resolve credentials and discover grants before starting bounded network workers.
	for _, name := range names {
		auth, err := a.resolveAuth(name)
		if err != nil {
			auths = append(auths, resolvedAuth{})
			responses = append(responses, projectResponse{Project: name, Error: projectError(err)})
			continue
		}
		targets := []resolvedAuth{auth}
		if len(auth.projects) > 1 {
			targets, err = a.credentialTargets(auth)
		}
		if err != nil {
			auths = append(auths, resolvedAuth{})
			responses = append(responses, projectResponse{Project: name, URL: auth.baseURL, Error: projectError(err)})
			continue
		}
		for _, target := range targets {
			auths = append(auths, target)
			responses = append(responses, projectResponse{Project: target.project, ProjectID: target.projectID, URL: target.baseURL})
		}
	}
	return a.requestTargets(auths, responses, path, query, kind)
}

func (a *app) requestTargets(auths []resolvedAuth, responses []projectResponse, path string, query url.Values, kind string) []projectResponse {
	var workers sync.WaitGroup
	jobs := make(chan int)
	for range min(projectConcurrency, len(auths)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range jobs {
				responses[i] = a.requestProject(auths[i], path, query, kind)
			}
		}()
	}
	for i := range auths {
		if responses[i].Error == nil {
			jobs <- i
		}
	}
	close(jobs)
	workers.Wait()
	return responses
}

func (a *app) requestProject(auth resolvedAuth, path string, query url.Values, kind string) projectResponse {
	result := projectResponse{Project: auth.project, ProjectID: auth.projectID, URL: auth.baseURL}
	client := apiClient{baseURL: auth.baseURL, apiKey: auth.apiKey, version: a.version, httpClient: a.httpClient, projectID: auth.projectID}
	body, err := client.get(a.context, path, query)
	if err != nil {
		result.Error = projectError(a.apiCommandError("request failed", err))
		return result
	}
	if !json.Valid(body) {
		result.Error = projectError(errors.New("Updog returned an invalid JSON response"))
		return result
	}
	if err := renderAPIResponse(io.Discard, kind, body, false); err != nil {
		result.Error = projectError(err)
		return result
	}
	result.Response = body
	return result
}

func projectError(err error) *projectFailure {
	failure := &projectFailure{Message: err.Error(), ExitCode: 1}
	var commandErr *commandError
	if errors.As(err, &commandErr) {
		failure.ExitCode = commandErr.code
		if json.Valid(commandErr.body) {
			failure.Body = commandErr.body
		}
		failure.RetryAfter = commandErr.retryAfter
		failure.RateLimitLimit = commandErr.rateLimitLimit
		failure.RateLimitRemaining = commandErr.rateLimitRemaining
		failure.RateLimitReset = commandErr.rateLimitReset
	}
	return failure
}

func projectFailureCounts(responses []projectResponse) (failed, exitCode int) {
	for _, result := range responses {
		if result.Error != nil {
			failed++
			exitCode = max(exitCode, result.Error.ExitCode)
		}
	}
	return
}

func (a *app) renderProjects(responses []projectResponse, kind string) error {
	for _, result := range responses {
		if _, err := fmt.Fprintf(a.out, "\nProject: %s (%s)\n", result.Project, result.URL); err != nil {
			return err
		}
		if result.Error != nil {
			fmt.Fprintf(a.err, "Project: %s\n", result.Project)
			a.finish(&commandError{
				code: result.Error.ExitCode, message: result.Error.Message, body: result.Error.Body,
				retryAfter: result.Error.RetryAfter, rateLimitLimit: result.Error.RateLimitLimit,
				rateLimitRemaining: result.Error.RateLimitRemaining, rateLimitReset: result.Error.RateLimitReset,
			})
			continue
		}
		if err := renderAPIResponse(a.out, kind, result.Response, false); err != nil {
			return err
		}
	}
	return nil
}
