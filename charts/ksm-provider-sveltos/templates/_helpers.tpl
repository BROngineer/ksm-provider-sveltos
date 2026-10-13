{{- define "ksm-provider-sveltos.fullname" -}}
{{- default .Release.Name .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "ksm-provider-sveltos.labels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "ksm-provider-sveltos.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "ksm-provider-sveltos.tlsSecretName" -}}
{{- if .Values.tls.certManager -}}
{{- printf "%s-tls" (include "ksm-provider-sveltos.fullname" .) -}}
{{- else -}}
{{- required "tls.secretName is required when tls.certManager is false" .Values.tls.secretName -}}
{{- end -}}
{{- end -}}

{{- define "ksm-provider-sveltos.endpoint" -}}
{{- $scheme := ternary "http" "https" .Values.tls.insecure -}}
{{- printf "%s://%s.%s.svc:%v" $scheme (include "ksm-provider-sveltos.fullname" .) .Release.Namespace .Values.service.port -}}
{{- end -}}
