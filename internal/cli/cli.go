package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"
)

type Options struct {
	Version string
	Args    []string
	In      io.Reader
	Out     io.Writer
	Err     io.Writer
	Context context.Context

	Getenv           func(string) string
	ConfigPath       string
	Secrets          secretStore
	HTTPClient       *http.Client
	InputIsTerminal  func() bool
	OutputIsTerminal func() bool
	ReadPassword     func() (string, error)
	Getwd            func() (string, error)
	Sleep            func(context.Context, time.Duration) error
	Now              func() time.Time
	DeviceName       string
}

type app struct {
	version          string
	args             []string
	in               io.Reader
	reader           *bufio.Reader
	out              io.Writer
	err              io.Writer
	context          context.Context
	getenv           func(string) string
	configPath       string
	secrets          secretStore
	httpClient       *http.Client
	inputIsTerminal  func() bool
	outputIsTerminal func() bool
	readPassword     func() (string, error)
	getwd            func() (string, error)
	sleep            func(context.Context, time.Duration) error
	now              func() time.Time
	deviceName       string
}

type globalOptions struct {
	project string
	json    bool
}

type commandError struct {
	code               int
	message            string
	body               []byte
	retryAfter         string
	rateLimitLimit     string
	rateLimitRemaining string
	rateLimitReset     string
}

func (e *commandError) Error() string { return e.message }

func Run(options Options) int {
	a, err := newApp(options)
	if err != nil {
		errOut := options.Err
		if errOut == nil {
			errOut = os.Stderr
		}
		fmt.Fprintf(errOut, "updog: %s\n", err)
		return 2
	}
	return a.run()
}

