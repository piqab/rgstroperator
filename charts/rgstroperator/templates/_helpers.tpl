{{- define "rgstroperator.name" -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "rgstroperator.serviceAccount" -}}
{{- default (include "rgstroperator.name" .) .Values.serviceAccount.name -}}
{{- end -}}

{{- define "rgstroperator.labels" -}}
app.kubernetes.io/name: rgstroperator
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end -}}

{{- define "rgstroperator.selector" -}}
app.kubernetes.io/name: rgstroperator
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
