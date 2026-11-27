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

{{- define "openaiMock.tlsEnabled" -}}{{ if .Values.tls.enabled }}true{{ end }}{{- end }}

{{- /* Client certificates are checked (required or optional), so the mock needs the client CA. */ -}}
{{- define "openaiMock.checksClientCerts" -}}{{ if and .Values.tls.enabled (ne .Values.tls.clientAuth "none") }}true{{ end }}{{- end }}

{{- define "openaiMock.tlsSecretName" -}}{{ include "openaiMock.name" . }}-tls{{- end }}

{{- define "openaiMock.serviceHost" -}}{{ include "openaiMock.name" . }}.{{ .Release.Namespace }}.svc{{- end }}
