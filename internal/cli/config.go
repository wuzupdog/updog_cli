package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const configVersion = 1

var projectNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type configFile struct {
	Version        int                `json:"version"`
	CurrentProject string             `json:"current_project,omitempty"`
	Projects       map[string]project `json:"projects"`
}

type project struct {
	URL          string `json:"url"`
	CredentialID string `json:"credential_id"`
}

func defaultConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find user configuration directory: %w", err)
	}
	return filepath.Join(dir, "updog", "config.json"), nil
}

func loadConfig(path string) (configFile, error) {
	cfg := configFile{Version: configVersion, Projects: map[string]project{}}

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("read configuration: %w", err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse configuration: %w", err)
	}
	if cfg.Version != configVersion {
		return cfg, fmt.Errorf("configuration version %d is not supported", cfg.Version)
	}
	if cfg.Projects == nil {
		cfg.Projects = map[string]project{}
	}
	return cfg, nil
}

func saveConfig(path string, cfg configFile) error {
	if cfg.Projects == nil {
		cfg.Projects = map[string]project{}
	}
	cfg.Version = configVersion

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create configuration directory: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode configuration: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, ".config-*")
	if err != nil {
		return fmt.Errorf("create temporary configuration: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("secure temporary configuration: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write configuration: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close configuration: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace configuration: %w", err)
	}
	return nil
}

func validateProjectName(name string) error {
	if !projectNamePattern.MatchString(name) {
		return errors.New("project name must start with a letter or number and contain only letters, numbers, '.', '_' or '-'")
	}
	return nil
}

func projectNames(cfg configFile) []string {
	names := make([]string, 0, len(cfg.Projects))
	for name := range cfg.Projects {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func newCredentialID(name, baseURL string) string {
	digest := sha256.Sum256([]byte(strings.ToLower(baseURL)))
	return name + ":" + hex.EncodeToString(digest[:6])
}