func newApp(options Options) (*app, error) {
	if options.Version == "" {
		options.Version = "dev"
	}
	if options.Args == nil {
		options.Args = os.Args[1:]
	}
	if options.In == nil {
		options.In = os.Stdin
	}
	if options.Out == nil {
		options.Out = os.Stdout
	}
	if options.Err == nil {
		options.Err = os.Stderr
	}
	if options.Context == nil {
		options.Context = context.Background()
	}
	if options.Getenv == nil {
		options.Getenv = os.Getenv
	}
	if options.ConfigPath == "" {
		path, err := defaultConfigPath()
		if err != nil {
			return nil, err
		}
		options.ConfigPath = path
	}
	if options.Secrets == nil {
		options.Secrets = osKeyring{}
	}
	if options.HTTPClient == nil {
		options.HTTPClient = defaultHTTPClient()
	}
	if options.InputIsTerminal == nil {
		options.InputIsTerminal = func() bool { return terminalReader(options.In) }
	}
	if options.OutputIsTerminal == nil {
		options.OutputIsTerminal = func() bool { return terminalWriter(options.Out) }
	}
	if options.ReadPassword == nil {
		options.ReadPassword = func() (string, error) {
			file, ok := options.In.(*os.File)
			if !ok || !term.IsTerminal(int(file.Fd())) {
				return "", errors.New("secure input requires a terminal; use --token-stdin for redirected input")
			}
			value, err := term.ReadPassword(int(file.Fd()))
			return string(value), err
		}
	}
	if options.Getwd == nil {
		options.Getwd = os.Getwd
	}
	if options.Sleep == nil {
		options.Sleep = sleepContext
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.DeviceName == "" {
		options.DeviceName = defaultDeviceName(options.Version)
	}

	return &app{
		version:          options.Version,
		args:             options.Args,
		in:               options.In,
		reader:           bufio.NewReader(options.In),
		out:              options.Out,
		err:              options.Err,
		context:          options.Context,
		getenv:           options.Getenv,
		configPath:       options.ConfigPath,
		secrets:          options.Secrets,
		httpClient:       options.HTTPClient,
		inputIsTerminal:  options.InputIsTerminal,
		outputIsTerminal: options.OutputIsTerminal,
		readPassword:     options.ReadPassword,
		getwd:            options.Getwd,
		sleep:            options.Sleep,
		now:              options.Now,
		deviceName:       options.DeviceName,
	}, nil
}

func terminalReader(reader io.Reader) bool {
	file, ok := reader.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func terminalWriter(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func (a *app) run() int {
	globals, args, err := parseGlobals(a.args)
	if err != nil {
		return a.finish(err)
	}
	if len(args) == 0 {
		a.printHelp()
		return 0
	}

	var commandErr error
	switch args[0] {
	case "help", "-h", "--help":
		a.printHelp()
	case "version":
		fmt.Fprintf(a.out, "updog %s\n", a.version)
	case "login":
		commandErr = a.login(globals, args[1:])
	case "logout":
		commandErr = a.logout(globals, args[1:])
	case "auth":
		commandErr = a.auth(globals, args[1:])
	case "projects":
		commandErr = a.projects(globals, args[1:])
	case "logs":
		commandErr = a.logs(globals, args[1:])
	case "errors":
		commandErr = a.errors(globals, args[1:])
	default:
		commandErr = usageError("unknown command: " + args[0])
	}
	return a.finish(commandErr)
}

func (a *app) finish(err error) int {
	if err == nil {
		return 0
	}
	var commandErr *commandError
	if !errors.As(err, &commandErr) {
		commandErr = &commandError{code: 1, message: err.Error()}
	}
	if len(commandErr.body) > 0 {
		fmt.Fprintln(a.err, string(compactJSON(commandErr.body)))
	} else {
		fmt.Fprintf(a.err, "updog: %s\n", commandErr.message)
	}
	if commandErr.retryAfter != "" {
		fmt.Fprintf(a.err, "Retry-After: %s\n", commandErr.retryAfter)
	}
	if commandErr.rateLimitLimit != "" {
		fmt.Fprintf(a.err, "RateLimit-Limit: %s\n", commandErr.rateLimitLimit)
	}
	if commandErr.rateLimitRemaining != "" {
		fmt.Fprintf(a.err, "RateLimit-Remaining: %s\n", commandErr.rateLimitRemaining)
	}
	if commandErr.rateLimitReset != "" {
		fmt.Fprintf(a.err, "RateLimit-Reset: %s\n", commandErr.rateLimitReset)
	}
	if commandErr.code == 2 {
		fmt.Fprintln(a.err, `Try "updog help" for usage.`)
	}
	return commandErr.code
}

func usageError(message string) error  { return &commandError{code: 2, message: message} }
func configError(message string) error { return &commandError{code: 2, message: message} }

func parseGlobals(args []string) (globalOptions, []string, error) {
	var globals globalOptions
	remaining := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			globals.json = true
		case arg == "--project":
			if index+1 >= len(args) {
				return globals, nil, usageError("option --project requires a value")
			}
			globals.project = args[index+1]
			index++
		case strings.HasPrefix(arg, "--project="):
			globals.project = strings.TrimPrefix(arg, "--project=")
		default:
			remaining = append(remaining, arg)
		}
	}
	if globals.project != "" {
		if err := validateProjectName(globals.project); err != nil {
			return globals, nil, usageError(err.Error())
		}
	}
	return globals, remaining, nil
}

func (a *app) login(globals globalOptions, args []string) error {
	fs := newFlagSet("login")
	baseURL := a.getenv("UPDOG_URL")
	if baseURL == "" {
		baseURL = defaultUpdogURL
	}
	fs.StringVar(&baseURL, "url", baseURL, "Updog server URL")
	manual := fs.Bool("manual", false, "enter an existing read-only API key")
	tokenStdin := fs.Bool("token-stdin", false, "read the API key from standard input")
	if hasHelp(args) {
		a.printLoginHelp()
		return nil
	}
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if fs.NArg() != 0 {
		return usageError("login does not accept positional arguments")
	}
	normalizedURL, err := normalizeBaseURL(baseURL)
	if err != nil {
		return configError(err.Error())
	}

	if *manual || *tokenStdin {
		return a.manualLogin(globals, normalizedURL, *tokenStdin)
	}
	return a.deviceLogin(globals, normalizedURL)
}

func (a *app) manualLogin(globals globalOptions, normalizedURL string, tokenStdin bool) error {
	projectName := globals.project
	if projectName == "" {
		if !a.inputIsTerminal() || tokenStdin {
			return usageError("login requires --project when input is not interactive")
		}
		defaultName := "project"
		if cwd, err := a.getwd(); err == nil {
			if name := filepath.Base(cwd); validateProjectName(name) == nil {
				defaultName = name
			}
		}
		value, err := a.promptLine("Project name", defaultName)
		if err != nil {
			return configError("read project name: " + err.Error())
		}
		projectName = value
	}
	if err := validateProjectName(projectName); err != nil {
		return usageError(err.Error())
	}

	var rawKey string
	if tokenStdin {
		line, err := a.reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return configError("read API key from stdin: " + err.Error())
		}
		rawKey = line
	} else {
		if !a.inputIsTerminal() {
			return configError("login requires a terminal; use --token-stdin for redirected input")
		}
		fmt.Fprint(a.err, "Read-only API key: ")
		value, err := a.readPassword()
		fmt.Fprintln(a.err)
		if err != nil {
			return configError("read API key: " + err.Error())
		}
		rawKey = value
	}
	apiKey, err := validateAPIKey(rawKey)
	if err != nil {
		return configError(err.Error())
	}

	client := apiClient{baseURL: normalizedURL, apiKey: apiKey, version: a.version, httpClient: a.httpClient}
	if _, err := client.get(a.context, "/api/v1/logs", url.Values{"limit": {"1"}}); err != nil {
		return a.apiCommandError("login validation failed", err)
	}

	return a.persistLogin(globals, projectName, normalizedURL, apiKey, project{})
}

