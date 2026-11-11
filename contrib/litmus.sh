#!/bin/sh
# Runs the litmus WebDAV conformance suite (https://github.com/notroj/litmus)
# against cmd/webdav-server with locking enabled, using the owncloud/litmus
# Docker image.
#
# Usage: contrib/litmus.sh [server address as seen from the container]
#
# On Linux the container shares the host network, so the default
# http://localhost:8099/ works. With Docker Desktop, pass
# http://host.docker.internal:8099/.
#
# The props suite is not run: LocalFileSystem doesn't store dead properties,
# so its PROPPATCH tests are refused with 403. For the same reason, the
# owner_modify tests of the locks suite, which PROPPATCH a locked resource,
# are expected to fail; any other failure fails the run.
set -eu

url=${1:-http://localhost:8099/}
dir=$(mktemp -d)
out=$(mktemp)
trap 'kill $pid 2>/dev/null || true; rm -rf "$dir" "$out"' EXIT

go build -o "$dir/webdav-server" ./cmd/webdav-server
mkdir "$dir/root"
"$dir/webdav-server" -addr 127.0.0.1:8099 -locking "$dir/root" &
pid=$!
sleep 1

status=0
docker run --rm --network host --platform linux/amd64 \
	-e TESTS="basic copymove locks" -e LITMUS_URL="$url" \
	owncloud/litmus:latest >"$out" 2>&1 || status=$?
cat "$out"

unexpected=$(grep -E '^ *[0-9]+\. .*FAIL' "$out" | grep -v 'owner_modify' || true)
if [ -n "$unexpected" ]; then
	echo "unexpected litmus failures:"
	echo "$unexpected"
	exit 1
fi
for suite in basic copymove locks; do
	if ! grep -q "summary for \`$suite'" "$out"; then
		echo "litmus suite $suite didn't run (litmus exit status $status)"
		exit 1
	fi
done
echo "litmus: no unexpected failures"
