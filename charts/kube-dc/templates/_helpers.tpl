{{- define "kube-dc.frontend.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 54 | trimSuffix "-" }}-frontend
{{- end }}

{{- define "kube-dc.backend.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 55 | trimSuffix "-" }}-backend
{{- end }}

{{- define "kube-dc.manager.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 55 | trimSuffix "-" }}-manager
{{- end }}

{{- define "kube-dc.frontend.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 54 | trimSuffix "-" }}-frontend
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 54 | trimSuffix "-" }}-frontend
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 54 | trimSuffix "-" }}-frontend
{{- end }}
{{- end }}
{{- end }}

{{- define "kube-dc.backend.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 54 | trimSuffix "-" }}-backend
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 54 | trimSuffix "-" }}-backend
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 54 | trimSuffix "-" }}-backend
{{- end }}
{{- end }}
{{- end }}

{{- define "kube-dc.manager.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 54 | trimSuffix "-" }}-manager
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 54 | trimSuffix "-" }}-manager
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 54 | trimSuffix "-" }}-manager
{{- end }}
{{- end }}
{{- end }}

{{- define "kube-dc.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}


{{- define "kube-dc.frontend.labels" -}}
helm.sh/chart: {{ include "kube-dc.chart" . }}
{{ include "kube-dc.frontend.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "kube-dc.manager.labels" -}}
helm.sh/chart: {{ include "kube-dc.chart" . }}
{{ include "kube-dc.manager.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}


{{- define "kube-dc.backend.labels" -}}
helm.sh/chart: {{ include "kube-dc.chart" . }}
{{ include "kube-dc.backend.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "kube-dc.frontend.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kube-dc.frontend.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "kube-dc.manager.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kube-dc.manager.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "kube-dc.backend.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kube-dc.backend.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "kube-dc.frontend.serviceAccountName" -}}
{{- default (include "kube-dc.frontend.fullname" .) .Values.frontend.serviceAccount.name }}
{{- end }}

{{- define "kube-dc.backend.serviceAccountName" -}}
{{- default (include "kube-dc.backend.fullname" .) .Values.backend.serviceAccount.name }}
{{- end }}

{{- define "kube-dc.manager.serviceAccountName" -}}
{{- default (include "kube-dc.manager.fullname" .) .Values.manager.serviceAccount.name }}
{{- end }}

