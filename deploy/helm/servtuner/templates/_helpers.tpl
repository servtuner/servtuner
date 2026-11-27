{{/* Names and labels */}}
{{- define "servtuner.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 40 | trimSuffix "-" -}}
{{- end -}}

{{- define "servtuner.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 40 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 40 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 40 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "servtuner.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
app.kubernetes.io/name: {{ include "servtuner.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/* Pass (dict "root" . "component" "api"). */}}
{{- define "servtuner.selectorLabels" -}}
app.kubernetes.io/name: {{ include "servtuner.name" .root }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{/* Pass (dict "root" . "image" .Values.api.image). A digest wins over the tag; the tag defaults to the
chart appVersion. global.imageRegistry applies (servtuner.withRegistry). */}}
{{- define "servtuner.image" -}}
{{- $repository := include "servtuner.withRegistry" (dict "root" .root "ref" .image.repository) -}}
{{- if .image.digest -}}
{{- printf "%s@%s" $repository .image.digest -}}
{{- else -}}
{{- printf "%s:%s" $repository (default .root.Chart.AppVersion .image.tag) -}}
{{- end -}}
{{- end -}}

{{/* (dict "root" . "ref" "quay.io/org/name:tag"): the ref under global.imageRegistry, or unchanged
without one. The first path component is a registry host when it has a "." or ":" or is "localhost"
(the Docker reference rules); only that component is replaced, so the repository path is kept. */}}
{{- define "servtuner.withRegistry" -}}
{{- $registry := trimSuffix "/" (.root.Values.global.imageRegistry | default "") -}}
{{- if $registry -}}
{{- $parts := splitList "/" .ref -}}
{{- $first := first $parts -}}
{{- if and (gt (len $parts) 1) (or (contains "." $first) (contains ":" $first) (eq $first "localhost")) -}}
{{- $parts = rest $parts -}}
{{- end -}}
{{- printf "%s/%s" $registry (join "/" $parts) -}}
{{- else -}}
{{- .ref -}}
{{- end -}}
{{- end -}}

{{/* global.imagePullSecrets then imagePullSecrets, each name once, as a list of names. */}}
{{- define "servtuner.imagePullSecretNames" -}}
{{- $names := list -}}
{{- range concat (.Values.global.imagePullSecrets | default list) (.Values.imagePullSecrets | default list) -}}
{{- $names = append $names .name -}}
{{- end -}}
{{- join "," (uniq $names) -}}
{{- end -}}

{{/* The pod-spec imagePullSecrets block, or nothing. */}}
{{- define "servtuner.imagePullSecrets" -}}
{{- with include "servtuner.imagePullSecretNames" . }}
imagePullSecrets:
{{- range splitList "," . }}
  - name: {{ . }}
{{- end }}
{{- end }}
{{- end -}}

{{/* The ingress path of the UI: frontend.basePath without its trailing slash, "/" at the root. */}}
{{- define "servtuner.uiPath" -}}
{{- $path := trimSuffix "/" .Values.frontend.basePath -}}
{{- if hasPrefix "/" $path -}}{{ $path }}{{- else if $path -}}/{{ $path }}{{- else -}}/{{- end -}}
{{- end -}}

{{/*
The ingress path prefix of the api. Default: "api" under the UI's base path, so the image's relative
config.json default ("api", resolved against the base URI) reaches it under any context path.
*/}}
{{- define "servtuner.apiPath" -}}
{{- if .Values.ingress.apiPath -}}{{ .Values.ingress.apiPath }}{{- else -}}{{ trimSuffix "/" (include "servtuner.uiPath" .) }}/api{{- end -}}
{{- end -}}

{{/*
The api URL written to the UI's config.json; empty keeps the image's own relative default. Written only
when set explicitly: frontend.apiBaseUrl, or an explicit ingress.apiPath (which may lie outside the base path).
*/}}
{{- define "servtuner.frontendApiBaseUrl" -}}
{{- if .Values.frontend.apiBaseUrl -}}{{ .Values.frontend.apiBaseUrl }}{{- else if and .Values.ingress.enabled .Values.ingress.apiPath -}}{{ .Values.ingress.apiPath }}{{- end -}}
{{- end -}}

{{- define "servtuner.postgresName" -}}
{{- default (printf "%s-pg" (include "servtuner.fullname" .)) .Values.postgres.cnpg.name -}}
{{- end -}}

{{/* Claim name, identical in the release and runs namespaces. */}}
{{- define "servtuner.dataClaimName" -}}
{{- default (printf "%s-data" (include "servtuner.fullname" .)) .Values.storage.file.pvc.existingClaim -}}
{{- end -}}

{{/* "true" when the data volume is ReadWriteOnce, so bench Jobs must share the api's node. */}}
{{- define "servtuner.isNodePinned" -}}
{{- if and (eq .Values.storage.backend "file") (eq .Values.storage.file.pvc.accessMode "ReadWriteOnce") -}}true{{- end -}}
{{- end -}}

{{/*
Transport security. Each connection uses TLS or not (tls.enabled, or tls.connections.<name>:
true|false). With TLS, each component presents its one certificate wherever the server will use it:
on the api's own listener (ingressApi, benchApi) unless tls.clientAuth is none, to Postgres with
postgres.auth certificate, and to every other server (whose own configuration decides whether to ask).

roleConnections is the single source of truth for which connections need each certificate role, and
how: "tls" when the connection uses TLS (a server certificate, or the Secret that carries the CA a
client verifies with), "cert" when the client presents its certificate on it. Templates and
validation never disagree.
*/}}
{{- define "servtuner.connectionNames" -}}{{ list "benchApi" "benchPrometheus" "ingressApi" "objectStore" "postgres" | toJson }}{{- end -}}

{{- define "servtuner.roleConnections" -}}
{{- dict
  "api" (list (list "ingressApi" "tls") (list "benchApi" "tls") (list "postgres" "cert") (list "objectStore" "cert"))
  "bench" (list (list "benchApi" "tls") (list "benchPrometheus" "cert") (list "objectStore" "cert"))
  "ingress" (list (list "ingressApi" "tls"))
  "postgres" (list (list "postgres" "tls"))
  "postgresReplication" (list (list "postgres" "tls"))
  "objectStore" (list (list "objectStore" "tls"))
  | toJson -}}
{{- end -}}

{{/* Pass (dict "root" . "connection" "postgres"); "true" when the connection uses TLS. */}}
{{- define "servtuner.connectionTls" -}}
{{- $connections := .root.Values.tls.connections | default dict -}}
{{- $enabled := .root.Values.tls.enabled -}}
{{- if hasKey $connections .connection -}}{{- $enabled = get $connections .connection -}}{{- end -}}
{{- if eq (toString $enabled) "true" -}}true{{- end -}}
{{- end -}}

{{/* Pass (dict "root" . "connection" "postgres"); "true" when the client presents its certificate on the
     connection: with TLS, unless the server will not use it (the api listener with clientAuth none,
     Postgres with password auth). */}}
{{- define "servtuner.connectionClientCert" -}}
{{- if include "servtuner.connectionTls" . -}}
  {{- if has .connection (list "ingressApi" "benchApi") -}}
    {{- if ne .root.Values.tls.clientAuth "none" -}}true{{- end -}}
  {{- else if eq .connection "postgres" -}}
    {{- if eq (toString .root.Values.postgres.auth) "certificate" -}}true{{- end -}}
  {{- else -}}true{{- end -}}
{{- end -}}
{{- end -}}

{{/* Pass (dict "root" . "role" "api"); "true" when some connection needs that role's certificate. */}}
{{- define "servtuner.roleNeeded" -}}
{{- $root := .root -}}
{{- $needed := false -}}
{{- range $use := get (include "servtuner.roleConnections" $root | fromJson) .role -}}
  {{- $args := dict "root" $root "connection" (index $use 0) -}}
  {{- if eq (index $use 1) "tls" -}}
    {{- if include "servtuner.connectionTls" $args -}}{{- $needed = true -}}{{- end -}}
  {{- else if include "servtuner.connectionClientCert" $args -}}{{- $needed = true -}}{{- end -}}
{{- end -}}
{{- if and $needed (hasPrefix "postgres" .role) (ne $root.Values.postgres.mode "cnpg") -}}{{- $needed = false -}}{{- end -}}
{{- if $needed -}}true{{- end -}}
{{- end -}}

{{/* "true" when any connection uses TLS, which is when pods need the CA bundle. */}}
{{- define "servtuner.anyTls" -}}
{{- $root := . -}}
{{- $found := false -}}
{{- range $connection := include "servtuner.connectionNames" $root | fromJsonArray -}}
  {{- if include "servtuner.connectionTls" (dict "root" $root "connection" $connection) -}}{{- $found = true -}}{{- end -}}
{{- end -}}
{{- if $found -}}true{{- end -}}
{{- end -}}

{{/* Pass (dict "root" . "role" "api"). cert-manager names the Secret; source: secret uses the user's name. */}}
{{- define "servtuner.tlsSecretName" -}}
{{- if eq .root.Values.tls.source "cert-manager" -}}
{{- printf "%s-%s-tls" (include "servtuner.fullname" .root) (kebabcase .role) -}}
{{- else -}}
{{- get .root.Values.tls.secrets .role -}}
{{- end -}}
{{- end -}}

{{/* Secret that carries the private CA's ca.crt. */}}
{{- define "servtuner.caSourceSecretName" -}}
{{- if .Values.tls.caSecretRef -}}
{{- .Values.tls.caSecretRef.name -}}
{{- else -}}
{{- include "servtuner.tlsSecretName" (dict "root" . "role" "api") -}}
{{- end -}}
{{- end -}}

{{/*
CA bundle: system CAs plus the private CA, written to an emptyDir, because
SSL_CERT_FILE replaces the default bundle. ONE definition, used by every pod that
needs it: add the init container, the volumes, and the mounts and env to the app container.
*/}}
{{- define "servtuner.caBundleInitContainer" -}}
name: build-ca-bundle
image: {{ include "servtuner.image" (dict "root" . "image" (dict "repository" (default .Values.api.image.repository .Values.tls.caBundle.image.repository) "tag" (default .Values.api.image.tag .Values.tls.caBundle.image.tag))) | quote }}
command:
  - sh
  - -c
  - cat "{{ .Values.tls.caBundle.systemCaFile }}" /etc/servtuner/ca-source/ca.crt > /etc/servtuner/ca/bundle.crt
securityContext:
  allowPrivilegeEscalation: false
  readOnlyRootFilesystem: true
  runAsNonRoot: true
  capabilities: {drop: [ALL]}
volumeMounts:
  - {name: ca-source, mountPath: /etc/servtuner/ca-source, readOnly: true}
  - {name: ca-bundle, mountPath: /etc/servtuner/ca}
{{- end -}}

{{/*
A volume of a Secret that carries a private key. libpq refuses a key readable by group or world
unless root owns it, and Secret volumes default to 0644, so the mode is set here: 0440 (decimal 288,
for YAML 1.1 safety) plus the pod's fsGroup lets the non-root user read it. Pass (dict "name" "tls-api" "secretName" "...").
*/}}
{{- define "servtuner.keySecretVolume" -}}
- name: {{ .name }}
  secret:
    secretName: {{ .secretName }}
    defaultMode: 288
{{- end -}}

{{- define "servtuner.caBundleVolumes" -}}
- name: ca-source
  secret:
    secretName: {{ include "servtuner.caSourceSecretName" . | quote }}
    items: [{key: ca.crt, path: ca.crt}]
- name: ca-bundle
  emptyDir: {}
{{- end -}}

{{- define "servtuner.caBundleVolumeMounts" -}}
- {name: ca-bundle, mountPath: /etc/servtuner/ca, readOnly: true}
{{- end -}}

{{- define "servtuner.caBundleEnv" -}}
- {name: SSL_CERT_FILE, value: /etc/servtuner/ca/bundle.crt}
{{- end -}}

{{/* Pass (dict "root" . "role" "api" "namespace" "ns" "commonName" ... "dnsNames" (list) "usages" (list)). */}}
{{- define "servtuner.certificate" -}}
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: {{ include "servtuner.tlsSecretName" . }}
  namespace: {{ .namespace }}
  labels:
    {{- include "servtuner.labels" .root | nindent 4 }}
spec:
  secretName: {{ include "servtuner.tlsSecretName" . }}
  commonName: {{ .commonName | quote }}
  {{- with .dnsNames }}
  dnsNames:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  usages:
    {{- toYaml .usages | nindent 4 }}
  privateKey:
    algorithm: ECDSA
    size: 256
    rotationPolicy: Always
  issuerRef:
    {{- toYaml .root.Values.tls.certManager.issuerRef | nindent 4 }}
{{- end -}}

{{/* The one place the effective runs namespace is resolved: empty means the release namespace. */}}
{{- define "servtuner.runsNamespace" -}}
{{- default .Release.Namespace .Values.namespaces.runs -}}
{{- end -}}

{{/* "true" when bench Jobs run outside the release namespace. */}}
{{- define "servtuner.hasSeparateRunsNamespace" -}}
{{- if ne (include "servtuner.runsNamespace" .) .Release.Namespace -}}true{{- end -}}
{{- end -}}

{{/* Who runs bench, and so needs bench's read-only access: the bench ServiceAccount (Jobs), and the api's
     too when the subprocess runner runs bench inside the api pod. */}}
{{- define "servtuner.benchSubjects" -}}
- {kind: ServiceAccount, name: {{ .Values.serviceAccounts.bench.name }}, namespace: {{ include "servtuner.runsNamespace" . }}}
{{- if eq .Values.runner "subprocess" }}
- {kind: ServiceAccount, name: {{ .Values.serviceAccounts.api.name }}, namespace: {{ .Release.Namespace }}}
{{- end }}
{{- end -}}

{{/* Managed targets' namespaces. `helm upgrade --reuse-values` on a release installed before managed targets existed carries
     values without rbac.tuning, so a missing block means none. */}}
{{- define "servtuner.tuningNamespaces" -}}
{{- toJson (((.Values.rbac).tuning).namespaces | default list) -}}
{{- end }}

{{/* How Prometheus finds a managed vLLM; podmonitor when the values predate it. */}}
{{- define "servtuner.tuningMonitor" -}}
{{- ((.Values.tuning).monitor | default "podmonitor") -}}
{{- end }}


{{/* The Secret holding the setup token: the user's, or the one the chart creates. */}}
{{- define "servtuner.setupTokenSecretName" -}}
{{- .Values.auth.setupToken.existingSecret | default (printf "%s-setup-token" (include "servtuner.fullname" .)) -}}
{{- end -}}
