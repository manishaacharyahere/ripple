#!/usr/bin/env bash
# Proves the runner NetworkPolicy is enforced: a pod with the runner's labels
# must reach the internet over HTTPS but not the Kubernetes API or the cloud
# metadata endpoint. Any HTTP response (even 403) counts as reachable.
set -euo pipefail
ctx=${1:-kind-ripple}
k="kubectl --context $ctx -n ripple"

$k delete pod sandbox-check --ignore-not-found --wait >/dev/null
$k run sandbox-check --image=ripple-runner:dev --image-pull-policy=IfNotPresent \
  --labels=app.kubernetes.io/name=ripple-runner --restart=Never --command -- sh -c '
probe() { # name url want
  out=$(wget -q -T 5 -O /dev/null --no-check-certificate "$2" 2>&1) && got=reachable || {
    case "$out" in *"server returned error"*) got=reachable ;; *) got=blocked ;; esac; }
  [ "$got" = "$3" ] && echo "PASS $1: $got" || { echo "FAIL $1: $got (want $3)"; fail=1; }
}
fail=0
probe "kubernetes API" https://kubernetes.default.svc/ blocked
probe "cloud metadata" http://169.254.169.254/ blocked
probe "github.com"     https://github.com/ reachable
exit $fail' >/dev/null

$k wait --for=jsonpath='{.status.phase}'=Succeeded pod/sandbox-check --timeout=120s >/dev/null 2>&1 || true
$k logs sandbox-check
phase=$($k get pod sandbox-check -o jsonpath='{.status.phase}')
$k delete pod sandbox-check --wait=false >/dev/null
[ "$phase" = Succeeded ]
