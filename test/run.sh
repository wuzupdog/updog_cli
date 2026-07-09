#!/bin/sh

TEST_ROOT=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
TEST_TMP=${TMPDIR:-/tmp}/updog-cli-test.$$
TEST_COUNT=0
TEST_FAILURES=0

mkdir -p "$TEST_TMP"
trap 'rm -rf "$TEST_TMP"' EXIT HUP INT TERM

export PATH="$TEST_ROOT/test/fixtures/bin:$PATH"
export FAKE_CURL_ARGS_FILE="$TEST_TMP/curl-args"
export FAKE_CURL_CONFIG_FILE="$TEST_TMP/curl-config"
export UPDOG_URL=https://example.test/updog/

run_cli() {
  TEST_STDOUT=$("$TEST_ROOT/updog" "$@" 2>"$TEST_TMP/stderr")
  TEST_STATUS=$?
  TEST_STDERR=$(cat "$TEST_TMP/stderr")
}

pass() {
  TEST_COUNT=$((TEST_COUNT + 1))
  printf 'ok %s - %s\n' "$TEST_COUNT" "$1"
}

fail() {
  TEST_COUNT=$((TEST_COUNT + 1))
  TEST_FAILURES=$((TEST_FAILURES + 1))
  printf 'not ok %s - %s\n' "$TEST_COUNT" "$1"
  printf '  %s\n' "$2"
}

assert_status() {
  test_name=$1
  expected=$2
  if [ "$TEST_STATUS" -eq "$expected" ]; then
    pass "$test_name"
  else
    fail "$test_name" "expected status $expected, got $TEST_STATUS; stderr: $TEST_STDERR"
  fi
}

assert_stdout() {
  test_name=$1
  expected=$2
  if [ "$TEST_STDOUT" = "$expected" ]; then
    pass "$test_name"
  else
    fail "$test_name" "unexpected stdout: $TEST_STDOUT"
  fi
}

assert_contains() {
  test_name=$1
  haystack=$2
  needle=$3
  case "$haystack" in
    *"$needle"*) pass "$test_name" ;;
    *) fail "$test_name" "expected to find '$needle' in: $haystack" ;;
  esac
}

assert_file_line() {
  test_name=$1
  file=$2
  expected=$3
  if grep -F -x -- "$expected" "$file" >/dev/null 2>&1; then
    pass "$test_name"
  else
    fail "$test_name" "expected exact line '$expected' in $file"
  fi
}

assert_file_excludes() {
  test_name=$1
  file=$2
  unexpected=$3
  if grep -F -- "$unexpected" "$file" >/dev/null 2>&1; then
    fail "$test_name" "found secret in $file"
  else
    pass "$test_name"
  fi
}

unset UPDOG_API_KEY
run_cli version
assert_status "version succeeds without configuration" 0
assert_stdout "version reports the release version" "updog 0.1.0"

run_cli help
assert_status "help succeeds without configuration" 0
assert_contains "help lists log search" "$TEST_STDOUT" "updog logs search"

run_cli errors show --help
assert_status "error detail help does not require an ID or configuration" 0
assert_contains "error detail help documents the ID" "$TEST_STDOUT" "errors show ID"

run_cli logs search
assert_status "missing API key is a configuration error" 2
assert_contains "missing API key explains the problem" "$TEST_STDERR" "UPDOG_API_KEY is required"

export UPDOG_API_KEY=updog_test-secret_123
export FAKE_CURL_BODY='{"data":[],"meta":{"total":0}}'
export FAKE_CURL_STATUS=0
unset FAKE_CURL_STDERR

run_cli logs search \
  --query 'space & slash/plus+' \
  --level error \
  --hostname=worker-1 \
  --trace-id trace/123 \
  --since 30m \
  --until 2026-07-09T14:00:00Z \
  --sort-by logged_at \
  --sort-dir=desc \
  --limit 25 \
  --offset=50
