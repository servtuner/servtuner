{{/*
All value validation lives here and is included once, from validate.yaml. Errors are
collected so one render reports every contradiction. TLS and plain settings never mix on a
connection and are never inferred.
*/}}
{{- define "servtuner.validate" -}}
{{- $errors := list -}}
{{- $values := .Values -}}
{{- $root := . -}}
{{- $tls := $values.tls -}}

{{- /* tls.enabled, tls.clientAuth, tls.source, tls.hops.* */ -}}
{{- $tlsValid := true -}}
{{- if hasKey $tls "mode" -}}
  {{- $tlsValid = false -}}
  {{- $errors = append $errors (printf "tls.mode %q was replaced in servtuner 0.2: set tls.enabled (true or false) and tls.clientAuth (required, optional or none)" (toString $tls.mode)) -}}
{{- end -}}
{{- if not (kindIs "bool" $tls.enabled) -}}
  {{- $tlsValid = false -}}
  {{- $errors = append $errors (printf "tls.enabled must be true or false, not %q" (toString $tls.enabled)) -}}
{{- end -}}
{{- if not (has $tls.clientAuth (list "required" "optional" "none")) -}}
  {{- $tlsValid = false -}}
  {{- $errors = append $errors (printf "tls.clientAuth %q is unknown; use required, optional or none" (toString $tls.clientAuth)) -}}
{{- end -}}
{{- if not (has $tls.source (list "cert-manager" "secret")) -}}
  {{- $tlsValid = false -}}
  {{- $errors = append $errors (printf "tls.source %q is unknown; use cert-manager or secret" $tls.source) -}}
{{- end -}}
{{- $listener := include "servtuner.apiListenerHops" $root | fromJsonArray -}}
{{- range $hop, $setting := $tls.hops -}}
  {{- if kindIs "string" $setting -}}
    {{- $tlsValid = false -}}
    {{- $errors = append $errors (printf "tls.hops.%s %q was replaced in servtuner 0.2: use {enabled: true|false}%s" $hop $setting (ternary "" ", with clientCert: true|false" (has $hop $listener))) -}}
  {{- else if not (kindIs "map" $setting) -}}
    {{- $tlsValid = false -}}
    {{- $errors = append $errors (printf "tls.hops.%s must be a map: {enabled, clientCert}" $hop) -}}
  {{- else -}}
    {{- range $key, $value := $setting -}}
      {{- if not (has $key (list "enabled" "clientCert")) -}}
        {{- $tlsValid = false -}}
        {{- $errors = append $errors (printf "tls.hops.%s.%s is unknown; a hop takes enabled and clientCert" $hop $key) -}}
      {{- else if not (kindIs "bool" $value) -}}
        {{- $tlsValid = false -}}
        {{- $errors = append $errors (printf "tls.hops.%s.%s must be true or false" $hop $key) -}}
      {{- end -}}
    {{- end -}}
    {{- if and (hasKey $setting "clientCert") (has $hop $listener) -}}
      {{- $tlsValid = false -}}
      {{- $errors = append $errors (printf "tls.hops.%s.clientCert: this hop reaches the api's own listener, so tls.clientAuth decides" $hop) -}}
    {{- end -}}
  {{- end -}}
{{- end -}}

{{- if $tlsValid -}}
  {{- range $hop, $setting := $tls.hops -}}
    {{- if and (eq (toString (get $setting "clientCert")) "true") (not (include "servtuner.hopTls" (dict "root" $root "hop" $hop))) -}}
      {{- $errors = append $errors (printf "tls.hops.%s.clientCert is true but the hop does not use TLS" $hop) -}}
    {{- end -}}
  {{- end -}}
  {{- /* The api has one listener, so its two inbound hops must agree. */ -}}
  {{- if ne (include "servtuner.hopTls" (dict "root" $root "hop" "benchApi")) (include "servtuner.hopTls" (dict "root" $root "hop" "ingressApi")) -}}
    {{- $errors = append $errors "tls.hops.benchApi.enabled and tls.hops.ingressApi.enabled must agree: the api has a single listener" -}}
  {{- end -}}

  {{- /* Cert Secrets set for a role no hop uses (for example with TLS off). */ -}}
  {{- range $role, $secretName := $tls.secrets -}}
    {{- if and $secretName (not (include "servtuner.roleNeeded" (dict "root" $root "role" $role))) -}}
      {{- $errors = append $errors (printf "tls.secrets.%s is set but no hop uses it (a hop without TLS must not carry certificate Secrets)" $role) -}}
    {{- end -}}
  {{- end -}}
  {{- if and $tls.caSecretRef (not (include "servtuner.anyTls" $root)) -}}
    {{- $errors = append $errors "tls.caSecretRef is set but no hop uses TLS (a hop without TLS must not carry certificate Secrets)" -}}
  {{- end -}}

  {{- /* Hops with TLS need certificates from the chosen source. */ -}}
  {{- if eq $tls.source "secret" -}}
    {{- range $role := list "api" "bench" "ingress" "postgres" "postgresReplication" -}}
      {{- if and (include "servtuner.roleNeeded" (dict "root" $root "role" $role)) (not (get $tls.secrets $role)) -}}
        {{- $errors = append $errors (printf "tls.source is secret and a hop with TLS needs tls.secrets.%s, which is empty" $role) -}}
      {{- end -}}
    {{- end -}}
  {{- else if include "servtuner.anyTls" $root -}}
    {{- if not $tls.certManager.issuerRef.name -}}
      {{- $errors = append $errors "tls.source is cert-manager and a hop uses TLS, so tls.certManager.issuerRef.name is required" -}}
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
  {{- if and (include "servtuner.hopTls" (dict "root" $root "hop" "ingressApi")) (not ($root.Capabilities.APIVersions.Has "gateway.envoyproxy.io/v1alpha1")) -}}
    {{- $errors = append $errors "gateway.enabled with TLS to the api needs Envoy Gateway's gateway.envoyproxy.io/v1alpha1 (its Backend carries the TLS settings and the gateway's client certificate)" -}}
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
