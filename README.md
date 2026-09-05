# Updog CLI

A read-only CLI for inspecting [Updog](https://wuzupdog.com) hosts and searching logs and errors.
It is designed for humans and coding agents: terminals get readable tables,
while redirected output is compact JSON.

## Install

Download the archive for your platform from the
[latest release](https://github.com/wuzupdog/updog_cli/releases/latest), verify
it against `SHA256SUMS`, and place `updog` somewhere on your `PATH`.

For Apple silicon:

```sh
version=v0.7.0
archive="updog_${version#v}_darwin_arm64.tar.gz"
curl -fsSLO "https://github.com/wuzupdog/updog_cli/releases/download/$version/$archive"
curl -fsSLO "https://github.com/wuzupdog/updog_cli/releases/download/$version/SHA256SUMS"
grep " ./$archive\$" SHA256SUMS | shasum -a 256 -c -
tar -xzf "$archive"
install -m 0755 updog "$HOME/.local/bin/updog"
```

The releases include macOS and Linux binaries for amd64/arm64 and Windows
binaries for amd64/arm64. Developers with Go installed can instead run:

```sh
go install github.com/wuzupdog/updog_cli/cmd/updog@v0.7.0
```

Confirm the installation:

```sh
updog version
```

## Log in

Run:

```sh
updog login
```

The CLI prints an Updog URL and a short code. Open the URL, sign in, enter the
code, select one or more projects, and approve read-only access to their hosts, logs, and errors.
The CLI waits for approval, receives one key granting exactly those projects, and stores it in the
operating system credential store. The configuration file contains only safe
project metadata and a credential reference. Nothing needs to be added to
`.bashrc`, `.zshrc`, or the repository.

Only project owners and admins can approve a CLI login for that project.

No project flags are needed at login. A single selection uses the project slug
as its local profile name; multiple selections use `default`. Optionally,
`--project` sets a local credential alias and never changes its permissions:

```sh
updog login --project mnm-production
```

For a self-hosted or local Updog server:

```sh
updog login --project mnm --url https://app.updog-devcontainer.orb.local
```

HTTPS is required for remote servers. Plain HTTP is accepted only for loopback
addresses so device codes and API keys are never sent over a network in clear
text.

Each login stores one credential profile, which may grant one or more projects.
To change its access, run `updog login` again and approve the desired projects.
Older keys still work; reissue a credential to combine projects under one key.
Server support is required for multi-selection; older servers retain their
single-project flow.

Manage profiles with:

```sh
updog projects list
updog projects use mnm
updog auth status
updog logout --project mnm
```

## Search telemetry

The current credential is used by default. Commands automatically query every
project granted to that key:

```sh
updog logs search --query 'checkout failed' --level error --since 30m
updog errors search --status unresolved --since 7d
updog errors show 42 --since 24h --limit 50
updog hosts list
updog hosts show zone-1
```

Select a saved credential profile explicitly when an agent should not depend
on local default state (a profile can grant multiple projects):

```sh
updog --project mnm logs search --hostname worker-1 --limit 100
updog --project mnm errors search --query ArgumentError
updog --project mnm hosts show worker-1
```

### Query multiple projects

CLI 0.7.0 lets you approve multiple projects with one browser login:

```sh
updog login
# Select one or more projects in the browser.
updog logs search --query timeout --since 1h
updog --all-projects hosts list
```

The server's key grants determine access. CLI discovery retrieves the allowed
projects and sends `X-Updog-Project-ID` for each query. A header or local profile
cannot add permissions. Project IDs appear alongside project slugs in grouped
JSON output.

Existing separate profiles still work: repeat `--project NAME` to query selected
profiles, or use `--all-projects` to query every saved profile and its grants.
Duplicate profile names are queried once; profiles keep explicit order or are
sorted by name with `--all-projects`. Granted projects are ordered by server ID.
These selectors do not change the current profile. Login and logout operate on
one credential profile. `--all-projects` cannot be combined with `--project`.
With `UPDOG_API_KEY`, plain queries and `--all-projects` query that key's grants;
`--project` remains incompatible with environment authentication.

Up to four requests run concurrently using each profile's own server and key.
Filters, sorting, `--limit`, and `--offset` apply independently to every project;
results are grouped, not merged into one globally sorted page. Each response
retains its own window and pagination metadata. For `errors show ID` or
`hosts show HOSTNAME`, the same ID or hostname is looked up in each selected
project; use one profile when you want one specific project's detail.

Terminals show a labeled section for each profile. JSON wraps the original API
response under `response` without changing its fields:

```json
{
  "data": [
    {
      "project": "mnm",
      "url": "https://wuzupdog.com",
      "response": {"data": [], "meta": {"pagination": {"total": 0}}}
    }
  ],
  "meta": {"projects": 1, "succeeded": 1, "failed": 0}
}
```

`--all-projects` always uses this envelope, even with one configured profile.
Single-project credentials keep their existing output. Credentials with multiple
projects use this envelope even without flags. Failed profiles have an
`error` instead of `response`, containing `message`, `exit_code`, and any JSON
API `body` and rate-limit headers (`retry_after`, `rate_limit_limit`,
`rate_limit_remaining`, `rate_limit_reset`). Successful results remain available.
The command exits `1` for request failures, or `2` if any profile has a local
configuration error. JSON includes each failure and stderr reports the failure
count; terminal output also prints each project's error and retry headers.

`logs search` supports `--query`, `--level`, `--hostname`, `--trace-id`,
`--since`, `--until`, `--sort-by`, `--sort-dir`, `--limit`, and `--offset`.
`errors search` supports `--query`, `--status`, `--since`, `--until`, `--limit`,
and `--offset`. `errors show` supports the time and pagination options.
`hosts list` returns every machine discovered during the last 30 days, with
measurements sampled from the last ten minutes. `hosts show HOSTNAME` displays
the full snapshot for one machine, including its current top processes by CPU
and memory.

### Output

- Interactive terminals receive compact tables.
- Redirected and agent output is compact JSON automatically.
- `--json` forces JSON even in a terminal.
- API and network errors go to stderr and exit `1`.
- Usage and local configuration errors exit `2`.
- Rate-limited responses print `Retry-After` plus the server's shared `RateLimit-Limit`, `RateLimit-Remaining`, and `RateLimit-Reset` values to stderr.

This makes agent calls predictable:

```sh
updog --project mnm --json logs search --level error --since 30m
```

## CI and noninteractive agents

Environment authentication remains available and takes precedence over stored
profiles:

```sh
UPDOG_API_KEY='updog_...' updog logs search --since 30m
```

Set `UPDOG_URL` when using environment authentication against another Updog
deployment. Do not pass secrets as command-line flags or commit them to a
repository.

`UPDOG_API_KEY` and `--project` are intentionally mutually exclusive: an
environment key has no trustworthy local profile mapping, so combining them
could make a command appear to target the wrong project.

Existing read-only keys can also be imported without a browser. This is an
explicit fallback, not the normal interactive login flow:

```sh
printf '%s\n' "$UPDOG_API_KEY" | updog login --token-stdin --project mnm
```

`updog login --manual --project mnm` securely prompts for an existing key in a
terminal. Neither fallback accepts a key as a command-line value.

For a repository agent, install the binary once on the host and add guidance,
not credentials, to `AGENTS.md`:

```md
Use `updog --project mnm logs search` and
`updog --project mnm errors search` when diagnosing production problems.
Use `updog --project mnm hosts list` for current Linux host health.
Updog access is read-only. Run these commands on the host.
```

## Security

- Interactive credentials are stored through the operating system credential
  manager (macOS Keychain, Windows Credential Manager, or Linux Secret Service).
- Device login grants only `hosts:read`, `logs:read`, and `errors:read` for exactly the projects
  selected during browser approval (up to 100).
- Project metadata is stored in the user configuration directory with mode
  `0600` on Unix systems and never contains the API key.
- CI can provide `UPDOG_API_KEY` without persisting it.
- Read access returns full telemetry for the authorized projects. The same key
  appears in each selected project's API Keys section. Revoking it from any
  selected project revokes the whole credential. Older CLIs continue to receive
  single-project approvals. Ingestion keys remain single-project.

## Develop

This project uses Go 1.25:

```sh
gofmt -w cmd internal
go vet ./...
go test -race ./...
go build ./cmd/updog
```

Build all release archives locally:

```sh
./scripts/build-release.sh v0.7.0
```

## License

[MIT](LICENSE)
