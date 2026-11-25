#!/usr/bin/env bash
# The SDK has no dependencies besides the Go standard library: plugins would inherit them
# and their licenses. A new dependency must be a decision in a review, not an accident.
set -euo pipefail

cd "$(dirname "$0")/.."
deps="$(go list -m -f '{{if not .Main}}{{.Path}} {{.Version}}{{end}}' all)"
if [[ -n "$deps" ]]; then
	echo "ERROR: the SDK must not have dependencies (plugins would inherit them and their licenses):" >&2
	echo "$deps" >&2
	exit 1
fi
echo "no dependencies"
