#!/usr/bin/env bash
# Acceptance check: clone HEAD into an empty directory and run the documented
# commands there, timing each. The stack of this checkout is taken down first
# (both use compose project "envkit"), so the clone starts with no volumes and
# no API image. Base images and Docker's layer cache are left alone.
# Output: results/clean-clone.txt
set -uo pipefail
cd "$(dirname "$0")/.."
root=$PWD
dir=.clone-check # inside the repo so Docker VMs that share only $HOME can mount it
report=$root/results/clean-clone.txt

make down >/dev/null 2>&1
docker image rm -f envkit-api >/dev/null 2>&1
rm -rf "$dir"
git clone -q "$root" "$dir"
cd "$dir"

status=0
{
	echo "# git clone of $(git rev-parse --short HEAD) into an empty directory, $(date -u +%FT%TZ)"
	echo "# no envkit volumes, no envkit-api image; base images already pulled, layer cache kept"
	echo "# other containers running on the same Docker VM: $(docker ps -q | wc -l | tr -d ' ')"
	for target in up test test-broken; do
		echo
		echo "\$ make $target"
		start=$(date +%s)
		make "$target" 2>&1 | grep -vE '^ *(Image|Container|Volume|Network) |^ *$'
		code=${PIPESTATUS[0]}
		echo "# make $target: exit $code, $(($(date +%s) - start)) s"
		[[ $code -eq 0 ]] || status=1
	done
	echo
	echo "\$ git status --short -- . ':!results'"
	git status --short -- . ':!results'
	echo "# end of clean-clone check, status $status"
} | tee "$report"

make down >/dev/null 2>&1
cd "$root" && rm -rf "$dir"
exit $status
