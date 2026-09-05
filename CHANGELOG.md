# Changelog

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