func (a *app) persistLogin(globals globalOptions, projectName, normalizedURL, apiKey string, metadata project) error {
	cfg, err := loadConfig(a.configPath)
	if err != nil {
		return configError(err.Error())
	}
	existing := cfg.Projects[projectName]
	credentialID := existing.CredentialID
	if credentialID == "" {
		credentialID = newCredentialID(projectName, normalizedURL)
	}

	oldSecret, secretErr := a.secrets.Get(credentialID)
	hadOldSecret := secretErr == nil
	if secretErr != nil && !errors.Is(secretErr, errSecretNotFound) {
		return configError(secretErr.Error())
	}
	if err := a.secrets.Set(credentialID, apiKey); err != nil {
		return configError(err.Error())
	}
	metadata.URL = normalizedURL
	metadata.CredentialID = credentialID
	cfg.Projects[projectName] = metadata
	cfg.CurrentProject = projectName
	if err := saveConfig(a.configPath, cfg); err != nil {
		var rollbackErr error
		if hadOldSecret {
			rollbackErr = a.secrets.Set(credentialID, oldSecret)
		} else {
			rollbackErr = a.secrets.Delete(credentialID)
		}
		return configPersistenceError(err, rollbackErr)
	}

	data := map[string]any{
		"project": projectName,
		"url":     normalizedURL,
		"storage": "os_keyring",
	}
	if metadata.ProjectID > 0 {
		data["project_id"] = metadata.ProjectID
	}
	if metadata.ProjectName != "" {
		data["project_name"] = metadata.ProjectName
	}
	if metadata.ProjectSlug != "" {
		data["project_slug"] = metadata.ProjectSlug
	}
	result := map[string]any{"data": data}
	if globals.json || !a.outputIsTerminal() {
		return writeJSON(a.out, result)
	}
	if metadata.ProjectName != "" {
		fmt.Fprintf(a.out, "Logged in to %s for %s as profile %q.\n", normalizedURL, metadata.ProjectName, projectName)
	} else {
		fmt.Fprintf(a.out, "Logged in to %s as project %q.\n", normalizedURL, projectName)
	}
	return nil
}