assert_status "log search succeeds" 0
assert_stdout "successful API JSON is written to stdout" '{"data":[],"meta":{"total":0}}'
assert_file_line "log search uses GET" "$FAKE_CURL_ARGS_FILE" "--get"
assert_file_line "log search targets the configured base URL" "$FAKE_CURL_ARGS_FILE" "https://example.test/updog/api/v1/logs"
assert_file_line "query is delegated to curl URL encoding" "$FAKE_CURL_ARGS_FILE" "q=space & slash/plus+"
assert_file_line "trace ID uses the API parameter name" "$FAKE_CURL_ARGS_FILE" "trace_id=trace/123"
assert_file_line "sort field is sent" "$FAKE_CURL_ARGS_FILE" "sort_by=logged_at"
assert_file_line "limit is sent" "$FAKE_CURL_ARGS_FILE" "limit=25"
assert_file_excludes "API key is absent from curl argv" "$FAKE_CURL_ARGS_FILE" "$UPDOG_API_KEY"
curl_config=$(cat "$FAKE_CURL_CONFIG_FILE")
assert_contains "API key is supplied through curl stdin config" "$curl_config" "x-api-key: $UPDOG_API_KEY"

run_cli errors search \
  --query='ArgumentError % value' \
  --status unresolved \
  --since=all \
  --until 2026-07-09T15:00:00Z \
  --limit=100 \
  --offset 200
assert_status "error search succeeds" 0
assert_file_line "error search targets its endpoint" "$FAKE_CURL_ARGS_FILE" "https://example.test/updog/api/v1/errors"
assert_file_line "error query is delegated to curl URL encoding" "$FAKE_CURL_ARGS_FILE" "q=ArgumentError % value"
assert_file_line "error status is sent" "$FAKE_CURL_ARGS_FILE" "status=unresolved"
assert_file_line "all-time search is sent" "$FAKE_CURL_ARGS_FILE" "since=all"

run_cli errors show 42 \
  --since 24h \
  --until=2026-07-09T16:00:00Z \
  --limit 20 \
  --offset=40
assert_status "error detail succeeds" 0
assert_file_line "error detail includes its ID in the path" "$FAKE_CURL_ARGS_FILE" "https://example.test/updog/api/v1/errors/42"
assert_file_line "occurrence offset is sent" "$FAKE_CURL_ARGS_FILE" "offset=40"

run_cli errors show 0
assert_status "zero is not a valid error ID" 2
assert_contains "error IDs must be positive integers" "$TEST_STDERR" "positive integer"

run_cli logs search --wat
assert_status "unknown options are usage errors" 2
assert_contains "unknown option is identified" "$TEST_STDERR" "unknown logs search option: --wat"

run_cli logs search --api-key should-never-be-accepted
assert_status "API keys are not accepted as command-line options" 2
assert_contains "command-line API key option is rejected" "$TEST_STDERR" "unknown logs search option: --api-key"

run_cli errors show
assert_status "missing error ID is a usage error" 2
assert_contains "missing error ID is identified" "$TEST_STDERR" "requires an error ID"

export FAKE_CURL_BODY='{"error":"API key is not allowed to access this endpoint"}'
export FAKE_CURL_STATUS=22
run_cli errors search
assert_status "HTTP failures return a nonzero status" 1
assert_stdout "HTTP failure body is not written to stdout" ""
assert_contains "HTTP failure body is written to stderr" "$TEST_STDERR" '"error":"API key is not allowed'

export FAKE_CURL_BODY=
export FAKE_CURL_STATUS=6
export FAKE_CURL_STDERR='curl: (6) Could not resolve host: example.test'
run_cli logs search
assert_status "network failures return a nonzero status" 1
assert_stdout "network failures do not write to stdout" ""
assert_contains "curl network diagnostic remains on stderr" "$TEST_STDERR" "Could not resolve host"

export FAKE_CURL_STATUS=0
unset FAKE_CURL_STDERR
export UPDOG_API_KEY='unsafe"key'
run_cli logs search
assert_status "unsafe API key values are configuration errors" 2
assert_contains "unsafe API key is rejected before curl config construction" "$TEST_STDERR" "contains invalid characters"

if [ "$TEST_FAILURES" -ne 0 ]; then
  printf '%s of %s assertions failed\n' "$TEST_FAILURES" "$TEST_COUNT" >&2
  exit 1
fi

printf '1..%s\n' "$TEST_COUNT"
