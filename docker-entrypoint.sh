#!/bin/sh
set -e

PUID=${PUID:-99}
PGID=${PGID:-100}

# Reject root and out-of-range ids instead of running privileged by accident.
for pair in "PUID:$PUID" "PGID:$PGID"; do
    name="${pair%%:*}"
    value="${pair#*:}"
    if ! echo "$value" | grep -qE '^[0-9]+$' || [ "$value" -lt 1 ] || [ "$value" -gt 65534 ]; then
        echo "ERROR: $name must be between 1 and 65534, got: $value" >&2
        exit 1
    fi
done

# su-exec needs resolvable passwd/group entries for the target ids.
if ! getent group "$PGID" >/dev/null 2>&1; then
    addgroup -g "$PGID" cd211
fi
if ! getent passwd "$PUID" >/dev/null 2>&1; then
    group_name=$(getent group "$PGID" | cut -d: -f1)
    adduser -D -u "$PUID" -G "$group_name" -h /data cd211
fi

# CD211 creates the SQLite database and the per-category staging directories
# itself, so both roots must be writable by the target user. Only the top level
# is touched, because a populated staging root can hold many files.
chown "$PUID:$PGID" /data /downloads

# Switch by numeric ids so the GID matches exactly instead of following the
# user's primary group.
exec su-exec "$PUID:$PGID" "$@"