func (a *app) logout(globals globalOptions, args []string) error {
	if hasHelp(args) {
		fmt.Fprintln(a.out, "Usage: updog logout [--project NAME]")
		return nil
	}
	if len(args) != 0 {
		return usageError("logout does not accept positional arguments")
	}
	cfg, err := loadConfig(a.configPath)
	if err != nil {
		return configError(err.Error())
	}
	name := globals.project
	if name == "" {
		name = cfg.CurrentProject
	}
	if name == "" {
		return configError("no logged-in project; use UPDOG_API_KEY or run updog login")
	}
	entry, ok := cfg.Projects[name]
	if !ok {
		return configError(fmt.Sprintf("project %q is not configured", name))
	}
	oldSecret, secretErr := a.secrets.Get(entry.CredentialID)
	hadSecret := secretErr == nil
	if secretErr != nil && !errors.Is(secretErr, errSecretNotFound) {
		return configError(secretErr.Error())
	}
	if hadSecret {
		if err := a.secrets.Delete(entry.CredentialID); err != nil {
			return configError(err.Error())
		}
	}
	delete(cfg.Projects, name)
	if cfg.CurrentProject == name {
		names := projectNames(cfg)
		cfg.CurrentProject = ""
		if len(names) > 0 {
			cfg.CurrentProject = names[0]
		}
	}
	if err := saveConfig(a.configPath, cfg); err != nil {
		var rollbackErr error
		if hadSecret {
			rollbackErr = a.secrets.Set(entry.CredentialID, oldSecret)
		}
		return configPersistenceError(err, rollbackErr)
	}
	if globals.json || !a.outputIsTerminal() {
		return writeJSON(a.out, map[string]any{"data": map[string]any{"project": name, "logged_out": true}})
	}
	fmt.Fprintf(a.out, "Logged out project %q.\n", name)
	return nil
}

func (a *app) auth(globals globalOptions, args []string) error {
	if len(args) == 0 || args[0] == "status" {
		if len(args) > 1 {
			return usageError("auth status does not accept positional arguments")
		}
		auth, err := a.resolveAuth(globals.project)
		if err != nil {
			return err
		}
		result := map[string]any{"data": map[string]any{"project": auth.project, "url": auth.baseURL, "source": auth.source}}
		if globals.json || !a.outputIsTerminal() {
			return writeJSON(a.out, result)
		}
		fmt.Fprintf(a.out, "Authenticated via %s", auth.source)
		if auth.project != "" {
			fmt.Fprintf(a.out, " for project %q", auth.project)
		}
		fmt.Fprintf(a.out, " at %s.\n", auth.baseURL)
		return nil
	}
	if args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(a.out, "Usage: updog auth status [--project NAME] [--json]")
		return nil
	}
	return usageError("unknown auth command: " + args[0])
}

func (a *app) projects(globals globalOptions, args []string) error {
	if len(args) == 0 || args[0] == "list" {
		if len(args) > 1 {
			return usageError("projects list does not accept positional arguments")
		}
		return a.projectsList(globals)
	}
	if args[0] == "use" {
		if len(args) != 2 {
			return usageError("projects use requires a project name")
		}
		return a.projectsUse(globals, args[1])
	}
	if args[0] == "-h" || args[0] == "--help" {
		a.printProjectsHelp()
		return nil
	}
	return usageError("unknown projects command: " + args[0])
}

