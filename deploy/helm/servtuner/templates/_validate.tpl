{{/*
All value validation lives here and is included once, from validate.yaml. Errors are
collected so one render reports every contradiction. Transport modes never mix on a
connection and are never inferred.
*/}}
{{- define "servtuner.validate" -}}
{{- $errors := list -}}
{{- $values := .Values -}}
{{- $root := . -}}
{{- $tls := $values.tls -}}

{{- /* tls.mode, tls.source, tls.hops.* */ -}}
{{- if not (has $tls.mode (list "mtls" "platform")) -}}
  {{- $errors = append $errors (printf "tls.mode %q is unknown; use mtls or platform" $tls.mode) -}}
{{- end -}}
{{- if not (has $tls.source (list "cert-manager" "secret")) -}}
  {{- $errors = append $errors (printf "tls.source %q is unknown; use cert-manager or secret" $tls.source) -}}
{{- end -}}
{{- range $hop, $mode := $tls.hops -}}
  {{- if not (has $mode (list "" "mtls" "platform")) -}}
    {{- $errors = append $errors (printf "tls.hops.%s %q is unknown; use mtls, platform, or empty to inherit tls.mode" $hop $mode) -}}
  {{- end -}}
{{- end -}}

{{- if and (has $tls.mode (list "mtls" "platform")) (has $tls.source (list "cert-manager" "secret")) -}}
  {{- /* The api has one listener, so its two inbound hops must agree. */ -}}
  {{- if ne (include "servtuner.hopMode" (dict "root" $root "hop" "benchApi")) (include "servtuner.hopMode" (dict "root" $root "hop" "ingressApi")) -}}
    {{- $errors = append $errors "tls.hops.benchApi and tls.hops.ingressApi must be the same mode: the api has a single listener" -}}
  {{- end -}}

  {{- /* Cert Secrets set for a role no mtls hop uses (for example platform with Secrets). */ -}}
  {{- range $role, $secretName := $tls.secrets -}}
    {{- if and $secretName (not (include "servtuner.roleNeeded" (dict "root" $root "role" $role))) -}}
      {{- $errors = append $errors (printf "tls.secrets.%s is set but no mtls hop uses it (platform mode must not carry certificate Secrets)" $role) -}}
    {{- end -}}
  {{- end -}}
  {{- if and $tls.caSecretRef (not (include "servtuner.anyMtls" $root)) -}}
    {{- $errors = append $errors "tls.caSecretRef is set but every hop is platform (platform mode must not carry certificate Secrets)" -}}
  {{- end -}}

  {{- /* mtls hops need certificates from the chosen source. */ -}}
  {{- if eq $tls.source "secret" -}}
    {{- range $role := list "api" "bench" "ingress" "postgres" "postgresReplication" -}}
      {{- if and (include "servtuner.roleNeeded" (dict "root" $root "role" $role)) (not (get $tls.secrets $role)) -}}
        {{- $errors = append $errors (printf "tls.source is secret and a mtls hop needs tls.secrets.%s, which is empty" $role) -}}
      {{- end -}}
    {{- end -}}
  {{- else if include "servtuner.anyMtls" $root -}}
    {{- if not $tls.certManager.issuerRef.name -}}
      {{- $errors = append $errors "tls.source is cert-manager and a hop is mtls, so tls.certManager.issuerRef.name is required" -}}
    {{- end -}}
  {{- end -}}
{{- end -}}

