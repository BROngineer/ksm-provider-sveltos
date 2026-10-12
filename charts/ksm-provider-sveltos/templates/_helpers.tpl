{{- define "ksm-provider-sveltos.fullname" -}}
{{- printf "%s" .Release.Name | trunc 63 | trimSuffix "-" -}}
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
{{- printf "https://%s.%s.svc:%v" (include "ksm-provider-sveltos.fullname" .) .Release.Namespace .Values.service.port -}}
{{- end -}}