func (a *app) projectsList(globals globalOptions) error {
	cfg, err := loadConfig(a.configPath)
	if err != nil {
		return configError(err.Error())
	}
	type row struct {
		Name        string `json:"name"`
		URL         string `json:"url"`
		Current     bool   `json:"current"`
		ProjectID   int64  `json:"project_id,omitempty"`
		ProjectName string `json:"project_name,omitempty"`
		ProjectSlug string `json:"project_slug,omitempty"`
	}
	rows := make([]row, 0, len(cfg.Projects))
	for _, name := range projectNames(cfg) {
		entry := cfg.Projects[name]
		rows = append(rows, row{
			Name:        name,
			URL:         entry.URL,
			Current:     cfg.CurrentProject == name,
			ProjectID:   entry.ProjectID,
			ProjectName: entry.ProjectName,
			ProjectSlug: entry.ProjectSlug,
		})
	}
	if globals.json || !a.outputIsTerminal() {
		return writeJSON(a.out, map[string]any{"data": rows, "meta": map[string]any{"total": len(rows)}})
	}
	if len(rows) == 0 {
		fmt.Fprintln(a.out, "No projects configured. Run updog login.")
		return nil
	}
	tw := tabwriter.NewWriter(a.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "CURRENT\tPROFILE\tUPDOG PROJECT\tURL")
	for _, item := range rows {
		current := ""
		if item.Current {
			current = "*"
		}
		serverProject := item.ProjectName
		if serverProject == "" {
			serverProject = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", current, item.Name, serverProject, item.URL)
	}
	return tw.Flush()
}

func (a *app) projectsUse(globals globalOptions, name string) error {
	if err := validateProjectName(name); err != nil {
		return usageError(err.Error())
	}
	cfg, err := loadConfig(a.configPath)
	if err != nil {
		return configError(err.Error())
	}
	if _, ok := cfg.Projects[name]; !ok {
		return configError(fmt.Sprintf("project %q is not configured", name))
	}
	cfg.CurrentProject = name
	if err := saveConfig(a.configPath, cfg); err != nil {
		return configError(err.Error())
	}
	if globals.json || !a.outputIsTerminal() {
		return writeJSON(a.out, map[string]any{"data": map[string]any{"current_project": name}})
	}
	fmt.Fprintf(a.out, "Current project is now %q.\n", name)
	return nil
}

func (a *app) logs(globals globalOptions, args []string) error {
	if len(args) > 0 && args[0] == "search" {
		args = args[1:]
	}
	if hasHelp(args) {
		a.printLogsHelp()
		return nil
	}
	fs := newFlagSet("logs search")
	query := fs.String("query", "", "search query")
	level := fs.String("level", "", "log level")
	hostname := fs.String("hostname", "", "hostname")
	traceID := fs.String("trace-id", "", "trace ID")
	since := fs.String("since", "", "start time")
	until := fs.String("until", "", "end time")
	sortBy := fs.String("sort-by", "", "sort field")
	sortDir := fs.String("sort-dir", "", "sort direction")
	var limit, offset optionalInt
	fs.Var(&limit, "limit", "result limit")
	fs.Var(&offset, "offset", "result offset")
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if fs.NArg() != 0 {
		return usageError("logs search does not accept positional arguments")
	}
	values := visitedValues(fs, map[string]queryValue{
		"query": {"q", *query}, "level": {"level", *level}, "hostname": {"hostname", *hostname},
		"trace-id": {"trace_id", *traceID}, "since": {"since", *since}, "until": {"until", *until},
		"sort-by": {"sort_by", *sortBy}, "sort-dir": {"sort_dir", *sortDir},
		"limit": {"limit", limit.String()}, "offset": {"offset", offset.String()},
	})
	return a.getAndRender(globals, "/api/v1/logs", values, "logs")
}

func (a *app) errors(globals globalOptions, args []string) error {
	if len(args) == 0 || args[0] == "search" {
		if len(args) > 0 {
			args = args[1:]
		}
		return a.errorsSearch(globals, args)
	}
	if args[0] == "show" {
		return a.errorShow(globals, args[1:])
	}
	if args[0] == "-h" || args[0] == "--help" {
		a.printErrorsHelp()
		return nil
	}
	return usageError("unknown errors command: " + args[0])
}

func (a *app) errorsSearch(globals globalOptions, args []string) error {
	if hasHelp(args) {
		a.printErrorsSearchHelp()
		return nil
	}
	fs := newFlagSet("errors search")
	query := fs.String("query", "", "search query")
	status := fs.String("status", "", "error status")
	since := fs.String("since", "", "start time")
	until := fs.String("until", "", "end time")
	var limit, offset optionalInt
	fs.Var(&limit, "limit", "result limit")
	fs.Var(&offset, "offset", "result offset")
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if fs.NArg() != 0 {
		return usageError("errors search does not accept positional arguments")
	}
	values := visitedValues(fs, map[string]queryValue{
		"query": {"q", *query}, "status": {"status", *status}, "since": {"since", *since},
		"until": {"until", *until}, "limit": {"limit", limit.String()}, "offset": {"offset", offset.String()},
	})
	return a.getAndRender(globals, "/api/v1/errors", values, "errors")
}

func (a *app) errorShow(globals globalOptions, args []string) error {
	if hasHelp(args) {
		a.printErrorsShowHelp()
		return nil
	}
	if len(args) == 0 {
		return usageError("errors show requires an error ID")
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || id <= 0 {
		return usageError("error ID must be a positive integer")
	}
	fs := newFlagSet("errors show")
	since := fs.String("since", "", "start time")
	until := fs.String("until", "", "end time")
	var limit, offset optionalInt
	fs.Var(&limit, "limit", "occurrence limit")
	fs.Var(&offset, "offset", "occurrence offset")
	if err := fs.Parse(args[1:]); err != nil {
		return usageError(err.Error())
	}
	if fs.NArg() != 0 {
		return usageError("errors show accepts one error ID")
	}
	values := visitedValues(fs, map[string]queryValue{
		"since": {"since", *since}, "until": {"until", *until},
		"limit": {"limit", limit.String()}, "offset": {"offset", offset.String()},
	})
	return a.getAndRender(globals, "/api/v1/errors/"+strconv.FormatInt(id, 10), values, "error")
}

type resolvedAuth struct {
	project string
	baseURL string
	apiKey  string
	source  string
}

func (a *app) resolveAuth(requestedProject string) (resolvedAuth, error) {
	if rawKey := a.getenv("UPDOG_API_KEY"); rawKey != "" {
		if requestedProject != "" {
			return resolvedAuth{}, configError("--project cannot be combined with UPDOG_API_KEY")
		}
		apiKey, err := validateAPIKey(rawKey)
		if err != nil {
			return resolvedAuth{}, configError(err.Error())
		}
		baseURL, err := normalizeBaseURL(a.getenv("UPDOG_URL"))
		if err != nil {
			return resolvedAuth{}, configError(err.Error())
		}
		return resolvedAuth{baseURL: baseURL, apiKey: apiKey, source: "environment"}, nil
	}

	cfg, err := loadConfig(a.configPath)
	if err != nil {
		return resolvedAuth{}, configError(err.Error())
	}
	name := requestedProject
	if name == "" {
		name = cfg.CurrentProject
	}
	if name == "" {
		return resolvedAuth{}, configError("no authentication configured; run updog login or set UPDOG_API_KEY")
	}
	entry, ok := cfg.Projects[name]
	if !ok {
		return resolvedAuth{}, configError(fmt.Sprintf("project %q is not configured; run updog projects list", name))
	}
	baseURL, err := normalizeBaseURL(entry.URL)
	if err != nil {
		return resolvedAuth{}, configError(fmt.Sprintf("stored URL for project %q is invalid: %v; run updog login again", name, err))
	}
	apiKey, err := a.secrets.Get(entry.CredentialID)
	if errors.Is(err, errSecretNotFound) {
		return resolvedAuth{}, configError(fmt.Sprintf("credential for project %q is missing; run updog login --project %s", name, name))
	}
	if err != nil {
		return resolvedAuth{}, configError(err.Error())
	}
	apiKey, err = validateAPIKey(apiKey)
	if err != nil {
		return resolvedAuth{}, configError("stored credential is invalid; run updog login again")
	}
	return resolvedAuth{project: name, baseURL: baseURL, apiKey: apiKey, source: "os_keyring"}, nil
}

func configPersistenceError(primary, rollback error) error {
	if rollback != nil {
		return configError(fmt.Sprintf("%v; credential rollback also failed: %v", primary, rollback))
	}
	return configError(primary.Error())
}

func (a *app) getAndRender(globals globalOptions, path string, query url.Values, kind string) error {
	auth, err := a.resolveAuth(globals.project)
	if err != nil {
		return err
	}
	client := apiClient{baseURL: auth.baseURL, apiKey: auth.apiKey, version: a.version, httpClient: a.httpClient}
	body, err := client.get(a.context, path, query)
	if err != nil {
		return a.apiCommandError("request failed", err)
	}
	if err := renderAPIResponse(a.out, kind, body, globals.json || !a.outputIsTerminal()); err != nil {
		return &commandError{code: 1, message: err.Error()}
	}
	return nil
}

func (a *app) apiCommandError(prefix string, err error) error {
	var responseErr *apiError
	if errors.As(err, &responseErr) {
		return &commandError{
			code:               1,
			message:            responseErr.Error(),
			body:               responseErr.Body,
			retryAfter:         responseErr.RetryAfter,
			rateLimitLimit:     responseErr.RateLimitLimit,
			rateLimitRemaining: responseErr.RateLimitRemaining,
			rateLimitReset:     responseErr.RateLimitReset,
		}
	}
	return &commandError{code: 1, message: prefix + ": " + err.Error()}
}

func (a *app) promptLine(label, defaultValue string) (string, error) {
	fmt.Fprintf(a.err, "%s [%s]: ", label, defaultValue)
	line, err := a.reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		line = defaultValue
	}
	return line, nil
}

func writeJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func hasHelp(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}

type optionalInt struct {
	set   bool
	value int
}

func (value *optionalInt) Set(raw string) error {
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return fmt.Errorf("must be an integer")
	}
	value.set = true
	value.value = parsed
	return nil
}

