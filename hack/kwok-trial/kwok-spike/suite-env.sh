#!/usr/bin/env bash
# One KWOK cluster for a single-cluster kubevela e2e suite, with vela-core in Docker on a
# container node's network, so pods that must really run (registries, ChartMuseum) are
# reachable from the controller by Service name.
#
#   suite-env.sh up <e2e.mk setup target> [pod-selector]   cluster, chart, container node, vela-core
#   suite-env.sh core [extra controller flags...]           restart vela-core (flags laid over the chart's)
#   suite-env.sh follow                                     restart vela-core whenever its Deployment's
#                                                           pod template changes (up starts it)
#   suite-env.sh down
#
# Env: SUITE_ENV_DIR (state), SUITE_ENV_NAME (cluster and Docker prefix, default vela-e2e),
# SUITE_ENV_REPO (kubevela worktree), SUITE_ENV_BIN (dir with vela-core-linux and vela),
# SUITE_ENV_CA_DIR (optional: certificates vela-core trusts beside the system ones, as the
# auth registries' CA injection does for the in-cluster Deployment).
# Ports (host, 127.0.0.1): webhook 9746, scheduling webhook 9873, proxy 18890, kubelet 10261.
# Exit codes: 0 ok, 1 failure, 2 usage.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
W=${SUITE_ENV_DIR:?set SUITE_ENV_DIR}
NAME=${SUITE_ENV_NAME:-vela-e2e}
REPO=${SUITE_ENV_REPO:?set SUITE_ENV_REPO}
BIN=${SUITE_ENV_BIN:?set SUITE_ENV_BIN}
KC=$W/kubeconfig
CIDR=10.96.0.0/16
NET=kc${NAME//-/}
PORT=9746
export PATH="$HOME/.local/share/mise/shims:$PATH"
for d in "$HOME"/.local/share/mise/installs/go/*/bin; do [[ -x $d/kwokctl ]] && { PATH="$PATH:$d"; break; }; done
die() { echo "error: $*" >&2; exit 1; }

core() {
  local extra=("$@")
  docker rm -f "$NET-core" >/dev/null 2>&1 || true
  local args=() l
  while IFS= read -r l; do args+=("$l"); done < "$W/core.args"
  args+=("${extra[@]+"${extra[@]}"}")
  local dns proxy
  dns=$(sed -n 's/^CoreDNS at \([0-9.]*\), kube-proxy at \([0-9.]*\).*/\1/p' "$W/attach.log" | tail -1)
  proxy=$(sed -n 's/^CoreDNS at \([0-9.]*\), kube-proxy at \([0-9.]*\).*/\2/p' "$W/attach.log" | tail -1)
  [[ -n $dns && -n $proxy ]] || die "no CoreDNS/kube-proxy line in $W/attach.log"
  docker run -d --name "$NET-core" --network "$NET" --cap-add NET_ADMIN --add-host host.docker.internal:host-gateway \
    --dns "$dns" --dns-search svc.cluster.local --dns-search cluster.local --dns-option ndots:5 \
    -p "127.0.0.1:$PORT:$PORT" -e KUBECONFIG=/w/core-docker.kubeconfig -v "$W:/w:ro" -v "$BIN:/b:ro" \
    ${SUITE_ENV_CA_DIR:+-v "$SUITE_ENV_CA_DIR:/ca:ro" -e SSL_CERT_DIR=/etc/ssl/certs:/ca} \
    alpine:3.20 sh -c "ip route add $CIDR via $proxy && exec /b/vela-core-linux ${args[*]}" >/dev/null
  for _ in $(seq 1 90); do
    docker exec "$NET-core" wget -qO- http://127.0.0.1:9741/readyz 2>/dev/null | grep -q ok && \
      docker logs "$NET-core" 2>&1 | grep -q "Serving webhook server" && { echo "ok: vela-core ready"; return 0; }
    sleep 1
  done
  docker logs "$NET-core" 2>&1 | tail -15 >&2; die "vela-core not ready"
}

# write_args takes the controller's flags from the Deployment, as a restart would, minus
# the ones that place it in the cluster.
write_args() {
  kubectl --kubeconfig "$KC" -n vela-system get deploy kubevela-vela-core -o jsonpath='{range .spec.template.spec.containers[0].args[*]}{@}{"\n"}{end}' \
    | grep -vE '^--(use-webhook|metrics-addr|health-addr|webhook-port|webhook-cert-dir|webhook-tls-secret|webhook-configuration|enable-leader-election|leader-election)' \
    > "$W/core.args.new"
  printf '%s\n' --use-webhook=true --metrics-addr=:8391 --health-addr=:9741 --webhook-port=$PORT --webhook-cert-dir=/w/repo/k8s-webhook-server/serving-certs --leader-elect=false >> "$W/core.args.new"
  mv "$W/core.args.new" "$W/core.args"
}

# follow stands in for the kubelet: a test that restarts vela-core or changes its flags
# through the Deployment (rollout restart, feature gates) gets the container restarted.
follow() {
  local last cur
  last=$(kubectl --kubeconfig "$KC" -n vela-system get deploy kubevela-vela-core -o jsonpath='{.spec.template}' | shasum)
  while sleep 1; do
    cur=$(kubectl --kubeconfig "$KC" -n vela-system get deploy kubevela-vela-core -o jsonpath='{.spec.template}' 2>/dev/null | shasum) || continue
    [[ $cur == "$last" ]] && continue
    last=$cur
    echo "$(date +%T) pod template changed: restarting vela-core"
    (write_args && core) || echo "$(date +%T) restart failed"
  done
}

