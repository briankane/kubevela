#!/usr/bin/env bash
# A KWOK cluster for running a chart with real Helm: every pod in vela-system (vela-core,
# the chart's hook Jobs, cluster-gateway) runs for real on a container node, and the API
# server reaches their webhooks, Services and kubelet API through the node's proxy.
#
#   helm-env.sh up       cluster and container node; prints the KUBECONFIG
#   helm-env.sh image    build vela-core-test:<tag> from HELM_ENV_BIN/vela-core-linux; prints the tag
#   helm-env.sh down
#
# Env: HELM_ENV_DIR (state), HELM_ENV_NAME (default vela-helm), HELM_ENV_REPO (kubevela
# worktree, for image), HELM_ENV_BIN (dir with kwok-containers and vela-core-linux),
# HELM_ENV_TAG (the image tag, default git-<short HEAD> as CI's deploy step).
# Ports (host, 127.0.0.1): scheduling webhook 9874, proxy 18891, kubelet 10262.
# Exit codes: 0 ok, 1 failure, 2 usage.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
W=${HELM_ENV_DIR:?set HELM_ENV_DIR}
NAME=${HELM_ENV_NAME:-vela-helm}
BIN=${HELM_ENV_BIN:?set HELM_ENV_BIN}
KC=$W/kubeconfig
CIDR=10.96.0.0/16
NET=kc${NAME//-/}
PROXY=18891
SOCK=/tmp/kwok-$NAME-egress.sock  # Unix socket paths are capped at 104 bytes on macOS
export PATH="$HOME/.local/share/mise/shims:$PATH"
for d in "$HOME"/.local/share/mise/installs/go/*/bin; do [[ -x $d/kwokctl ]] && { PATH="$PATH:$d"; break; }; done
die() { echo "error: $*" >&2; exit 1; }

up() {
  mkdir -p "$W"
  down >/dev/null 2>&1 || true
  cat > "$W/egress.yaml" <<YAML
apiVersion: apiserver.k8s.io/v1beta1
kind: EgressSelectorConfiguration
egressSelections:
- name: cluster
  connection:
    proxyProtocol: HTTPConnect
    transport:
      uds:
        udsName: $SOCK
YAML
  cat > "$W/apiserver.yaml" <<YAML
apiVersion: config.kwok.x-k8s.io/v1alpha1
kind: KwokctlConfiguration
componentsPatches:
- name: kube-apiserver
  extraArgs:
  - {key: enable-aggregator-routing, value: "true"}
  - {key: service-cluster-ip-range, value: "$CIDR"}
  - {key: egress-selector-config-file, value: "$W/egress.yaml"}
- name: kube-controller-manager
  extraArgs:
  - {key: service-cluster-ip-range, value: "$CIDR"}
YAML
  kwokctl create cluster --name "$NAME" --runtime binary --kubeconfig "$KC" -c "$HERE/stages.yaml" \
    -c "$HERE/containers-config.yaml" -c "$W/apiserver.yaml" --wait 3m >"$W/kwokctl.log" 2>&1 || die "cluster: see $W/kwokctl.log"
  kwokctl scale node --replicas 1 --name "$NAME" >/dev/null 2>&1
  nohup "$BIN/kwok-containers" attach -kubeconfig "$KC" -name "$NET" -namespaces vela-system -service-cidr "$CIDR" \
    -egress-socket "$SOCK" -webhook-port 9874 -proxy-port $PROXY -kubelet-port 10262 > "$W/attach.log" 2>&1 < /dev/null &
  echo $! > "$W/attach.pid"
  for _ in $(seq 1 120); do grep -q '^ready:' "$W/attach.log" && break; grep -q '^error' "$W/attach.log" && break; sleep 1; done
  grep -q '^ready:' "$W/attach.log" || { tail -5 "$W/attach.log" >&2; die "container node"; }
  echo "ok: $NAME up; KUBECONFIG=$KC"
}

# image wraps the branch binary as the Dockerfile does, under the tag CI's deploy step uses.
image() {
  local repo=${HELM_ENV_REPO:?set HELM_ENV_REPO} tag ctx
  tag=${HELM_ENV_TAG:-git-$(git -C "$repo" rev-parse --short HEAD)}
  ctx=$(mktemp -d)
  cp "$BIN/vela-core-linux" "$ctx/manager"
  cp "$repo/entrypoint.sh" "$ctx/"
  printf 'FROM alpine:3.20\nCOPY manager /usr/local/bin/manager\nCOPY entrypoint.sh /usr/local/bin/\nENTRYPOINT ["entrypoint.sh"]\nCMD ["manager"]\n' > "$ctx/Dockerfile"
  docker build -q -t "vela-core-test:$tag" "$ctx" >/dev/null || die "image build"
  rm -rf "$ctx"
  echo "$tag"
}

down() {
  [[ -f $W/attach.pid ]] && kill -INT "$(cat "$W/attach.pid")" 2>/dev/null || true
  for _ in $(seq 1 30); do [[ -f $W/attach.pid ]] && kill -0 "$(cat "$W/attach.pid")" 2>/dev/null || break; sleep 1; done
  kwokctl delete cluster --name "$NAME" >/dev/null 2>&1 || true
  echo "ok: $NAME down"
}

case ${1:-} in
  up) up ;;
  image) image ;;
  down) down ;;
  *) sed -n '2,14p' "$0" | sed 's/^# \{0,1\}//'; exit 2 ;;
esac
