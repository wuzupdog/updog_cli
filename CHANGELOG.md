# Changelog

## 0.8.0

- Show each host's reporting IP address in `hosts list` (new `IP ADDRESS` column) and `hosts show`. JSON output passes the server's additive `ip_address` field through unchanged.
- The address is the one the host's agent last reported from, as seen by Updog, and is frequently IPv6. Hosts without a recorded address show `—`.
- Requires the server host-address deployment for addresses to appear; older servers omit the field and the CLI shows `—`. No agent or ingestion-client release is required. Tag only after merge and coordinated rollout approval.

## 0.7.1

- Document read-only CLI login and manual key creation for regular project members.
- Permission checks remain server-side; device requests, scopes, responses, and existing CLI compatibility are unchanged.
- Deploy the server member-access update before publishing this documentation release. Existing CLI versions gain member access without an upgrade. Tag only after merge and coordinated rollout approval.

## 0.7.0

- Select one or more projects during plain `updog login` and store one credential granting exactly that selection.
- Automatically discover and query the projects granted to a multi-project key, including imported and environment keys. Keep `--all-projects` for querying all saved profiles or an environment key's grants.
- Show granted projects in login and profile output; preserve single-project output and older server login compatibility.
- Requires the additive server project-grant and discovery API deployment before release. Existing tokens remain valid; log in again to issue one key for several projects.

## 0.6.0

- Query multiple saved profiles with repeated `--project NAME` or `--all-projects` on all telemetry commands.
- Use up to four concurrent requests with separate project credentials, stable output order, and project-specific pagination.
- Preserve successful results alongside labeled failures and rate-limit details, returning a nonzero exit status for incomplete results.
- Preserve single-project output and existing device authorization and read-API contracts.
- Release after merge; no coordinated server deployment or ingestion-client release is required.

## 0.5.0

- Add `hosts list` and `hosts show HOSTNAME` for Updog Agent host snapshots.
- Grant new device logins project-scoped `hosts:read` access.

## 0.4.0

- Surface shared read-API rate-limit limits, remaining capacity, reset time, and retry delay on API failures.
- Document that read-only log and error searches are rate-limited per project key and scope across Updog server nodes.