{{- /* Storage */ -}}
{{- if not (has $values.storage.backend (list "file" "s3")) -}}
  {{- $errors = append $errors (printf "storage.backend %q is unknown; use file or s3" $values.storage.backend) -}}
{{- else if eq $values.storage.backend "file" -}}
  {{- $pvc := $values.storage.file.pvc -}}
  {{- if and (not $pvc.create) (not $pvc.existingClaim) -}}
    {{- $errors = append $errors "storage.backend is file but there is no PVC: set storage.file.pvc.create=true or storage.file.pvc.existingClaim (an emptyDir would lose every run)" -}}
  {{- end -}}
  {{- if and $pvc.create $pvc.existingClaim -}}
    {{- $errors = append $errors "storage.file.pvc.create and storage.file.pvc.existingClaim are both set; choose one" -}}
  {{- end -}}
  {{- if not (has $pvc.accessMode (list "ReadWriteMany" "ReadWriteOnce")) -}}
    {{- $errors = append $errors (printf "storage.file.pvc.accessMode %q is unknown; use ReadWriteMany or ReadWriteOnce" $pvc.accessMode) -}}
  {{- end -}}
  {{- if include "servtuner.hasSeparateRunsNamespace" $root -}}
    {{- if $pvc.create -}}
      {{- $errors = append $errors (printf "storage.file.pvc.create renders one claim in the release namespace, but bench Jobs run in %q: leave namespaces.runs empty, or use storage.file.pvc.existingClaim" (include "servtuner.runsNamespace" $root)) -}}
    {{- else if ne $pvc.accessMode "ReadWriteMany" -}}
      {{- $errors = append $errors "a separate runs namespace with the file backend needs storage.file.pvc.accessMode=ReadWriteMany: a ReadWriteOnce volume cannot be shared across namespaces" -}}
    {{- end -}}
  {{- end -}}
{{- else -}}
  {{- $s3 := $values.storage.s3 -}}
  {{- if not $s3.bucket -}}{{- $errors = append $errors "storage.backend is s3 but storage.s3.bucket is empty" -}}{{- end -}}
  {{- if not (has $s3.credentials.source (list "secret" "ambient")) -}}
    {{- $errors = append $errors (printf "storage.s3.credentials.source %q is unknown; use secret or ambient" $s3.credentials.source) -}}
  {{- else if and (eq $s3.credentials.source "secret") (not $s3.credentials.secretRef.name) -}}
    {{- $errors = append $errors "storage.s3.credentials.source is secret but secretRef.name is empty" -}}
  {{- end -}}
{{- end -}}

{{- /* Runner */ -}}
{{- if not (has $values.runner (list "kubernetes" "subprocess")) -}}
  {{- $errors = append $errors (printf "runner %q is unknown; use kubernetes or subprocess" $values.runner) -}}
{{- end -}}
{{- if not (has $values.bench.targetAntiAffinity (list "required" "preferred")) -}}
  {{- $errors = append $errors (printf "bench.targetAntiAffinity %q is unknown; use required or preferred" $values.bench.targetAntiAffinity) -}}
{{- end -}}

{{- /* Ingress */ -}}
{{- if $values.ingress.enabled -}}
  {{- if not $values.ingress.host -}}
    {{- $errors = append $errors "ingress.enabled is true but ingress.host is empty" -}}
  {{- end -}}
  {{- if and $values.ingress.apiPath (not (regexMatch "^(/[A-Za-z0-9._~-]+)+$" $values.ingress.apiPath)) -}}
    {{- $errors = append $errors (printf "ingress.apiPath %q must be a path such as /api (never /)" $values.ingress.apiPath) -}}
  {{- end -}}
{{- end -}}

{{- if $values.ingress.enabled -}}
  {{- /* The chart owns the backend TLS annotations: a user value must never weaken them. */ -}}
  {{- range $key, $_ := $values.ingress.annotations -}}
    {{- if or (hasSuffix "/backend-protocol" $key) (contains "/proxy-ssl-" $key) -}}
      {{- $errors = append $errors (printf "ingress.annotations.%s is set by the chart from tls.hops.ingressApi; remove it" $key) -}}
    {{- end -}}
  {{- end -}}
  {{- /* An api path equal to, or a prefix of, the UI path would shadow the UI. */ -}}
  {{- $uiPath := include "servtuner.uiPath" $root -}}
  {{- $apiPath := include "servtuner.apiPath" $root -}}
  {{- if and $values.ingress.apiPath (or (eq $uiPath $apiPath) (hasPrefix (printf "%s/" $apiPath) $uiPath)) -}}
    {{- $errors = append $errors (printf "ingress.apiPath %q is equal to or a prefix of the UI path %q (frontend.basePath), which would shadow the UI" $apiPath $uiPath) -}}
  {{- end -}}
{{- end -}}

{{- /* Postgres */ -}}
{{- if not (has $values.postgres.mode (list "cnpg" "external")) -}}
  {{- $errors = append $errors (printf "postgres.mode %q is unknown; use cnpg or external" $values.postgres.mode) -}}
{{- else if eq $values.postgres.mode "cnpg" -}}
  {{- if not (.Capabilities.APIVersions.Has "postgresql.cnpg.io/v1") -}}
    {{- $errors = append $errors "postgres.mode is cnpg but postgresql.cnpg.io/v1 is not available: install the CloudNativePG operator, or set postgres.mode=external" -}}
  {{- end -}}
  {{- /* The operator's default image names a public registry the site cannot reach. */ -}}
  {{- if and $values.global.imageRegistry (not $values.postgres.cnpg.imageName) -}}
    {{- $errors = append $errors "global.imageRegistry is set but postgres.cnpg.imageName is empty: name the Postgres image, or the operator pulls its default from a public registry" -}}
  {{- end -}}
{{- else if not $values.postgres.external.host -}}
  {{- $errors = append $errors "postgres.mode is external but postgres.external.host is empty" -}}
{{- end -}}

