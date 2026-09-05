package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
)

func projectSelectionRequired(err error) bool {
	var responseErr *apiError
	if !errors.As(err, &responseErr) || responseErr.StatusCode != http.StatusBadRequest {
		return false
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	return json.Unmarshal(responseErr.Body, &body) == nil && body.Error.Code == "project_selection_required"
}

func validateGrantedProjects(projects []deviceProject) error {
	if len(projects) == 0 || len(projects) > 100 {
		return errors.New("invalid project grants")
	}
	ids := map[int64]bool{}
	for _, project := range projects {
		if project.ID <= 0 || ids[project.ID] || unsafeProjectName(project.Name) || validateProjectName(project.Slug) != nil {
			return errors.New("invalid project grants")
		}
		ids[project.ID] = true
	}
	return nil
}

func (a *app) discoverProjects(auth resolvedAuth) ([]deviceProject, error) {
	client := apiClient{baseURL: auth.baseURL, apiKey: auth.apiKey, version: a.version, httpClient: a.httpClient}
	body, err := client.get(a.context, "/api/v1/projects", nil)
	if err != nil {
		return nil, a.apiCommandError("project discovery failed", err)
	}
	var result struct {
		Data []deviceProject `json:"data"`
	}
	if json.Unmarshal(body, &result) != nil || validateGrantedProjects(result.Data) != nil {
		return nil, errors.New("Updog returned an invalid project list")
	}
	return result.Data, nil
}

func (a *app) credentialTargets(auth resolvedAuth) ([]resolvedAuth, error) {
	projects, err := a.discoverProjects(auth)
	if err != nil {
		return nil, err
	}
	targets := make([]resolvedAuth, len(projects))
	for i, project := range projects {
		target := auth
		target.project = project.Slug
		target.projectID = project.ID
		targets[i] = target
	}
	return targets, nil
}

func (a *app) getCredentialProjectsAndRender(g globalOptions, auth resolvedAuth, path string, query url.Values, kind string) error {
	targets, err := a.credentialTargets(auth)
	if err != nil {
		return err
	}
	responses := make([]projectResponse, len(targets))
	responses = a.requestTargets(targets, responses, path, query, kind)
	return a.renderProjectResponses(g, responses, kind)
}
