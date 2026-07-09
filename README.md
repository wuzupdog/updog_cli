# Updog CLI

A read-only CLI for searching [Updog](https://wuzupdog.com) logs and errors.
It is designed for humans and coding agents: terminals get readable tables,
while redirected output is compact JSON.

## Install

Download the archive for your platform from the
[latest release](https://github.com/wuzupdog/updog_cli/releases/latest), verify
it against `SHA256SUMS`, and place `updog` somewhere on your `PATH`.

For Apple silicon:

```sh
version=v0.2.0
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
go install github.com/wuzupdog/updog_cli/cmd/updog@v0.2.0
```

Confirm the installation:

```sh
updog version
```

## Log in

Create a **Read-only** key on the Updog project page, then run:

```sh
updog login --project mnm
```

Paste the key at the hidden prompt. The CLI validates it and stores it in the
operating system credential store; the configuration file contains only the
project name, Updog URL, and a credential reference. Nothing needs to be added
to `.bashrc`, `.zshrc`, or the repository.

For a self-hosted or local Updog server:

```sh
updog login --project mnm --url http://localhost:4000
```

Each login authorizes one local project profile because Updog read keys are
project-scoped. Add another project by running `updog login --project NAME`
again.

Manage profiles with:

```sh
updog projects list
updog projects use mnm
updog auth status
updog logout --project mnm
```

## Search telemetry

The current project is used by default:

```sh
updog logs search --query 'checkout failed' --level error --since 30m
updog errors search --status unresolved --since 7d
updog errors show 42 --since 24h --limit 50
```

Select a project explicitly when an agent should not depend on local default
state:

```sh
updog --project mnm logs search --hostname worker-1 --limit 100
updog --project mnm errors search --query ArgumentError
```

`logs search` supports `--query`, `--level`, `--hostname`, `--trace-id`,
`--since`, `--until`, `--sort-by`, `--sort-dir`, `--limit`, and `--offset`.
`errors search` supports `--query`, `--status`, `--since`, `--until`, `--limit`,
and `--offset`. `errors show` supports the time and pagination options.

### Output

- Interactive terminals receive compact tables.
- Redirected and agent output is compact JSON automatically.
- `--json` forces JSON even in a terminal.
- API and network errors go to stderr and exit `1`.
- Usage and local configuration errors exit `2`.

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

For a repository agent, install the binary once on the host and add guidance,
not credentials, to `AGENTS.md`:

```md
Use `updog --project mnm logs search` and
`updog --project mnm errors search` when diagnosing production problems.
Updog access is read-only. Run these commands on the host.
```

## Security

- Interactive credentials are stored through the operating system credential
  manager (macOS Keychain, Windows Credential Manager, or Linux Secret Service).
- Project metadata is stored in the user configuration directory with mode
  `0600` on Unix systems and never contains the API key.
- CI can provide `UPDOG_API_KEY` without persisting it.
- Read access returns full telemetry for the authorized project. Revoke or
  rotate a key if exposure is suspected.

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
./scripts/build-release.sh v0.2.0
```

## License

[MIT](LICENSE)
