# Деплой activity-dashboard в Kubernetes

Helm-чарт по образцу `incidentbot-go`: самодостаточный, со встроенным
Postgres для staging и жёсткими предохранителями для production.

## Быстрый старт (личный namespace, staging)

```bash
cp values-local.yaml.example values-local.yaml   # заполнить токены
./deploy.sh                                      # dal2.staging, ns u-adavydov
./deploy.sh nyc.staging                          # другой кластер
```

`deploy.sh` сам подхватывает `values-local.yaml`, если тот лежит рядом.
`HELM_DRIVER=configmap` обязателен для каждой helm-команды в личном
namespace — там нельзя создавать Secret'ы, а Helm по умолчанию хранит
состояние релиза в Secret.

Доступ снаружи: включите `tvingress` (VPN-адрес
`activity-dashboard.<ns>.svc.<cluster>`) или `ingress`
(`activity.dal2.xstaging.tv`).

## Что где лежит

| | |
|---|---|
| `activity-dashboard/` | сам чарт (deployment, service, configmap, secret, postgres, ingress, tvingress, pdb) |
| `values-local.yaml.example` | шаблон локальных значений с токенами; копия `values-local.yaml` в .gitignore |
| `deploy.sh` | прямой helm-деплой в личный namespace |

## Секреты

- **staging**: `secrets.create=true` + значения в `secrets.data`
  (через `values-local.yaml`), либо `insecureInConfigMap=true` для личного
  namespace — тогда токены уезжают в ConfigMap (видны всем с доступом на
  чтение namespace; чарт откажет в этом режиме вне local/staging).
- **production**: только `secrets.existingSecret` — Secret, который
  ExternalSecret проецирует из хранилища секретов организации. Chart-managed
  Secret и встроенный Postgres в production запрещены на уровне шаблонов.

## База данных

`postgres.enabled=true` поднимает одиночный Postgres 16 с PVC — годится для
dev/staging. Для production укажите `POSTGRES_DSN` в Secret и выключите
встроенную базу. Схема применяется приложением при старте
(`POSTGRES_MIGRATE_ON_START=true`), отдельного шага миграции нет.

## CI

`.gitlab-ci.yml` в корне репозитория: на MR — lint + test + typecheck фронта,
на push в master — то же плюс сборка образа kaniko в
`registry-staging.xtools.tv/u-adavydov/activity-dashboard` и ручной deploy-джоб
(нужны CI-переменные `REGISTRY_USER`, `REGISTRY_PASSWORD`, `KUBECONFIG_STAGING`).

## Обязательное перед production

- `ops.team` — без него чарт не отрендерится: под без владельца некому
  эскалировать.
- `secrets.existingSecret` + `postgres.enabled=false` + managed-БД.
- Пробы ходят в `/api/health`; он отвечает 200 и при упавшей БД (состояние
  базы — в теле), так что отказ БД не роняет здоровый процесс в рестарты.
