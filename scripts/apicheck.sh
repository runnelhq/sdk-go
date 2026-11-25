#!/usr/bin/env bash
# Compares the Go API of the SDK with the last release tag (apidiff). Incompatible
# changes break plugins; they are allowed only when "## Unveröffentlicht" in CHANGELOG.md
# names them under "Inkompatibel". Without a release tag there is nothing to compare.
# In GitHub Actions the result also goes to the job summary.
set -euo pipefail

cd "$(dirname "$0")/.."
apidiff="go run golang.org/x/exp/cmd/apidiff@v0.0.0-20261124000051-d6646c509b6d"

base=$(git describe --tags --abbrev=0 --match 'v[0-9]*' 2>/dev/null || true)
if [[ -z "$base" ]]; then
	echo "no release tag yet: nothing to compare the API with"
	exit 0
fi

work=$(mktemp -d)
cleanup() {
	git worktree remove --force "$work/base" >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT
git worktree add --quiet --detach "$work/base" "$base"

report=""
for pkg in $(go list -f '{{if ne .Name "main"}}{{.ImportPath}}{{end}}' ./...); do
	export_file="$work/$(echo "$pkg" | tr '/' '_').export"
	if ! (cd "$work/base" && $apidiff -w "$export_file" "$pkg") >/dev/null 2>&1; then
		continue # a new package: nothing to break
	fi
	changes=$($apidiff -incompatible "$export_file" "$pkg")
	if [[ -n "$changes" ]]; then
		report+="$pkg"$'\n'"$changes"$'\n'
	fi
done
# Packages of the release that are gone.
for pkg in $(cd "$work/base" && go list -f '{{if ne .Name "main"}}{{.ImportPath}}{{end}}' ./...); do
	if ! go list "$pkg" >/dev/null 2>&1; then
		report+="$pkg"$'\n'"- package removed"$'\n'
	fi
done

if [[ -z "$report" ]]; then
	echo "API compatible with $base"
	exit 0
fi

unreleased=$(awk '/^## /{if (seen) exit; if ($0 ~ /^## Unveröffentlicht/) seen=1} seen' CHANGELOG.md)
summary="Incompatible changes of the API since $base:"$'\n\n'"$report"
if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
	printf '### API\n\n```\n%s```\n' "$summary" >>"$GITHUB_STEP_SUMMARY"
fi
echo "$summary"
if grep -q "Inkompatibel" <<<"$unreleased"; then
	echo "CHANGELOG.md names them under \"Inkompatibel\": allowed."
	exit 0
fi
echo "ERROR: incompatible changes break plugins. If they are intended, name them in CHANGELOG.md" >&2
echo "under \"## Unveröffentlicht\" with \"Inkompatibel\" (and raise the minor version while below v1)." >&2
exit 1
