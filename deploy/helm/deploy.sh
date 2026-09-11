#!/bin/sh
# Прямой helm-деплой activity-dashboard в личный namespace.
#
# HELM_DRIVER=configmap ОБЯЗАТЕЛЕН: личный namespace разрешает читать Secret'ы,
# но не создавать их, а Helm по умолчанию хранит состояние релиза в Secret —
# без этой переменной каждая helm-команда падает с "secrets is forbidden".
#
# Использование:
#   ./deploy.sh                 # контекст dal2.staging, namespace u-adavydov
#   ./deploy.sh nyc.staging     # выбрать кластер
#   NS=u-adavydov ./deploy.sh dal2.staging
#
# Требуется: VPN, helm, kube-контекст (из staging-скриптов nyc/dal2).
set -eu

CTX="${1:-dal2.staging}"
NS="${NS:-u-adavydov}"
HERE="$(cd "$(dirname "$0")" && pwd)"

export HELM_DRIVER=configmap

VALUES_LOCAL=""
if [ -f "$HERE/values-local.yaml" ]; then
  VALUES_LOCAL="-f $HERE/values-local.yaml"
fi

# shellcheck disable=SC2086
helm upgrade --install activity-dashboard "$HERE/activity-dashboard" \
  --namespace "$NS" \
  --kube-context "$CTX" \
  -f "$HERE/activity-dashboard/values.yaml" \
  $VALUES_LOCAL

echo
echo "Deployed. Check with:"
echo "  HELM_DRIVER=configmap helm -n $NS --kube-context $CTX status activity-dashboard"
echo "  kubectl -n $NS --context $CTX get pods"
