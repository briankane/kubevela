#!/usr/bin/env bash
# Rehearses a trial job on this machine as a GitHub-hosted runner would run it: Linux with
# Docker Engine (not Docker Desktop's forwarding), 4 CPUs and 16 GB, in a privileged
# container with its own dockerd. The architecture is this machine's, not necessarily amd64.
#
#   rehearse.sh <config-upgrade|addon-module> <kubevela checkout>
#
# Env: TRIAL_DIR (state, default $TMPDIR/kwok-trial): bin/ from build.sh, out-<job>/ with the
# job's logs. The inner Docker's images and kwokctl's downloads are kept in the volumes
# kwok-trial-docker and kwok-trial-kwok-cache between runs.
# Exit codes: trial.sh's; 2 usage.
set -euo pipefail
[[ $# -eq 2 ]] || { sed -n '2,11p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }
JOB=$1
REPO=$(cd "$2" && pwd)
HERE=$(cd "$(dirname "$0")" && pwd)
SPIKE=$(cd "$HERE/.." && pwd)
DIR=${TRIAL_DIR:-${TMPDIR:-/tmp}/kwok-trial}
BIN=$DIR/bin OUT=$DIR/out-$JOB
arch=$(uname -m); case $arch in x86_64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; esac

"$HERE/build.sh" "$REPO" "$BIN" "linux/$arch"
docker build -q -t kwok-trial-runner "$HERE/dind" >/dev/null
rm -rf "$OUT"; mkdir -p "$OUT"
docker rm -f "kwok-trial-$JOB" >/dev/null 2>&1 || true
docker run --rm --name "kwok-trial-$JOB" --privileged --cpuset-cpus 0-3 --memory 16g \
  -v kwok-trial-docker:/var/lib/docker -v kwok-trial-kwok-cache:/root/.kwok/cache \
  -v "$REPO:$REPO:ro" -v "$BIN/vela:$REPO/bin/vela:ro" -v "$SPIKE:$SPIKE:ro" \
  -v "$BIN:/trial/bin:ro" -v "$OUT:/trial/out" \
  -e REPO="$REPO" -e BIN=/trial/bin -e OUT=/trial/out -e HELM_ENV_TAG="git-$(git -C "$REPO" rev-parse --short HEAD)" \
  kwok-trial-runner "$HERE/trial.sh" "$JOB"
