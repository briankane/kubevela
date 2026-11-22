#!/usr/bin/env bash
# Prepare a KWOK cluster for running kubevela's e2e suites against a host vela-core.
# Usage: setup.sh <kubevela-worktree> <kubeconfig> [webhook-port, default 9445]
# E2E_TARGET picks the e2e.mk target whose --set flags are used (default e2e-setup-core-wo-auth).
# Installs the chart (CRDs, definitions, RBAC) with e2e.mk's gates, scales the
# in-cluster controller to 0, and points admission at a vela-core on 127.0.0.1:<port>.
set -euo pipefail
REPO=$1 KC=$2 PORT=${3:-9445}
export KUBECONFIG=$KC
cd "$REPO"
[[ $(kubectl config current-context) == kwok-* ]] || { echo "refusing: context is not a kwok cluster" >&2; exit 1; }

# Webhooks from an earlier run point at the host and fail closed, so they go first.
kubectl get validatingwebhookconfiguration,mutatingwebhookconfiguration -o name | { grep kubevela || true; } | xargs -r kubectl delete >/dev/null

TARGET=${E2E_TARGET:-e2e-setup-core-wo-auth}
sets=$(awk -v t="^${TARGET}:" '$0 ~ t {f=1;next} f&&/^(\.PHONY|[A-Za-z0-9_.-]+:)/{exit} f' makefiles/e2e.mk \
  | grep -oE -- '--set[[:space:]]+[^[:space:]]+' | grep -v 'image\.' | tr -d "'" | tr '\n' ' ')
# shellcheck disable=SC2086
helm upgrade --install kubevela ./charts/vela-core --namespace vela-system --create-namespace \
  $sets --set multicluster.enabled=false --set sharding.enabled=false --set admissionWebhooks.enabled=false --wait --timeout 3m >/dev/null
kubectl -n vela-system scale deploy kubevela-vela-core --replicas=0 >/dev/null

./hack/debug-webhook-setup.sh "$PORT" >/tmp/kwok-webhook-setup.log 2>&1 || { tail -20 /tmp/kwok-webhook-setup.log >&2; exit 1; }
for kind in validatingwebhookconfiguration mutatingwebhookconfiguration; do
  for n in $(kubectl get $kind -o name | grep kubevela); do
    kubectl get "$n" -o json | sed -E "s#https://[^/:\"]+:${PORT}/#https://127.0.0.1:${PORT}/#g" | kubectl replace -f - >/dev/null
  done
done
n=$(kubectl get validatingwebhookconfiguration,mutatingwebhookconfiguration -o json | grep -c "127.0.0.1:${PORT}" || true)
echo "ok: chart installed, controller scaled to 0, $n webhook URLs on 127.0.0.1:${PORT}"
