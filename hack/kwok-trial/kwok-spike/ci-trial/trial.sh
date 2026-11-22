#!/usr/bin/env bash
# One CI trial job on KWOK with a container node, on a Linux host with Docker Engine: the
# GitHub Actions trial and the local dind rehearsal both run this, so the two cannot drift.
#
#   trial.sh config-upgrade   upgrade-e2e-test.yml's config upgrade, step for step, with real Helm
#   trial.sh addon-module     e2e-test.yml's addon-module suite
#
# Env: REPO (kubevela checkout; $REPO/bin/vela must be the branch CLI), BIN (build.sh's out dir),
# OUT (logs, docker stats, steps.tsv). Tools come from $BIN, then PATH.
# Prints one line per step and a summary; exit 0 when every step passed, 1 otherwise, 2 usage.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
SPIKE=$HERE/..
JOB=${1:-}
REPO=${REPO:?set REPO} BIN=${BIN:?set BIN} OUT=${OUT:?set OUT}
export PATH="$BIN:$PATH"
mkdir -p "$OUT"
: > "$OUT/steps.tsv"
failed=0

# step <name> <command...>: runs it with its output in $OUT/<name>.log and records the result.
step() {
  local name=$1 t=$SECONDS rc; shift
  "$@" > "$OUT/$name.log" 2>&1; rc=$?
  printf '%s\t%s\t%s\n' "$name" "$rc" "$((SECONDS - t))" >> "$OUT/steps.tsv"
  echo "$name: $([[ $rc == 0 ]] && echo ok || echo "FAILED rc=$rc, tail of $OUT/$name.log:") $((SECONDS - t))s"
  [[ $rc == 0 ]] || { tail -15 "$OUT/$name.log" | sed 's/^/  /'; failed=1; }
  return $rc
}

# sample records every container's CPU and memory, and the host's load, every 5s.
sample() {
  echo "time,name,cpu,mem" > "$OUT/docker-stats.csv"
  while sleep 5; do
    local t; t=$(date +%s)
    docker stats --no-stream --format '{{.Name}},{{.CPUPerc}},{{.MemUsage}}' 2>/dev/null | sed "s/^/$t,/" >> "$OUT/docker-stats.csv"
    echo "$t $(cut -d' ' -f1-3 /proc/loadavg)" >> "$OUT/loadavg.txt"
  done
}

summary() {
  kill "$SAMPLER" 2>/dev/null
  local peak
  peak=$(awk -F, 'NR>1{gsub("%","",$3); s[$1]+=$3} END{m=0; for(t in s) if(s[t]>m) m=s[t]; printf "%.0f", m/100}' "$OUT/docker-stats.csv")
  echo "summary: $JOB $([[ $failed == 0 ]] && echo passed || echo FAILED) in $((SECONDS))s on $(nproc) cores; containers peaked at ~${peak} cores; $(awk -F'\t' '$2!=0' "$OUT/steps.tsv" | wc -l | tr -d ' ') failed steps"
}

config_upgrade() {
  export HELM_ENV_DIR=$OUT/env HELM_ENV_BIN=$BIN HELM_ENV_REPO=$REPO HELM_ENV_NAME=vela-helm
  export KUBECONFIG=$OUT/env/kubeconfig VELA_OLD_CLI=$BIN/vela-1.11.0 VELA_NEW_CLI=$REPO/bin/vela
  unset VELA_E2E_CONTROLLER
  local net=kcvelahelm tag ip
  step cluster "$SPIKE/helm-env.sh" up || return
  tag=$("$SPIKE/helm-env.sh" image) || { echo "image: failed"; failed=1; return; }

  # Nacos on the node's network: the 1.11 CLI here and the controller's pod reach one address.
  docker rm -f trial-nacos >/dev/null 2>&1
  docker run -d --name trial-nacos --network "$net" -e MODE=standalone -e NACOS_AUTH_ENABLE=false \
    -e JVM_XMS=256m -e JVM_XMX=512m nacos/nacos-server:v2.4.3 >/dev/null
  ip=$(docker inspect -f "{{(index .NetworkSettings.Networks \"$net\").IPAddress}}" trial-nacos)
  export NACOS_ADDR=$ip:8848 NACOS_CLI_HOST=$ip
  step nacos-up sh -c "for i in \$(seq 80); do curl -sf --max-time 3 http://$ip:8848/nacos/v1/console/health/readiness && exit 0; sleep 3; done; exit 1" || return

  cd "$REPO" || return
  step install sh -c 'helm repo add kubevela https://kubevela.github.io/charts >/dev/null && helm repo update >/dev/null &&
    helm install --create-namespace -n vela-system kubevela kubevela/vela-core --version 1.11.0 \
      --set multicluster.clusterGateway.secureTLS.enabled=false --timeout 10m --wait' || return
  phase seed || return
  step crds kubectl apply -f charts/vela-core/crds || return
  step upgrade helm upgrade kubevela ./charts/vela-core -n vela-system --set image.repository=vela-core-test \
    --set "image.tag=$tag" --set image.pullPolicy=IfNotPresent --timeout 5m --wait || return
  step rollout kubectl -n vela-system rollout status deploy/kubevela-vela-core --timeout=240s || return
  for p in after-upgrade migrate gate nacos; do phase "$p"; done
  step rollback rollback || return
  phase after-rollback
}