up() {
  local target=$1 sel=${2:-kwok.e2e/real=true}
  mkdir -p "$W"
  kwokctl delete cluster --name "$NAME" >/dev/null 2>&1 || true
  local pki=$HOME/.kwok/clusters/$NAME/pki
  cat > "$W/apiserver.yaml" <<YAML
apiVersion: config.kwok.x-k8s.io/v1alpha1
kind: KwokctlConfiguration
componentsPatches:
- name: kube-apiserver
  extraArgs:
  - {key: enable-aggregator-routing, value: "true"}
  - {key: service-cluster-ip-range, value: "$CIDR"}
- name: kube-controller-manager
  extraArgs:
  - {key: service-cluster-ip-range, value: "$CIDR"}
  - {key: cluster-signing-cert-file, value: "$pki/ca.crt"}
  - {key: cluster-signing-key-file, value: "$pki/ca.key"}
YAML
  kwokctl create cluster --name "$NAME" --runtime binary --kubeconfig "$KC" -c "$HERE/stages.yaml" \
    -c "$HERE/containers-config.yaml" -c "$W/apiserver.yaml" --wait 3m >"$W/kwokctl.log" 2>&1 || die "cluster: see $W/kwokctl.log"
  kwokctl scale node --replicas 1 --name "$NAME" >/dev/null 2>&1
  export KUBECONFIG=$KC

  # A shadow of the worktree, so the webhook certificates this cluster gets do not replace
  # the ones another cluster's controller is serving from the worktree.
  rm -rf "$W/repo"; mkdir -p "$W/repo"
  for d in charts makefiles hack; do ln -s "$REPO/$d" "$W/repo/$d"; done
  E2E_TARGET=$target "$HERE/setup.sh" "$W/repo" "$KC" "$PORT" >"$W/setup.log" 2>&1 || { tail -20 "$W/setup.log" >&2; die "setup"; }
  if kubectl get clusterrole kubevela-vela-core:manager >/dev/null 2>&1 &&
    ! kubectl get clusterrole kubevela-vela-core:manager -o json | grep -q '"cue.oam.dev"'; then
    kubectl patch clusterrole kubevela-vela-core:manager --type=json \
      -p '[{"op":"add","path":"/rules/-","value":{"apiGroups":["cue.oam.dev"],"resources":["packages"],"verbs":["get","list","watch"]}}]' >/dev/null
  fi

  local tok server
  tok=$(kubectl -n vela-system create token kubevela-vela-core --duration=48h)
  server=$(kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}' | sed 's#//[^:]*:#//host.docker.internal:#')
  cat > "$W/core-docker.kubeconfig" <<YAML
apiVersion: v1
kind: Config
clusters: [{name: kwok, cluster: {server: "$server", insecure-skip-tls-verify: true}}]
users: [{name: sa, user: {token: "$tok"}}]
contexts: [{name: kwok, context: {cluster: kwok, user: sa}}]
current-context: kwok
YAML
  chmod 600 "$W/core-docker.kubeconfig"
  write_args

  pkill -INT -f "kwok-containers attach -kubeconfig $KC" 2>/dev/null || true
  nohup "$BIN/../kwok-containers" attach -kubeconfig "$KC" -name "$NET" -pod-selector "$sel" -service-cidr "$CIDR" \
    -webhook-port 9873 -proxy-port 18890 -kubelet-port 10261 > "$W/attach.log" 2>&1 < /dev/null &
  echo $! > "$W/attach.pid"
  for _ in $(seq 1 120); do grep -q '^ready:' "$W/attach.log" && break; grep -q '^error' "$W/attach.log" && break; sleep 1; done
  grep -q '^ready:' "$W/attach.log" || { tail -5 "$W/attach.log" >&2; die "container node"; }
  core
  "$HERE/apply-builtins.sh" "$KC" >/dev/null
  nohup "$0" follow > "$W/follow.log" 2>&1 < /dev/null &
  echo $! > "$W/follow.pid"
  echo "ok: $NAME up ($target); KUBECONFIG=$KC"
}

down() {
  [[ -f $W/follow.pid ]] && kill "$(cat "$W/follow.pid")" 2>/dev/null || true
  [[ -f $W/attach.pid ]] && kill -INT "$(cat "$W/attach.pid")" 2>/dev/null || true
  for _ in $(seq 1 30); do [[ -f $W/attach.pid ]] && kill -0 "$(cat "$W/attach.pid")" 2>/dev/null || break; sleep 1; done
  docker rm -f "$NET-core" >/dev/null 2>&1 || true
  kwokctl delete cluster --name "$NAME" >/dev/null 2>&1 || true
  echo "ok: $NAME down"
}

case ${1:-} in
  up) shift; [[ $# -ge 1 ]] || { sed -n '2,17p' "$0"; exit 2; }; up "$@" ;;
  core) shift; core "$@" ;;
  follow) follow ;;
  down) down ;;
  *) sed -n '2,17p' "$0" | sed 's/^# \{0,1\}//'; exit 2 ;;
esac