func (value *optionalInt) String() string {
	if !value.set {
		return ""
	}
	return strconv.Itoa(value.value)
}

type queryValue struct {
	name  string
	value string
}

func visitedValues(fs *flag.FlagSet, mappings map[string]queryValue) url.Values {
	values := url.Values{}
	fs.Visit(func(item *flag.Flag) {
		mapping, ok := mappings[item.Name]
		if ok {
			values.Set(mapping.name, mapping.value)
		}
	})
	return values
}

func (a *app) printHelp() {
	fmt.Fprintln(a.out, `updog - search Updog logs and errors

Usage:
  updog login [--project NAME] [--url URL]
  updog logout [--project NAME]
  updog projects list
  updog projects use NAME
  updog [--project NAME] logs search [options]
  updog [--project NAME] errors search [options]
  updog [--project NAME] errors show ID [options]
  updog auth status
  updog version

Global options:
  --project NAME  Use a configured project
  --json          Force compact JSON output

Authentication:
  Login displays a URL and code for approving one read-only project.
  The resulting key is stored in the OS keyring.
  UPDOG_API_KEY overrides stored credentials for CI and automation.
  UPDOG_URL changes the server URL for environment-key authentication.

When stdout is not a terminal, command results are JSON automatically.`)
}

func (a *app) printLoginHelp() {
	fmt.Fprintln(a.out, `Usage: updog login [--project NAME] [--url URL]
       updog login --manual [--project NAME] [--url URL]
       updog login --token-stdin --project NAME [--url URL]

The default flow displays a URL and short code. Sign in through the browser,
choose one project, and approve read-only logs and errors access. The CLI then
stores the issued key in the operating system keyring.

--project sets a local alias; otherwise the server project slug is used.
--manual prompts for an existing read-only key. --token-stdin reads one key
from standard input and requires --project. Keys are never accepted as
command-line values.`)
}

