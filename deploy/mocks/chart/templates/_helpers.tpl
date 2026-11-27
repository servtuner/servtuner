{{- define "openaiMock.name" -}}openai-mock{{- end }}

{{- define "openaiMock.labels" -}}
app.kubernetes.io/name: {{ include "openaiMock.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "openaiMock.selectorLabels" -}}
app.kubernetes.io/name: {{ include "openaiMock.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "openaiMock.isMtls" -}}{{ if eq .Values.tls.mode "mtls" }}true{{ end }}{{- end }}

{{- define "openaiMock.tlsSecretName" -}}{{ include "openaiMock.name" . }}-tls{{- end }}

{{- define "openaiMock.serviceHost" -}}{{ include "openaiMock.name" . }}.{{ .Release.Namespace }}.svc{{- end }}
