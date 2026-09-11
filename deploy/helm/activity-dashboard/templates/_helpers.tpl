{{/*
Standard helpers — name, labels. Зеркалит incidentbot/dashboard/hub-admin.
*/}}
{{- define "dashboard.name" -}}
activity-dashboard
{{- end }}

{{/*
Operational labels: service / team / subservice обязательны на каждом поде —
по ним ищутся логи, роутятся алерты и строится карточка в CMDB.
*/}}
{{- define "dashboard.opsLabels" -}}
service: {{ .Values.dashboard.ops.service | quote }}
team: {{ .Values.dashboard.ops.team | quote }}
subservice: {{ .Values.dashboard.ops.subservice | quote }}
environment: {{ .Values.dashboard.ops.environment | quote }}
{{- end }}

{{- define "dashboard.labels" -}}
app.kubernetes.io/name: {{ include "dashboard.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: activity-dashboard
app.kubernetes.io/version: {{ .Values.dashboard.image.tag | quote }}
{{ include "dashboard.opsLabels" . }}
{{- end }}

{{- define "dashboard.selectorLabels" -}}
app.kubernetes.io/name: {{ include "dashboard.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Имя Secret с учётными данными, либо "" когда Secret'а нет вовсе (личный
namespace, insecureInConfigMap). Либо существующий Secret — production-путь,
проецируется ExternalSecret'ом из хранилища секретов — либо chart-managed.
*/}}
{{- define "dashboard.secretName" -}}
{{- if .Values.dashboard.secrets.existingSecret -}}
{{ .Values.dashboard.secrets.existingSecret }}
{{- else if .Values.dashboard.secrets.create -}}
{{ include "dashboard.name" . }}-secrets
{{- end -}}
{{- end }}
