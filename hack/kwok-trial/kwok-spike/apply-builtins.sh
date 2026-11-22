#!/usr/bin/env bash
# Apply the chart's builtin definitions from its ConfigMaps, as the hook Job would.
# On KWOK the Job completes without running, so this stands in for it. Needs the
# controller (and so the webhook) running. Usage: apply-builtins.sh <kubeconfig>
set -euo pipefail
export KUBECONFIG=$1
[[ $(kubectl config current-context) == kwok-* ]] || { echo "refusing: context is not a kwok cluster" >&2; exit 1; }
dir=$(mktemp -d)
for cm in $(kubectl -n vela-system get cm -o name | grep -i builtin); do
  kubectl -n vela-system get "$cm" -o json \
    | python3 -c 'import json,sys,os; d=json.load(sys.stdin).get("data",{}); [open(os.path.join(sys.argv[1],k),"w").write(v) for k,v in d.items()]' "$dir"
done
n=$(ls "$dir" | wc -l | tr -d ' ')
out=$(kubectl apply --server-side -f "$dir" 2>&1) || { echo "$out" | grep -v serverside-applied >&2; exit 1; }
echo "ok: $n builtin definitions applied"
rm -rf "$dir"
