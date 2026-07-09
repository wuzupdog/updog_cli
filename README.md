# Updog CLI

A small, read-only CLI for searching [Updog](https://wuzupdog.com) logs and
errors. It is a single POSIX shell script and requires only `curl` at runtime.

## Install

Download a release rather than piping a remote script into your shell:

```sh
version=v0.1.0
curl -fsSLO "https://github.com/wuzupdog/updog_cli/releases/download/$version/updog"
curl -fsSLO "https://github.com/wuzupdog/updog_cli/releases/download/$version/SHA256SUMS"
sha256sum -c SHA256SUMS # macOS: shasum -a 256 -c SHA256SUMS
chmod +x updog
mkdir -p "$HOME/.local/bin"
mv updog "$HOME/.local/bin/updog"
```

Make sure `$HOME/.local/bin` is on your `PATH`, then confirm the installation:

```sh
updog version
```

## Configure

Create a **Read-only** key on the Updog project page. The secret is shown
once, so place it in your agent or shell environment when you need it:

```sh
export UPDOG_API_KEY='updog_...'
```

The CLI uses `https://wuzupdog.com` by default. Set `UPDOG_URL` for another
Updog deployment, including local development:

```sh
export UPDOG_URL='http://localhost:4000'
```

## Use

Search recent logs:

```sh
updog logs search --query 'checkout failed' --level error --since 30m
updog logs search --hostname worker-1 --trace-id abc123 --limit 100
```

Search error groups and inspect their occurrences:

```sh
updog errors search --query ArgumentError --status unresolved --since 7d
updog errors show 42 --since 24h --limit 50
```

Successful commands write the API's compact JSON response to stdout, making it
easy for an agent or another program to consume. API and network failures write
to stderr and exit nonzero. Usage and configuration errors exit with status 2.

### Options

`logs search` supports:

- `--query`, `--level`, `--hostname`, and `--trace-id`
- `--since` and `--until`
- `--sort-by` and `--sort-dir`
- `--limit` and `--offset`

`errors search` supports `--query`, `--status`, `--since`, `--until`, `--limit`,
and `--offset`. `errors show ID` supports the time and pagination options. Use
`updog COMMAND --help` for the complete command-specific help.

`--since` accepts a relative duration such as `30m`, `6h`, or `7d`, an RFC3339
timestamp, or `all`. `--until` accepts an RFC3339 timestamp. Limits and time
windows are validated by the Updog API.

## Security

- The API key is accepted only through `UPDOG_API_KEY`. There is no command-line
  key option and the CLI never writes the key to disk.
- The key header is passed to `curl` with a configuration on stdin, so the
  secret does not appear in `curl`'s process arguments.
- Use a read-only key, rotate or revoke it when exposure is suspected, and
  point `UPDOG_URL` only at a server you trust. Read access includes the full
  telemetry stored for the project.
- Environment variables can be inherited by child processes and may be visible
  to sufficiently privileged local users. Keep the key in the narrowest-lived
  environment your agent supports.

## Develop

Run the POSIX syntax checks and dependency-free test harness:

```sh
sh -n updog test/run.sh test/fixtures/bin/curl
sh test/run.sh
```

The tests use a fake `curl` to verify parsing, URL-encoding arguments, error
handling, and that the API key never appears in `curl`'s argument vector.

## License

[MIT](LICENSE)
