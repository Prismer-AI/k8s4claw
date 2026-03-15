{{/*
Expand the name of the chart.
*/}}
{{- define "k8s4claw.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "k8s4claw.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "k8s4claw.labels" -}}
helm.sh/chart: {{ include "k8s4claw.chart" . }}
{{ include "k8s4claw.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: k8s4claw
{{- end }}

{{/*
Selector labels
*/}}
{{- define "k8s4claw.selectorLabels" -}}
app.kubernetes.io/name: {{ include "k8s4claw.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Chart label
*/}}
{{- define "k8s4claw.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
ServiceAccount name
*/}}
{{- define "k8s4claw.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "k8s4claw.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Webhook service name
*/}}
{{- define "k8s4claw.webhookServiceName" -}}
{{- printf "%s-webhook" (include "k8s4claw.fullname" .) }}
{{- end }}

{{/*
Webhook cert secret name
*/}}
{{- define "k8s4claw.webhookCertSecretName" -}}
{{- printf "%s-webhook-cert" (include "k8s4claw.fullname" .) }}
{{- end }}

{{/*
Whether to use cert-manager for webhook TLS
*/}}
{{- define "k8s4claw.useCertManager" -}}
{{- if .Values.webhook.certManager.enabled }}true{{- end }}
{{- end }}

{{/*
Operator image
*/}}
{{- define "k8s4claw.image" -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) }}
{{- end }}