{{- /* subprocess.secrets: mounted into the api pod, so meaningless under another runner. */ -}}
{{- if and $values.subprocess.secrets (ne $values.runner "subprocess") -}}
  {{- $errors = append $errors "subprocess.secrets is set but runner is not subprocess: the Kubernetes runner mounts a run's Secrets into its Job itself" -}}
{{- end -}}
{{- range $values.subprocess.secrets -}}
  {{- if not (regexMatch "^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$" (toString .)) -}}
    {{- $errors = append $errors (printf "subprocess.secrets: %q is not a Secret name" (toString .)) -}}
  {{- end -}}
{{- end -}}

{{- if not (has $values.secretStore (list "kubernetes" "none")) -}}
  {{- $errors = append $errors (printf "secretStore %q is unknown; use kubernetes or none" $values.secretStore) -}}
{{- end -}}

{{- /* gateway */ -}}
{{- $gateway := $values.gateway -}}
{{- if $gateway.enabled -}}
  {{- if $values.ingress.enabled -}}
    {{- $errors = append $errors "gateway.enabled and ingress.enabled are both set: route through one of them" -}}
  {{- end -}}
  {{- if not $gateway.parentRefs -}}
    {{- $errors = append $errors "gateway.parentRefs is empty: name the Gateway the routes attach to" -}}
  {{- end -}}
  {{- if not $gateway.hostname -}}
    {{- $errors = append $errors "gateway.hostname is required when gateway.enabled" -}}
  {{- end -}}
  {{- if not ($root.Capabilities.APIVersions.Has "gateway.networking.k8s.io/v1") -}}
    {{- $errors = append $errors "gateway.enabled but gateway.networking.k8s.io/v1 is not available: install a Gateway API implementation (Envoy Gateway)" -}}
  {{- end -}}
  {{- if and (eq (include "servtuner.hopMode" (dict "root" $root "hop" "ingressApi")) "mtls") (not ($root.Capabilities.APIVersions.Has "gateway.envoyproxy.io/v1alpha1")) -}}
    {{- $errors = append $errors "gateway.enabled in mtls needs Envoy Gateway's gateway.envoyproxy.io/v1alpha1 (Backend carries the gateway's client certificate)" -}}
  {{- end -}}
  {{- with $gateway.envoy.extAuth -}}
  {{- if and .enabled (or (not .service.name) (not .service.port)) -}}
    {{- $errors = append $errors "gateway.envoy.extAuth.enabled needs service.name and service.port" -}}
  {{- end -}}
  {{- end -}}
{{- end -}}

{{- /* auth */ -}}
{{- $auth := $values.auth -}}
{{- if not (has $auth.mode (list "local" "none")) -}}
  {{- $errors = append $errors (printf "auth.mode %q is unknown; use local or none" $auth.mode) -}}
{{- end -}}
{{- if and (eq $auth.mode "local") (not $auth.methods) -}}
  {{- $errors = append $errors "auth.methods is empty: auth.mode local needs local, forward_header or both" -}}
{{- end -}}
{{- range $auth.methods -}}
  {{- if not (has . (list "local" "forward_header")) -}}
    {{- $errors = append $errors (printf "auth.methods: %q is unknown; use local or forward_header" .) -}}
  {{- end -}}
{{- end -}}
{{- if not (has $auth.forwardHeader.provisioning (list "refuse" "viewer")) -}}
  {{- $errors = append $errors (printf "auth.forwardHeader.provisioning %q is unknown; use refuse or viewer" $auth.forwardHeader.provisioning) -}}
{{- end -}}
{{- if not $auth.forwardHeader.userHeader -}}
  {{- $errors = append $errors "auth.forwardHeader.userHeader is empty" -}}
{{- end -}}
{{- if gt (int $auth.session.idleTimeoutSeconds) (int $auth.session.absoluteLifetimeSeconds) -}}
  {{- $errors = append $errors "auth.session.idleTimeoutSeconds may not exceed auth.session.absoluteLifetimeSeconds" -}}
{{- end -}}

{{- if $errors -}}
{{- fail (printf "invalid servtuner values:\n  - %s" (join "\n  - " $errors)) -}}
{{- end -}}
{{- end -}}
