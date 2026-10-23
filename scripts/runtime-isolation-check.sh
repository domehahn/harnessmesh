#!/usr/bin/env sh
set -eu

: "${HARNESSMESH_ISOLATION_IMAGE:?set HARNESSMESH_ISOLATION_IMAGE to the exact production image digest/tag}"

if ! command -v docker >/dev/null 2>&1; then
	echo "RUNTIME_ISOLATION=FAIL docker is not installed" >&2
	exit 1
fi

docker image inspect "$HARNESSMESH_ISOLATION_IMAGE" >/dev/null

# The production image runs as the non-root service user and drops all Linux
# capabilities. Bubblewrap must still be able to create the private namespace;
# a failure is a hard deployment gate, never a warning.
docker run --rm --read-only --cap-drop=ALL \
	--security-opt=no-new-privileges \
	--entrypoint /usr/bin/bwrap "$HARNESSMESH_ISOLATION_IMAGE" \
	--die-with-parent --new-session --unshare-net \
	--tmpfs / --dev /dev --proc /proc --tmpfs /tmp \
	--ro-bind /usr /usr --ro-bind /bin /bin --ro-bind /sbin /sbin \
	--ro-bind /lib /lib --ro-bind /etc /etc \
	--dir /workspace --chdir /workspace -- true

echo "RUNTIME_ISOLATION=PASS image=$HARNESSMESH_ISOLATION_IMAGE"