phase() {
  step "$1" sh -c "cd '$REPO/test/e2e-config-upgrade-test' && '$BIN/config-upgrade.test' -test.count=1 -test.timeout 15m -ginkgo.v -ginkgo.label-filter='$1'"
}

# rollback is CI's: the first rollback fails on the definitions the chart stopped shipping, and
# the second, with the webhook configuration deleted, recreates it as 1.11.0 has it.
rollback() {
  local out
  if ! out=$(helm rollback kubevela 1 -n vela-system --wait --timeout 6m 2>&1); then
    echo "first rollback: $out"
    echo "$out" | grep -q "no ComponentDefinition with the name" || return 1
  fi
  kubectl delete validatingwebhookconfiguration kubevela-vela-core-admission
  helm rollback kubevela 1 -n vela-system --wait --timeout 6m
  kubectl -n vela-system rollout status deploy/kubevela-vela-core --timeout=240s
}

addon_module() {
  export SUITE_ENV_DIR=$OUT/env SUITE_ENV_REPO=$REPO SUITE_ENV_BIN=$BIN/core SUITE_ENV_NAME=vela-e2e
  export KUBECONFIG=$OUT/env/kubeconfig
  step cluster "$SPIKE/suite-env.sh" up e2e-setup-core-wo-auth 'app in (oci-registry,addon-module-chartmuseum)' || return
  # The registries' NodePorts on the container node: a Linux host routes to the node's network,
  # so the CLI here and the controller's pod use the same URL.
  local node
  node=$(sed -n 's/^CoreDNS at [0-9.]*, kube-proxy at \([0-9.]*\).*/\1/p' "$OUT/env/attach.log" | tail -1)
  export MODULE_E2E_REGISTRY_URL=http://$node:30500/modules ADDON_E2E_REGISTRY_URL=http://$node:30501
  cd "$REPO" || return
  step suite ginkgo -v --procs="$(nproc)" --timeout=2h --output-dir="$OUT" --json-report=addon-module.json "$BIN/addon-module.test"
}

down() {
  case $JOB in
    config-upgrade) HELM_ENV_DIR=$OUT/env HELM_ENV_BIN=$BIN HELM_ENV_NAME=vela-helm "$SPIKE/helm-env.sh" down >/dev/null 2>&1; docker rm -f trial-nacos >/dev/null 2>&1 ;;
    addon-module) SUITE_ENV_DIR=$OUT/env SUITE_ENV_REPO=$REPO SUITE_ENV_BIN=$BIN/core SUITE_ENV_NAME=vela-e2e "$SPIKE/suite-env.sh" down >/dev/null 2>&1 ;;
  esac
}

case $JOB in
  config-upgrade|addon-module) ;;
  *) sed -n '2,10p' "$0" | sed 's/^# \{0,1\}//'; exit 2 ;;
esac
sample & SAMPLER=$!
trap 'down; summary' EXIT
SECONDS=0
if [[ $JOB == config-upgrade ]]; then config_upgrade; else addon_module; fi
exit $failed
