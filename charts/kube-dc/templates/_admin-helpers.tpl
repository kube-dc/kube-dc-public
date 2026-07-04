{{/*
Admin-frontend (superadmin console) naming helpers — mirrors the frontend helpers.
With release name "kube-dc" these resolve to "kube-dc-admin-frontend", matching the
deployment name the dev-build / release tooling targets.
*/}}
{{- define "kube-dc.adminFrontend.name" -}}
{{- printf "%s-admin-frontend" (default .Chart.Name .Values.nameOverride) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "kube-dc.adminFrontend.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- printf "%s-admin-frontend" (.Values.fullnameOverride | trunc 47 | trimSuffix "-") }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- printf "%s-admin-frontend" (.Release.Name | trunc 47 | trimSuffix "-") }}
{{- else }}
{{- printf "%s-%s-admin-frontend" (.Release.Name | trunc 30 | trimSuffix "-") ($name | trunc 16 | trimSuffix "-") }}
{{- end }}
{{- end }}
{{- end }}

{{/*
selectorLabels: deliberately just `app: <fullname>` (= app: kube-dc-admin-frontend)
to MATCH the pre-existing standalone deployment's immutable spec.selector, so Helm
can adopt the live object instead of failing on an immutable-selector change. The
dev-build/dagger pod cleanup (`-l app=kube-dc-admin-frontend`) also relies on it.
*/}}
{{- define "kube-dc.adminFrontend.selectorLabels" -}}
app: {{ include "kube-dc.adminFrontend.fullname" . }}
{{- end }}

{{- define "kube-dc.adminFrontend.labels" -}}
helm.sh/chart: {{ include "kube-dc.chart" . }}
{{ include "kube-dc.adminFrontend.selectorLabels" . }}
app.kubernetes.io/name: {{ include "kube-dc.adminFrontend.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: admin-frontend
{{- end }}

{{- define "kube-dc.adminFrontend.serviceAccountName" -}}
{{- default (include "kube-dc.adminFrontend.fullname" .) .Values.adminFrontend.serviceAccount.name }}
{{- end }}
