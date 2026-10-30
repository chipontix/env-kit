#!/usr/bin/env bash
# For every broken/*.patch: apply it, run the test suite, and require that it
# fails and that every test named in the patch's "Expect-Fail:" lines is among
# the failures. The patch is reverted afterwards, also on error or Ctrl-C.
# Output of each run is saved to results/broken/<patch>.txt.
set -euo pipefail
cd "$(dirname "$0")/.."

out=results/broken
mkdir -p "$out"
applied=""
restore() {
	if [[ -n $applied ]]; then
		git apply -R "$applied"
		applied=""
	fi
}
trap restore EXIT INT TERM

caught=0 total=0
for patch in broken/*.patch; do
	name=$(basename "$patch" .patch)
	total=$((total + 1))
	expect=$(sed -n 's/^Expect-Fail: *//p' "$patch")
	if [[ -z $expect ]]; then
		echo "$patch: no Expect-Fail lines" >&2
		exit 2
	fi

	git apply --check "$patch"
	git apply "$patch"
	applied=$patch
	echo "=== $name: applied, running tests"

	set +e
	docker compose --progress quiet --profile test run --rm -T test \
		go test -race -count=1 -timeout 3m ./internal/... >"$out/$name.txt" 2>&1
	code=$?
	set -e
	restore

	failed=$(grep -oE -- '--- FAIL: [A-Za-z0-9_/]+' "$out/$name.txt" | sed 's/--- FAIL: //' | sort -u | tr '\n' ' ')
	missing=""
	for t in $expect; do
		grep -qE -- "--- FAIL: $t( |$)" "$out/$name.txt" || missing="$missing $t"
	done

	if [[ $code -eq 0 ]]; then
		echo "    NOT CAUGHT: suite passed with the bug applied"
	elif [[ -n $missing ]]; then
		echo "    failed, but expected tests did not:$missing"
		echo "    failed tests: $failed"
	else
		caught=$((caught + 1))
		echo "    caught. failed tests: $failed"
		grep -E '^[[:space:]]+[^[:space:]]+_test\.go:[0-9]+: ' "$out/$name.txt" | sed 's/^[[:space:]]*/    /' | head -5
	fi
done

echo "=== $caught/$total broken variants caught; tree restored"
[[ $caught -eq $total ]]
