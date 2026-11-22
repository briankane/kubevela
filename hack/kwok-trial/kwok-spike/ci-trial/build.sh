#!/usr/bin/env bash
# Builds and fetches everything trial.sh runs with, for one platform, into one directory.
#
#   build.sh <kubevela checkout> <out dir> [linux/amd64|linux/arm64]   (default: linux on this arch)
#
# Out: kwok-containers, kwokctl, kubectl, helm (CI's v3.6.3), ginkgo, vela (the branch's CLI),
# vela-1.11.0, core/vela-core-linux (and a vela-core-linux link to it), and the suites' test
# binaries config-upgrade.test and addon-module.test. Downloads are cached in the out dir.
# Exit codes: 0 ok, 1 failure, 2 usage.
set -euo pipefail
[[ $# -ge 2 ]] || { sed -n '2,9p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }
REPO=$(cd "$1" && pwd)
mkdir -p "$2/core"; OUT=$(cd "$2" && pwd)
HERE=$(cd "$(dirname "$0")" && pwd)
SRC=$HERE/../../kwok-containers
arch=$(uname -m); case $arch in x86_64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; esac
PLAT=${3:-linux/$arch}
export GOOS=${PLAT%/*} GOARCH=${PLAT#*/} CGO_ENABLED=0
KWOK=v0.8.0 KUBECTL=v1.36.1 HELM=v3.6.3 OLD=1.11.0
die() { echo "error: $*" >&2; exit 1; }
t0=$SECONDS
fetch() { # fetch <dest> <url> [tar member]
  [[ -s $1 ]] && return 0
  if [[ -n ${3:-} ]]; then curl -sSfL "$2" | tar -xzO "$3" > "$1.tmp"; else curl -sSfL "$2" -o "$1.tmp"; fi
  chmod +x "$1.tmp" && mv "$1.tmp" "$1"
}

(cd "$SRC" && go build -o "$OUT/kwok-containers" .) || die "kwok-containers"
(cd "$REPO" && go build -o "$OUT/core/vela-core-linux" ./cmd/core/main.go) || die "vela-core"
ln -sf core/vela-core-linux "$OUT/vela-core-linux"
(cd "$REPO" && go build -o "$OUT/vela" ./references/cmd/cli/main.go) || die "vela CLI"
(cd "$REPO" && go build -o "$OUT/ginkgo" github.com/onsi/ginkgo/v2/ginkgo) || die "ginkgo"
(cd "$REPO" && go test -c -o "$OUT/config-upgrade.test" ./test/e2e-config-upgrade-test) || die "config-upgrade.test"
(cd "$REPO" && go test -c -o "$OUT/addon-module.test" ./test/e2e-addon-module-test) || die "addon-module.test"
if [[ ! -s $OUT/kwokctl ]]; then
  m=$(mktemp -d)
  (cd "$m" && GOOS= GOARCH= go mod init kwokctl-build >/dev/null 2>&1 && GOOS= GOARCH= go get "sigs.k8s.io/kwok/cmd/kwokctl@$KWOK" >/dev/null 2>&1 \
    && go build -o "$OUT/kwokctl" sigs.k8s.io/kwok/cmd/kwokctl) || die "kwokctl"
  rm -rf "$m"
fi
fetch "$OUT/kubectl" "https://dl.k8s.io/release/$KUBECTL/bin/$GOOS/$GOARCH/kubectl"
fetch "$OUT/helm" "https://get.helm.sh/helm-$HELM-$GOOS-$GOARCH.tar.gz" "$GOOS-$GOARCH/helm"
fetch "$OUT/vela-$OLD" "https://github.com/kubevela/kubevela/releases/download/v$OLD/vela-v$OLD-$GOOS-$GOARCH.tar.gz" "$GOOS-$GOARCH/vela"
echo "ok: $PLAT tools in $OUT ($((SECONDS - t0))s)"
