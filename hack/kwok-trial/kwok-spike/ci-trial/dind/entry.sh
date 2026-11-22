#!/usr/bin/env bash
# Starts Docker Engine, waits for it, then runs the given command.
set -euo pipefail
dockerd > /var/log/dockerd.log 2>&1 &
for _ in $(seq 60); do docker info >/dev/null 2>&1 && break; sleep 1; done
docker info >/dev/null 2>&1 || { tail -20 /var/log/dockerd.log >&2; exit 1; }
exec "$@"
