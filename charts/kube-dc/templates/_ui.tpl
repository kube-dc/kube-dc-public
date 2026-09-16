{{- define "kube-dc.ui.image" -}}
{{- $image := .image -}}
{{- if $image.digest -}}
{{- if not (regexMatch "^sha256:[a-f0-9]{64}$" $image.digest) }}{{ fail "image.digest must be a sha256 digest" }}{{ end -}}
{{- printf "%s@%s" $image.repository $image.digest -}}
{{- else -}}
{{- printf "%s:%s" $image.repository ($image.tag | default .appVersion) -}}
{{- end -}}
{{- end -}}

{{- define "kube-dc.ui.runtimeConfig" -}}
{{- $r := .root -}}
{{- $admin := eq .application "admin" -}}
{{- $console := printf "https://%s/" (required "ui.enabled requires frontend.gateway.hostname" $r.Values.frontend.gateway.hostname) -}}
{{- $adminUrl := printf "https://%s/" ($r.Values.adminFrontend.gateway.hostname | default (printf "admin.%s" (trimPrefix "console." $r.Values.frontend.gateway.hostname))) -}}
{{- $sso := eq (printf "%v" $r.Values.manager.keycloakSecret.ssoEnabled) "true" -}}
{{- $google := and $sso (not (empty $r.Values.manager.keycloakSecret.googleClientId)) (not (empty $r.Values.manager.keycloakSecret.googleClientSecret)) -}}
{{- dict "schemaVersion" 1 "application" .application "environment" $r.Values.ui.environment "environmentLabel" $r.Values.ui.environmentLabel "basePath" "/" "apiBaseUrl" "/api/ui/v1" "keycloakBaseUrl" $r.Values.manager.keycloakSecret.url "clientId" (ternary "kube-dc-admin-console" "kube-dc" $admin) "defaultOrganization" $r.Values.ui.defaultOrganization "consoleUrl" $console "adminUrl" $adminUrl "buildLabel" $r.Values.ui.buildLabel "partnerApiBaseUrl" (printf "https://%s/api/public/v1" $r.Values.backend.gateway.hostname) "kubeDcDomain" (trimPrefix "console." $r.Values.frontend.gateway.hostname) "managedServices" $r.Values.ui.managedServices "ssoEnabled" $sso "googleSsoEnabled" $google "ssoClientId" "sso-broker" | toJson -}}
{{- end -}}
