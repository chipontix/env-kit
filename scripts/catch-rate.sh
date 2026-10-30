#!/usr/bin/env bash
# How reliable is the detector? Builds the test binary once per variant and
# runs it N times: on the clean tree every run must pass (flake rate), on each
# broken patch its Expect-Fail tests must fail (catch rate). Some of the bugs
# are races, so one red run proves little; N runs give a rate.
# Usage: scripts/catch-rate.sh [N]   Output: results/catch-rate.txt
set -euo pipefail
cd "$(dirname "$0")/.."
n=${1:-20}
report=results/catch-rate.txt
mkdir -p results

applied=""
restore() {
	if [[ -n $applied ]]; then
		git apply -R "$applied"
		applied=""
	fi
}
trap restore EXIT INT TERM

# runs <-test.run regex>: prints how many of the n runs passed
runs() {
	docker compose --progress quiet --profile test run --rm -T test bash -c '
		set -e
		go test -race -c -o /tmp/orders.test ./internal/orders
		ok=0
		for i in $(seq '"$n"'); do
			if /tmp/orders.test -test.count=1 -test.timeout=3m -test.run "'"$1"'" >/tmp/out 2>&1; then
				ok=$((ok + 1))
			fi
		done
		echo "passed=$ok"' 2>/dev/null | sed -n 's/^passed=//p'
}

count() {
	local c
	c=$(runs "$1")
	[[ $c =~ ^[0-9]+$ ]] || { echo "test binary did not build or run for $1" >&2; exit 1; }
	echo "$c"
}

exec > >(tee "$report")

echo "catch-rate: $n runs per variant, $(date -u +%Y-%m-%dT%H:%M:%SZ)"
pass=$(count '.')
echo "clean tree, full suite:          $pass/$n passed"
for patch in broken/*.patch; do
	name=$(basename "$patch" .patch)
	re="^($(sed -n 's/^Expect-Fail: *//p' "$patch" | paste -sd'|' -))\$"
	git apply "$patch"
	applied=$patch
	passed=$(count "$re")
	restore
	printf '%-32s %s/%s caught (%s)\n' "$name:" "$((n - passed))" "$n" "$re"
done
