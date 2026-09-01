# Changelog

## 0.5.0

- Add `hosts list` and `hosts show HOSTNAME` for Updog Agent host snapshots.
- Grant new device logins project-scoped `hosts:read` access.

## 0.4.0

- Surface shared read-API rate-limit limits, remaining capacity, reset time, and retry delay on API failures.
- Document that read-only log and error searches are rate-limited per project key and scope across Updog server nodes.