func (a *app) printProjectsHelp() {
	fmt.Fprintln(a.out, `Usage:
  updog projects list
  updog projects use NAME

Projects are local profiles backed by separately authorized, project-scoped
read keys. Run updog login again to authorize another project.`)
}

func (a *app) printLogsHelp() {
	fmt.Fprintln(a.out, `Usage: updog [--project NAME] logs search [options]

Options:
  --query VALUE       Search log messages and metadata
  --level VALUE       Filter by log level
  --hostname VALUE    Filter by hostname
  --trace-id VALUE    Filter by trace ID
  --since VALUE       Relative duration, RFC3339 timestamp, or all
  --until VALUE       RFC3339 timestamp
  --sort-by VALUE     logged_at, level, hostname, or trace_id
  --sort-dir VALUE    asc or desc
  --limit VALUE       Results per page (maximum 200)
  --offset VALUE      Result offset (maximum 10000)`)
}

func (a *app) printErrorsHelp() {
	fmt.Fprintln(a.out, `Usage:
  updog [--project NAME] errors search [options]
  updog [--project NAME] errors show ID [options]`)
}

func (a *app) printErrorsSearchHelp() {
	fmt.Fprintln(a.out, `Usage: updog [--project NAME] errors search [options]

Options:
  --query VALUE       Search class, message, and fingerprint
  --status VALUE      unresolved, resolved, or ignored
  --since VALUE       Relative duration, RFC3339 timestamp, or all
  --until VALUE       RFC3339 timestamp
  --limit VALUE       Results per page (maximum 200)
  --offset VALUE      Result offset (maximum 10000)`)
}

func (a *app) printErrorsShowHelp() {
	fmt.Fprintln(a.out, `Usage: updog [--project NAME] errors show ID [options]

Options:
  --since VALUE       Relative duration, RFC3339 timestamp, or all
  --until VALUE       RFC3339 timestamp
  --limit VALUE       Occurrences per page (maximum 100)
  --offset VALUE      Occurrence offset (maximum 10000)`)
}
