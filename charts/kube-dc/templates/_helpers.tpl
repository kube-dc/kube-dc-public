{{- define "kube-dc.frontend.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 54 | trimSuffix "-" }}-frontend
{{- end }}

{{- define "kube-dc.backend.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 55 | trimSuffix "-" }}-backend
{{- end }}

{{- define "kube-dc.manager.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 55 | trimSuffix "-" }}-manager
{{- end }}

{{- define "kube-dc.frontend.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 54 | trimSuffix "-" }}-frontend
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 54 | trimSuffix "-" }}-frontend
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 54 | trimSuffix "-" }}-frontend
{{- end }}
{{- end }}
{{- end }}

{{- define "kube-dc.backend.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 54 | trimSuffix "-" }}-backend
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 54 | trimSuffix "-" }}-backend
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 54 | trimSuffix "-" }}-backend
{{- end }}
{{- end }}
{{- end }}

{{- define "kube-dc.manager.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 54 | trimSuffix "-" }}-manager
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 54 | trimSuffix "-" }}-manager
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 54 | trimSuffix "-" }}-manager
{{- end }}
{{- end }}
{{- end }}

{{- define "kube-dc.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}


{{- define "kube-dc.frontend.labels" -}}
helm.sh/chart: {{ include "kube-dc.chart" . }}
{{ include "kube-dc.frontend.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "kube-dc.manager.labels" -}}
helm.sh/chart: {{ include "kube-dc.chart" . }}
{{ include "kube-dc.manager.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}


{{- define "kube-dc.backend.labels" -}}
helm.sh/chart: {{ include "kube-dc.chart" . }}
{{ include "kube-dc.backend.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "kube-dc.frontend.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kube-dc.frontend.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "kube-dc.manager.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kube-dc.manager.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "kube-dc.backend.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kube-dc.backend.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "kube-dc.frontend.serviceAccountName" -}}
{{- default (include "kube-dc.frontend.fullname" .) .Values.frontend.serviceAccount.name }}
{{- end }}

{{- define "kube-dc.backend.serviceAccountName" -}}
{{- default (include "kube-dc.backend.fullname" .) .Values.backend.serviceAccount.name }}
{{- end }}

{{- define "kube-dc.manager.serviceAccountName" -}}
{{- default (include "kube-dc.manager.fullname" .) .Values.manager.serviceAccount.name }}
{{- end }}

{{- define "kube-dc.k8manager.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 52 | trimSuffix "-" }}-k8-manager
{{- end }}

{{- define "kube-dc.k8manager.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 52 | trimSuffix "-" }}-k8-manager
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 52 | trimSuffix "-" }}-k8-manager
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 52 | trimSuffix "-" }}-k8-manager
{{- end }}
{{- end }}
{{- end }}

{{- define "kube-dc.k8manager.labels" -}}
helm.sh/chart: {{ include "kube-dc.chart" . }}
{{ include "kube-dc.k8manager.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "kube-dc.k8manager.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kube-dc.k8manager.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "kube-dc.dbmanager.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 52 | trimSuffix "-" }}-db-manager
{{- end }}

{{- define "kube-dc.dbmanager.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 52 | trimSuffix "-" }}-db-manager
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 52 | trimSuffix "-" }}-db-manager
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 52 | trimSuffix "-" }}-db-manager
{{- end }}
{{- end }}
{{- end }}

{{- define "kube-dc.dbmanager.labels" -}}
helm.sh/chart: {{ include "kube-dc.chart" . }}
{{ include "kube-dc.dbmanager.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "kube-dc.dbmanager.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kube-dc.dbmanager.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}


{{/*
Effective URL for a single OS-image catalog entry.

Inputs:
  .entry - one catalog entry from .Values.osImages.catalog
  .root  - the chart context ($), so we can read .root.Values.osImages.mirrorBaseURL

If a cluster-level mirror is configured (`osImages.mirrorBaseURL` is
non-empty) AND the entry has a `mirrorPath`, render the URL as
`<mirrorBaseURL>/<mirrorPath>` with one slash between. Otherwise fall
back to the entry's `upstreamURL`.
*/}}
{{- define "kube-dc.osImageURL" -}}
{{- $mirror := default "" .root.Values.osImages.mirrorBaseURL -}}
{{- if and $mirror .entry.mirrorPath -}}
{{ printf "%s/%s" (trimSuffix "/" $mirror) (trimPrefix "/" .entry.mirrorPath) }}
{{- else -}}
{{ .entry.upstreamURL }}
{{- end -}}
{{- end -}}

{{- /*
Pod-template labels. Deliberately NOT the full <component>.labels set:
helm.sh/chart embeds the chart VERSION, so putting it on pod templates
forces a rollout of every Deployment on every chart-only version bump
(no image/config change). Pod templates get the selectorLabels plus the
appVersion (which only moves when the shipped images move — a roll is
then correct). helm.sh/chart + managed-by stay on object metadata.
*/}}
{{- define "kube-dc.podVersionLabel" -}}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
{{- end }}


{{/*
Resolve the management-cluster API endpoint consumed by an infra-side
component. The second argument is that component's legacy value so an empty
mode remains upgrade-neutral. Explicit modes form the new fleet contract.
*/}}
{{- define "kube-dc.managementAPIEndpoint" -}}
{{- $root := index . 0 -}}
{{- $legacy := index . 1 -}}
{{- $mode := default "" $root.Values.managementAPI.mode -}}
{{- if eq $mode "" -}}
{{- $legacy -}}
{{- else if eq $mode "external" -}}
{{- required "managementAPI.externalEndpoint or kubeApiExternalUrl is required in external mode" (default $root.Values.kubeApiExternalUrl $root.Values.managementAPI.externalEndpoint) -}}
{{- else if eq $mode "platformVIP" -}}
{{- /*
  RETIRED 2026-08-07 (front-door simplification, docs/prd/
  front-door-simplification-implementation.md §1.1). This mode meant "dial the
  Fork-E kube-api MetalLB VIP:6443". No cluster ever used it, and `service`
  mode reaches the apiserver's own ClusterIP over the dual-home infra NIC —
  no VIP, no hairpin, one fewer moving part. Fail loudly rather than silently
  resolving something else: an operator who set this expects a specific
  datapath.
*/ -}}
{{- fail "managementAPI.mode=platformVIP is RETIRED. Use mode=service (reaches the apiserver ClusterIP over the dual-home infra NIC; requires manager.infraAttachment.enabled=true, managementAPI.serviceIP and .serviceCIDR) or mode=external (public kube-api hostname). See docs/prd/front-door-simplification-implementation.md" -}}
{{- else if eq $mode "service" -}}
{{- if not $root.Values.manager.infraAttachment.enabled -}}
{{- fail "managementAPI.mode=service requires manager.infraAttachment.enabled=true" -}}
{{- end -}}
{{- $serviceIP := required "managementAPI.serviceIP is required in service mode" $root.Values.managementAPI.serviceIP -}}
{{- $serviceCIDR := required "managementAPI.serviceCIDR is required in service mode" $root.Values.managementAPI.serviceCIDR -}}
{{- else -}}
{{- fail (printf "managementAPI.mode must be external or service; got %q (platformVIP is retired)" $mode) -}}
{{- end -}}
{{- end -}}

{{/*
kube-dc.manager.failClosedAdmission — "this installation depends on the
manager's admission being available". THREE features put a fail-closed webhook
on the ordinary tenant path: tenant-VLAN attachment (projectNetwork), routed
networks, and managed-services protection. They must share ONE definition:
the budget, the replica floor and the node spread were previously derived from
different subsets of them, so a managed-services-only installation got the
budget but neither a replica floor nor a spread, and both manager replicas
could land on one node while ordinary tenant Pod/PVC/Secret admission depended
on them (release-boundary review 2026-09-05, F3).
*/}}
{{- define "kube-dc.manager.failClosedAdmission" -}}
{{- if and .Values.manager.webhook.enabled (or .Values.projectNetwork.enabled .Values.routedNetwork.enabled .Values.projectPolicies.protectManagedServices.enabled) -}}
true
{{- end -}}
{{- end }}

{{/*
kube-dc.manager.highlyAvailableAdmission — fail-closed admission AND the
replicas to serve it. The HA machinery (topology spread with minDomains: 2, a
PodDisruptionBudget) is derived from this, never from the fail-closed flag
alone: on a knowingly single-replica installation minDomains: 2 makes a
rolling update's surge pod unschedulable (fewer eligible domains than
minDomains puts the global minimum at zero, so the new pod is skew 2) and the
rollout stalls forever, while the budget blocks the drain the operator already
accepted. Round 81.
*/}}
{{- define "kube-dc.manager.highlyAvailableAdmission" -}}
{{- if and (include "kube-dc.manager.failClosedAdmission" .) (ge (int .Values.manager.replicaCount) 2) -}}
true
{{- end -}}
{{- end }}

{{/*
kube-dc.reservedLabelKeysCEL — the CEL list literal of every label key a tenant
identity may not set: the platform's own markers, the built-in families'
selector keys (CNPG, MariaDB, and Strimzi's, reserved since before the
registry existed), and every selector key of a family added in
.Values.serviceFamilies.extra. One definition, used by all three policies, so
adding a family reserves its keys everywhere at once.
*/}}
{{- define "kube-dc.reservedLabelKeysCEL" -}}
{{- include "kube-dc.serviceFamilies.validate" . -}}
{{- $keys := list "services.kube-dc.com/managed-by" "services.kube-dc.com/instance-uid" "services.kube-dc.com/instance" "services.kube-dc.com/provenance" "kube-dc.com/managed-db" "cnpg.io/cluster" "cnpg.io/poolerName" "strimzi.io/cluster" -}}
{{- range .Values.serviceFamilies.extra }}
{{- range .selectorKeys }}
{{- if not (has . $keys) }}{{ $keys = append $keys . }}{{ end }}
{{- end }}
{{- end -}}
"[{{ range $i, $k := $keys }}{{ if $i }}, {{ end }}'{{ $k }}'{{ end }}]"
{{- end }}

{{- /*
kube-dc.identity.trustedEdge -- the CEL predicate over one ownerReference `o`
that the owner-forgery policy treats as a TRUSTED edge; the one definition
behind variables.ownerTrusted (exists) and every operator carve-out (all).
Two arms. The platform's own kinds and the kube-system workload kinds --
including every registered family's via kinds in core/apps/batch -- are
trusted through CONTROLLER references only (metav1.GetControllerOf; a
tenant's non-controller garbage-collection reference to their own
Deployment is theirs). The engine kinds -- CNPG Cluster, MariaDB, every
registered family's roots and its custom via kinds -- are trusted through
ANY reference: Strimzi owns its pod set through a non-controller reference
to the KafkaNodePool, the classifier follows that edge for custom kinds, and
a tenant has no business referencing a platform engine as an owner at all.
*/}}
{{- define "kube-dc.identity.trustedEdge" -}}
(o.?controller.orValue(false) && (
          (o.apiVersion == 'apps/v1' && o.kind in ['Deployment', 'ReplicaSet', 'StatefulSet'])
          || (o.apiVersion == 'batch/v1' && o.kind in ['Job', 'CronJob'])
          || (o.apiVersion == 'kamaji.clastix.io/v1alpha1' && o.kind == 'TenantControlPlane')
          || (o.apiVersion == 'k8s.kube-dc.com/v1alpha1' && o.kind in ['KdcCluster', 'KdcClusterDatastore'])
          || (o.apiVersion == 'db.kube-dc.com/v1alpha1' && o.kind == 'KdcDatabase')
{{- range .Values.serviceFamilies.extra }}{{ range .engineRoots }}{{ range .via }}{{ if has (.group | default "") (list "" "apps" "batch") }}
          || (o.apiVersion == '{{ if .group }}{{ .group }}/{{ end }}{{ .version }}' && o.kind == '{{ .kind }}')
{{- end }}{{ end }}{{ end }}{{ end }}))
          || (o.apiVersion == 'postgresql.cnpg.io/v1' && o.kind == 'Cluster')
          || (o.apiVersion == 'postgresql.cnpg.io/v1' && o.kind == 'Pooler')
          || (o.apiVersion == 'k8s.mariadb.com/v1alpha1' && o.kind == 'MariaDB')
{{- range .Values.serviceFamilies.extra }}{{ range .engineRoots }}
          || (o.apiVersion == '{{ if .group }}{{ .group }}/{{ end }}{{ .version }}' && o.kind == '{{ .kind }}')
{{- range .via }}{{ if not (has (.group | default "") (list "" "apps" "batch")) }}
          || (o.apiVersion == '{{ if .group }}{{ .group }}/{{ end }}{{ .version }}' && o.kind == '{{ .kind }}')
{{- end }}{{ end }}{{ end }}{{ end }}
{{- end }}

{{- /*
kube-dc.managedServices.exemptNamespaces -- the ServiceAccount namespaces the
managed-services policies, the child mutator and the manager's gates treat as
PLATFORM identities: .Values.projectPolicies.protectManagedServices.
exemptServiceAccountNamespaces plus the namespace of every operator a
registered family declares (system:serviceaccount:<ns>:<name> in
serviceFamilies.extra[].operatorServiceAccounts). An engine operator writes
its engine's marked children inside the project namespace -- Strimzi's pod
sets, Secrets and Services as CNPG's Jobs and Secrets -- so registering a
family is what makes its operator a platform identity for the marker
policies; the same treatment cnpg-system and mariadb-system get by hand.
Renders a JSON list; consume with `fromJsonArray`.
*/}}
{{- define "kube-dc.managedServices.exemptNamespaces" -}}
{{- $out := list -}}
{{- range .Values.projectPolicies.protectManagedServices.exemptServiceAccountNamespaces }}{{ if not (has . $out) }}{{ $out = append $out . }}{{ end }}{{ end -}}
{{- range .Values.serviceFamilies.extra }}{{ range .operatorServiceAccounts }}{{ $parts := splitList ":" . }}{{ if eq (len $parts) 4 }}{{ $ns := index $parts 2 }}{{ if not (has $ns $out) }}{{ $out = append $out $ns }}{{ end }}{{ end }}{{ end }}{{ end -}}
{{ toJson $out }}
{{- end }}

{{- /*
kube-dc.serviceFamilies.validate -- every string in .Values.serviceFamilies.extra
is rendered into CEL and YAML literals (the forgery guard's apiVersion/kind
comparisons and operator carve-outs, the webhook rules, the reserved-key list),
and every SEMANTIC rule the manager enforces when it loads the registry
(internal/servicefamily) is mirrored here: Helm installs the policies before the
new manager runs, and during a rolling upgrade an old manager and a newly
widened policy would otherwise coexist (codex round 93). A value that fails
any check stops the render. Included by every template that consumes the list.
Renders nothing.
*/}}
{{- define "kube-dc.serviceFamilies.validate" -}}
{{- $label := "[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?" -}}
{{- $subdomain := printf "%s(\\.%s)*" $label $label -}}
{{- $groupRe := printf "^(%s)?$" $subdomain -}}
{{- $versionRe := "^v[0-9]+((alpha|beta)[0-9]+)?$" -}}
{{- $kindRe := "^[A-Z][A-Za-z0-9]*$" -}}
{{- $resourceRe := "^[a-z][a-z0-9]*$" -}}
{{- $saRe := printf "^system:serviceaccount:%s:%s$" $label $subdomain -}}
{{- $labelKeyRe := printf "^(%s/)?[A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?$" $subdomain -}}
{{- $pathRe := "^[A-Za-z0-9_-]+(\\.[A-Za-z0-9_-]+)*$" -}}
{{- /* The platform's own trust graph and the built-in engines: never a root or a hop of an extra family. */ -}}
{{- /* Reserved by GROUP and KIND / RESOURCE, whatever the version: a second served version of the same CRD is the same object. */ -}}
{{- $platformRoots := list "kamaji.clastix.io/TenantControlPlane" "k8s.kube-dc.com/KdcCluster" "k8s.kube-dc.com/KdcClusterDatastore" "db.kube-dc.com/KdcDatabase" "postgresql.cnpg.io/Cluster" "k8s.mariadb.com/MariaDB" -}}
{{- $builtinOps := list "postgresql.cnpg.io/backups" "postgresql.cnpg.io/scheduledbackups" "postgresql.cnpg.io/publications" "postgresql.cnpg.io/subscriptions" "k8s.mariadb.com/backups" "k8s.mariadb.com/physicalbackups" "k8s.mariadb.com/restores" "k8s.mariadb.com/pointintimerecoveries" "k8s.mariadb.com/sqljobs" "k8s.mariadb.com/users" "k8s.mariadb.com/grants" "k8s.mariadb.com/databases" "k8s.mariadb.com/connections" "k8s.mariadb.com/maxscales" -}}
{{- $reservedSuffixes := list "kube-dc.com" "kamaji.clastix.io" "kubevirt.io" "x-k8s.io" -}}
{{- $kubeSystemGroups := list "" "apps" "batch" -}}
{{- /* Groups the reference gate judges with hand-written handlers (matched by group and resource before the registry): no extra family may declare operations or passthroughs there. */ -}}
{{- $fixedHandlerGroups := list "" "apps" "policy" "snapshot.storage.k8s.io" "objectbucket.io" "cdi.kubevirt.io" "kubevirt.io" "cert-manager.io" "k8s.mariadb.com" "postgresql.cnpg.io" -}}
{{- $names := dict -}}
{{- $roots := dict -}}
{{- $ops := dict -}}
{{- $opResources := dict -}}
{{- $customVia := dict -}}
{{- $pass := dict -}}
{{- $passExact := dict -}}
{{- /* A family is an API-group trust boundary: every custom group it touches (roots, custom vias, operations, passthroughs) is its alone. */ -}}
{{- $ownedGroups := dict "postgresql.cnpg.io" "postgresql" "k8s.mariadb.com" "mariadb" -}}
{{- /* Operator ServiceAccounts are unique across families, the built-ins' included. */ -}}
{{- $saOwners := dict "system:serviceaccount:cnpg-system:cnpg-cloudnative-pg" "postgresql" "system:serviceaccount:mariadb-system:mariadb-operator" "mariadb" -}}
{{- $deleteBy := dict -}}
{{- range .Values.serviceFamilies.extra }}
{{- $name := .name | default "" }}
{{- if not (regexMatch "^[a-z0-9]([a-z0-9-]*[a-z0-9])?$" $name) }}{{ fail (printf "serviceFamilies.extra: family name %q must be lowercase letters, digits and dashes" $name) }}{{ end }}
{{- if has $name (list "postgresql" "mariadb") }}{{ fail (printf "serviceFamilies.extra: %q is a built-in family and cannot be redeclared" $name) }}{{ end }}
{{- if hasKey $names $name }}{{ fail (printf "serviceFamilies.extra: family %q is declared twice" $name) }}{{ end }}
{{- $_ := set $names $name true }}
{{- $rootKinds := dict }}
{{- range .engineRoots }}
{{- $g := .group | default "" }}{{ $v := .version | default "" }}{{ $k := .kind | default "" }}
{{- if or (not (regexMatch $groupRe $g)) (gt (len $g) 253) }}{{ fail (printf "serviceFamilies.extra %s: engine root group %q is not a DNS subdomain" $name $g) }}{{ end }}
{{- if not (regexMatch $versionRe $v) }}{{ fail (printf "serviceFamilies.extra %s: engine root version %q is not an API version" $name $v) }}{{ end }}
{{- if not (regexMatch $kindRe $k) }}{{ fail (printf "serviceFamilies.extra %s: engine root kind %q is not a Kind" $name $k) }}{{ end }}
{{- if has $g $kubeSystemGroups }}{{ fail (printf "serviceFamilies.extra %s: engine root %s must be a custom resource, not a core/apps/batch kind" $name $k) }}{{ end }}
{{- range $reservedSuffixes }}{{ if or (eq $g .) (hasSuffix (printf ".%s" .) $g) }}{{ fail (printf "serviceFamilies.extra %s: engine root group %q is a platform API group; a family may not claim the platform's trust graph" $name $g) }}{{ end }}{{ end }}
{{- $gvk := printf "%s/%s/%s" $g $v $k }}
{{- if has (printf "%s/%s" $g $k) $platformRoots }}{{ fail (printf "serviceFamilies.extra %s: engine root %s is a platform or built-in root (in any version)" $name $gvk) }}{{ end }}
{{- $gk := printf "%s/%s" $g $k }}
{{- if and (hasKey $roots $gk) (ne (index $roots $gk) $name) }}{{ fail (printf "serviceFamilies.extra: engine root %s is declared by both %s and %s (in some version)" $gk (index $roots $gk) $name) }}{{ end }}
{{- $_ := set $roots $gk $name }}
{{- if and (hasKey $ownedGroups $g) (ne (index $ownedGroups $g) $name) }}{{ fail (printf "serviceFamilies.extra: API group %q is used by both %s and %s (engine root %s); a custom API group belongs to one family" $g (index $ownedGroups $g) $name $k) }}{{ end }}
{{- $_ := set $ownedGroups $g $name }}
{{- if hasKey $rootKinds $k }}{{ fail (printf "serviceFamilies.extra %s: engine root kind %q is declared twice (operations reference a root by kind)" $name $k) }}{{ end }}
{{- $_ := set $rootKinds $k true }}
{{- $seenVia := dict }}
{{- range .via }}
{{- $vg := .group | default "" }}{{ $vv := .version | default "" }}{{ $vk := .kind | default "" }}{{ $vr := .resource | default "" }}
{{- if or (not (regexMatch $groupRe $vg)) (gt (len $vg) 253) }}{{ fail (printf "serviceFamilies.extra %s: via kind group %q is not a DNS subdomain" $name $vg) }}{{ end }}
{{- if not (regexMatch $versionRe $vv) }}{{ fail (printf "serviceFamilies.extra %s: via kind version %q is not an API version" $name $vv) }}{{ end }}
{{- if not (regexMatch $kindRe $vk) }}{{ fail (printf "serviceFamilies.extra %s: via kind kind %q is not a Kind" $name $vk) }}{{ end }}
{{- if not (regexMatch $resourceRe $vr) }}{{ fail (printf "serviceFamilies.extra %s: via kind %s needs its plural `resource` (lowercase), got %q" $name $vk $vr) }}{{ end }}
{{- range $reservedSuffixes }}{{ if or (eq $vg .) (hasSuffix (printf ".%s" .) $vg) }}{{ fail (printf "serviceFamilies.extra %s: via kind group %q is a platform API group; a family may not walk through the platform's own objects" $name $vg) }}{{ end }}{{ end }}
{{- $vgvk := printf "%s/%s/%s" $vg $vv $vk }}
{{- if has (printf "%s/%s" $vg $vk) $platformRoots }}{{ fail (printf "serviceFamilies.extra %s: via kind %s is a platform or built-in root (in any version)" $name $vgvk) }}{{ end }}
{{- if eq $vgvk $gvk }}{{ fail (printf "serviceFamilies.extra %s: root %s lists itself as a via kind" $name $gvk) }}{{ end }}
{{- if hasKey $seenVia $vgvk }}{{ fail (printf "serviceFamilies.extra %s: via kind %s is listed twice under %s" $name $vgvk $gvk) }}{{ end }}
{{- $_ := set $seenVia $vgvk true }}
{{- if and (hasKey . "operatorCreatesChildren") (not (kindIs "bool" .operatorCreatesChildren)) }}{{ fail (printf "serviceFamilies.extra %s: via kind %s operatorCreatesChildren must be a boolean, not %q" $name $vk (toString .operatorCreatesChildren)) }}{{ end }}
{{- if and .operatorCreatesChildren (has $vg $kubeSystemGroups) }}{{ fail (printf "serviceFamilies.extra %s: via kind %s cannot set operatorCreatesChildren (its children are kube-system's; the carve-out would widen to platform-owned workloads)" $name $vk) }}{{ end }}
{{- $vgk := printf "%s/%s" $vg $vk }}
{{- if not (has $vg $kubeSystemGroups) }}
{{- if and (hasKey $customVia $vgk) (ne (index $customVia $vgk) $name) }}{{ fail (printf "serviceFamilies.extra: via kind %s is declared by both %s and %s; a custom controller kind belongs to one family" $vgk (index $customVia $vgk) $name) }}{{ end }}
{{- $_ := set $customVia $vgk $name }}
{{- if and (hasKey $ownedGroups $vg) (ne (index $ownedGroups $vg) $name) }}{{ fail (printf "serviceFamilies.extra: API group %q is used by both %s and %s (via kind %s); a custom API group belongs to one family" $vg (index $ownedGroups $vg) $name $vk) }}{{ end }}
{{- $_ := set $ownedGroups $vg $name }}
{{- end }}
{{- end }}
{{- end }}
{{- $familyKeys := dict }}
{{- range .selectorKeys }}
{{- /* Component lengths as k8s validates them: a prefix of at most 253, a name of at most 63 (the regex bounds the name). */ -}}
{{- $prefix := "" }}{{ if contains "/" . }}{{ $prefix = index (splitList "/" .) 0 }}{{ end }}
{{- if or (not (regexMatch $labelKeyRe .)) (gt (len $prefix) 253) }}{{ fail (printf "serviceFamilies.extra %s: selector key %q is not a valid label key" $name .) }}{{ end }}
{{- if or (hasPrefix "services.kube-dc.com/" .) (eq . "kube-dc.com/managed-db") }}{{ fail (printf "serviceFamilies.extra %s: selector key %q is a platform marker, not an operator's selector" $name .) }}{{ end }}
{{- $_ := set $familyKeys . true }}
{{- end }}
{{- range .operatorServiceAccounts }}
{{- $saName := "" }}{{ if eq (len (splitList ":" .)) 4 }}{{ $saName = index (splitList ":" .) 3 }}{{ end }}
{{- if or (not (regexMatch $saRe .)) (gt (len $saName) 253) }}{{ fail (printf "serviceFamilies.extra %s: operator %q is not system:serviceaccount:<ns>:<name>" $name .) }}{{ end }}
{{- if and (hasKey $saOwners .) (ne (index $saOwners .) $name) }}{{ fail (printf "serviceFamilies.extra: operator %s is declared by both %s and %s; an operator ServiceAccount belongs to one family" . (index $saOwners .) $name) }}{{ end }}
{{- $_ := set $saOwners . $name }}
{{- end }}
{{- $familyOps := dict }}
{{- range .passthroughResources }}
{{- $pg := .group | default "" }}{{ $pv := .version | default "" }}{{ $pr := .resource | default "" }}
{{- if or (not (regexMatch $groupRe $pg)) (gt (len $pg) 253) }}{{ fail (printf "serviceFamilies.extra %s: passthrough group %q is not a DNS subdomain" $name $pg) }}{{ end }}
{{- if not (regexMatch $versionRe $pv) }}{{ fail (printf "serviceFamilies.extra %s: passthrough version %q is not an API version" $name $pv) }}{{ end }}
{{- if not (regexMatch $resourceRe $pr) }}{{ fail (printf "serviceFamilies.extra %s: passthrough resource %q is not a resource name" $name $pr) }}{{ end }}
{{- if or (has $pg $kubeSystemGroups) (has $pg $fixedHandlerGroups) (has $pg $reservedSuffixes) }}{{ fail (printf "serviceFamilies.extra %s: passthrough group %q is a core/apps/batch group or one gated by the platform's own handlers" $name $pg) }}{{ end }}
{{- range $reservedSuffixes }}{{ if hasSuffix (printf ".%s" .) $pg }}{{ fail (printf "serviceFamilies.extra %s: passthrough group %q is a platform API group" $name $pg) }}{{ end }}{{ end }}
{{- $pgr := printf "%s/%s" $pg $pr }}
{{- if has $pgr $builtinOps }}{{ fail (printf "serviceFamilies.extra %s: passthrough %s is a built-in operation (in any version)" $name $pgr) }}{{ end }}
{{- if hasKey $opResources $pgr }}{{ fail (printf "serviceFamilies.extra %s: passthrough %s is an operation of %s (in some version); a resource has one classification across versions" $name $pgr (index $opResources $pgr)) }}{{ end }}
{{- if and (hasKey $pass $pgr) (ne (index $pass $pgr) $name) }}{{ fail (printf "serviceFamilies.extra: passthrough %s is declared by both %s and %s" $pgr (index $pass $pgr) $name) }}{{ end }}
{{- $_ := set $pass $pgr $name }}
{{- $pgvr := printf "%s/%s/%s" $pg $pv $pr }}
{{- if hasKey $passExact $pgvr }}{{ fail (printf "serviceFamilies.extra %s: passthrough %s is declared twice" $name $pgvr) }}{{ end }}
{{- $_ := set $passExact $pgvr true }}
{{- if and (hasKey $ownedGroups $pg) (ne (index $ownedGroups $pg) $name) }}{{ fail (printf "serviceFamilies.extra: API group %q is used by both %s and %s (passthrough %s); a custom API group belongs to one family" $pg (index $ownedGroups $pg) $name $pr) }}{{ end }}
{{- $_ := set $ownedGroups $pg $name }}
{{- end }}
{{- range .operations }}
{{- $og := .group | default "" }}{{ $ov := .version | default "" }}{{ $or := .resource | default "" }}
{{- if or (not (regexMatch $groupRe $og)) (gt (len $og) 253) }}{{ fail (printf "serviceFamilies.extra %s: operation group %q is not a DNS subdomain" $name $og) }}{{ end }}
{{- if not (regexMatch $versionRe $ov) }}{{ fail (printf "serviceFamilies.extra %s: operation version %q is not an API version" $name $ov) }}{{ end }}
{{- if not (regexMatch $resourceRe $or) }}{{ fail (printf "serviceFamilies.extra %s: operation resource %q is not a resource name" $name $or) }}{{ end }}
{{- if or (has $og $kubeSystemGroups) (has $og $fixedHandlerGroups) (has $og $reservedSuffixes) }}{{ fail (printf "serviceFamilies.extra %s: operation group %q is a core/apps/batch group or one gated by the platform's own handlers; a family's operations live in its own API group" $name $og) }}{{ end }}
{{- range $reservedSuffixes }}{{ if hasSuffix (printf ".%s" .) $og }}{{ fail (printf "serviceFamilies.extra %s: operation group %q is a platform API group" $name $og) }}{{ end }}{{ end }}
{{- $gvr := printf "%s/%s/%s" $og $ov $or }}
{{- $ogr := printf "%s/%s" $og $or }}
{{- if has $ogr $builtinOps }}{{ fail (printf "serviceFamilies.extra %s: operation %s belongs to a built-in family (in any version)" $name $gvr) }}{{ end }}
{{- if hasKey $ops $gvr }}{{ fail (printf "serviceFamilies.extra: operation %s is declared by both %s and %s" $gvr (index $ops $gvr) $name) }}{{ end }}
{{- if and (hasKey $opResources $ogr) (ne (index $opResources $ogr) $name) }}{{ fail (printf "serviceFamilies.extra: operation resource %s is declared by both %s and %s (in some version)" $ogr (index $opResources $ogr) $name) }}{{ end }}
{{- if hasKey $pass $ogr }}{{ fail (printf "serviceFamilies.extra %s: %s is both an operation and a passthrough resource (in some version); a resource has one classification across versions" $name $ogr) }}{{ end }}
{{- $_ := set $ops $gvr $name }}
{{- $_ := set $opResources $ogr $name }}
{{- if and (hasKey $ownedGroups $og) (ne (index $ownedGroups $og) $name) }}{{ fail (printf "serviceFamilies.extra: API group %q is used by both %s and %s (operation %s); a custom API group belongs to one family" $og (index $ownedGroups $og) $name $or) }}{{ end }}
{{- $_ := set $ownedGroups $og $name }}
{{- $del := .delete | default false }}
{{- if and (hasKey $deleteBy $ogr) (ne (index $deleteBy $ogr) $del) }}{{ fail (printf "serviceFamilies.extra %s: operation %s declares delete differently across versions; deletion is judged per resource" $name $ogr) }}{{ end }}
{{- $_ := set $deleteBy $ogr $del }}
{{- if not (hasKey $rootKinds (.root | default "")) }}{{ fail (printf "serviceFamilies.extra %s: operation %s names root %q, which the family does not declare" $name $or (.root | default "")) }}{{ end }}
{{- if and (hasKey . "delete") (not (kindIs "bool" .delete)) }}{{ fail (printf "serviceFamilies.extra %s: operation %s delete must be a boolean" $name $or) }}{{ end }}
{{- if and (hasKey . "allowMissingReference") (not (kindIs "bool" .allowMissingReference)) }}{{ fail (printf "serviceFamilies.extra %s: operation %s allowMissingReference must be a boolean" $name $or) }}{{ end }}
{{- $ref := .engineRef | default "" }}
{{- if hasPrefix "label:" $ref }}
{{- $key := trimPrefix "label:" $ref }}
{{- $kprefix := "" }}{{ if contains "/" $key }}{{ $kprefix = index (splitList "/" $key) 0 }}{{ end }}
{{- if or (not (regexMatch $labelKeyRe $key)) (gt (len $kprefix) 253) }}{{ fail (printf "serviceFamilies.extra %s: operation %s engineRef label key %q is not a valid label key" $name $or $key) }}{{ end }}
{{- /* The key a tenant would set to aim an operation at an engine must be one the platform reserves (the manager enforces the same). */ -}}
{{- if not (hasKey $familyKeys $key) }}{{ fail (printf "serviceFamilies.extra %s: operation %s references its engine by label %q, which is not among the family's selectorKeys (it must be reserved)" $name $or $key) }}{{ end }}
{{- else if not (regexMatch $pathRe $ref) }}{{ fail (printf "serviceFamilies.extra %s: operation %s engineRef %q is neither a dotted field path nor label:<key>" $name $or $ref) }}
{{- end }}
{{- end }}
{{- end }}
{{- /* Second pass: a via kind of one family may not be a root of another. */ -}}
{{- range .Values.serviceFamilies.extra }}
{{- $name := .name }}
{{- range .engineRoots }}
{{- range .via }}
{{- $vgk := printf "%s/%s" (.group | default "") .kind }}
{{- if hasKey $roots $vgk }}{{ fail (printf "serviceFamilies.extra %s: via kind %s is an engine root of %s (in some version); a kind is a root or a hop, never both" $name $vgk (index $roots $vgk)) }}{{ end }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}

{{- /*
kube-dc.serviceFamilies.validateInstanceWrites -- mirrors the manager's rules for
a root's instanceServiceAccount / instanceWrites (internal/servicefamily): the
template is a DNS-label affix around exactly one {{instance}}; a write is a
status subresource of one of the family's operation resources, or a core
Secret/ConfigMap without subresource; writes need an account. The affixes and
resources render into CEL and YAML literals. Renders nothing.
*/}}
{{- define "kube-dc.serviceFamilies.validateInstanceWrites" -}}
{{- $affixRe := "^[a-z0-9-]*$" -}}
{{- $resourceRe := "^[a-z][a-z0-9]*$" -}}
{{- $groupRe := "^([a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?)*)?$" -}}
{{- range .Values.serviceFamilies.extra }}
{{- $family := . }}
{{- range .engineRoots }}
{{- $sa := .instanceServiceAccount | default "" }}
{{- if $sa }}
{{- $parts := splitList "{{instance}}" $sa }}
{{- if ne (len $parts) 2 }}{{ fail (printf "serviceFamilies.extra %s: root %s instanceServiceAccount %q must contain exactly one {{instance}}" $family.name .kind $sa) }}{{ end }}
{{- if not (regexMatch $affixRe (index $parts 0)) }}{{ fail (printf "serviceFamilies.extra %s: root %s instanceServiceAccount prefix %q is not a DNS-label affix" $family.name .kind (index $parts 0)) }}{{ end }}
{{- if not (regexMatch $affixRe (index $parts 1)) }}{{ fail (printf "serviceFamilies.extra %s: root %s instanceServiceAccount suffix %q is not a DNS-label affix" $family.name .kind (index $parts 1)) }}{{ end }}
{{- if hasPrefix "-" (index $parts 0) }}{{ fail (printf "serviceFamilies.extra %s: root %s instanceServiceAccount prefix %q may not start with a hyphen" $family.name .kind (index $parts 0)) }}{{ end }}
{{- if hasSuffix "-" (index $parts 1) }}{{ fail (printf "serviceFamilies.extra %s: root %s instanceServiceAccount suffix %q may not end with a hyphen" $family.name .kind (index $parts 1)) }}{{ end }}
{{- if gt (add (len (index $parts 0)) (len (index $parts 1))) 190 }}{{ fail (printf "serviceFamilies.extra %s: root %s instanceServiceAccount %q: prefix and suffix may total at most 190 characters" $family.name .kind $sa) }}{{ end }}
{{- end }}
{{- if and (.instanceWrites | default list) (not $sa) }}{{ fail (printf "serviceFamilies.extra %s: root %s declares instanceWrites without an instanceServiceAccount" $family.name .kind) }}{{ end }}
{{- $seenWrites := list }}
{{- range (.instanceWrites | default list) }}
{{- $g := .group | default "" }}
{{- $sub := .subresource | default "" }}
{{- $key := printf "%s/%s/%s" $g (.resource | default "") $sub }}
{{- if has $key $seenWrites }}{{ fail (printf "serviceFamilies.extra %s: instance write %s is declared twice" $family.name $key) }}{{ end }}
{{- $seenWrites = append $seenWrites $key }}
{{- if not (regexMatch $groupRe $g) }}{{ fail (printf "serviceFamilies.extra %s: instance write group %q is not an API group" $family.name $g) }}{{ end }}
{{- if not (regexMatch $resourceRe (.resource | default "")) }}{{ fail (printf "serviceFamilies.extra %s: instance write resource %q is not a resource name" $family.name (.resource | default "")) }}{{ end }}
{{- if eq $g "" }}
{{- if or $sub (not (has .resource (list "secrets" "configmaps"))) }}{{ fail (printf "serviceFamilies.extra %s: core instance write %q: only secrets and configmaps, without subresource" $family.name .resource) }}{{ end }}
{{- else }}
{{- if ne $sub "status" }}{{ fail (printf "serviceFamilies.extra %s: instance write %s/%s: outside core only a status subresource" $family.name .resource $sub) }}{{ end }}
{{- $known := false }}
{{- $wr := .resource }}
{{- range ($family.operations | default list) }}{{ if and (eq (.group | default "") $g) (eq .resource $wr) }}{{ $known = true }}{{ end }}{{ end }}
{{- if not $known }}{{ fail (printf "serviceFamilies.extra %s: instance write %s.%s/status must be one of the family's operation resources" $family.name .resource $g) }}{{ end }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}

{{- /*
kube-dc.serviceFamilies.instanceIdentityCEL -- a CEL expression true when the
request's user is shaped like a registered root's instance account in the
request's namespace ("false" when no family declares one). Shape only: the
manager's instance-write judge proves provenance by UID. Used by the webhook
stanza that routes core writes and by the policies that hand them over.
*/}}
{{- define "kube-dc.serviceFamilies.instanceIdentityCEL" -}}
{{- $terms := list -}}
{{- range .Values.serviceFamilies.extra }}{{ range .engineRoots }}{{ if and (.instanceServiceAccount | default "") (.instanceWrites | default list) }}
{{- $parts := splitList "{{instance}}" .instanceServiceAccount }}
{{- $terms = append $terms (printf "(request.userInfo.username.startsWith('system:serviceaccount:' + request.namespace + ':%s') && request.userInfo.username.endsWith('%s') && size(request.userInfo.username) > size('system:serviceaccount:' + request.namespace + ':%s%s'))" (index $parts 0) (index $parts 1) (index $parts 0) (index $parts 1)) }}
{{- end }}{{ end }}{{ end }}
{{- if $terms }}({{ join " || " $terms }}){{ else }}false{{ end }}
{{- end }}

{{- /*
kube-dc.serviceFamilies.instanceStatusWrites -- JSON list of {group, resource}
for every registered root's status instance writes; instanceCoreWrites -- JSON
list of core resource names.
*/}}
{{- define "kube-dc.serviceFamilies.instanceStatusWrites" -}}
{{- $out := list -}}
{{- range .Values.serviceFamilies.extra }}{{ range .engineRoots }}{{ if .instanceServiceAccount }}{{ range (.instanceWrites | default list) }}{{ if eq (.subresource | default "") "status" }}{{ $out = append $out (dict "group" (.group | default "") "resource" .resource) }}{{ end }}{{ end }}{{ end }}{{ end }}{{ end -}}
{{ toJson $out }}
{{- end }}
{{- define "kube-dc.serviceFamilies.instanceCoreWrites" -}}
{{- $out := list -}}
{{- range .Values.serviceFamilies.extra }}{{ range .engineRoots }}{{ if .instanceServiceAccount }}{{ range (.instanceWrites | default list) }}{{ if and (eq (.group | default "") "") (not (has .resource $out)) }}{{ $out = append $out .resource }}{{ end }}{{ end }}{{ end }}{{ end }}{{ end -}}
{{ toJson $out }}
{{- end }}

{{- /*
kube-dc.instanceWrites.handoff -- PHASE 2 of the instance-write judge: a
matchCondition that hands the core writes (Secrets/ConfigMaps) of a registered
family's in-project instance account over to the managed-instance-objects
webhook, which judges them with UID-verified provenance. Rendered only when
projectPolicies.protectManagedServices.instanceWritesJudgedByWebhook is true
and some family declares such writes; the shape of the account name selects
the request, the webhook proves it. Renders nothing otherwise.
*/}}
{{- define "kube-dc.instanceWrites.handoff" -}}
{{- $cores := include "kube-dc.serviceFamilies.instanceCoreWrites" . | fromJsonArray -}}
{{- if and .Values.projectPolicies.protectManagedServices.instanceWritesJudgedByWebhook $cores }}
- name: exclude-instance-account-core-writes
  expression: >-
    !({{ include "kube-dc.serviceFamilies.instanceIdentityCEL" . }} &&
      request.resource.group == '' && request.resource.resource in {{ toJson $cores }})
{{- end }}
{{- end }}

{{- /*
kube-dc.markingServiceAccounts -- the exact service accounts whose CREATEs keep
the reserved labels they carry (KUBE_DC_MARKING_SERVICE_ACCOUNTS): db-manager,
and every registered family's operator. An operator creates children from
its root's labels, the platform's markers included; the child mutator strips
those from an object whose owner is not marked -- a StatefulSet template
claim, a Strimzi data claim (no owner reference at all) -- and the operator's
next reconcile, which puts them back, is a marker introduction the policy
refuses (measured on stage: a Kafka never reached Ready). A tenant cannot make
an operator copy markers: only the platform's roots carry them.
*/}}
{{- define "kube-dc.markingServiceAccounts" -}}
{{- $out := list (printf "system:serviceaccount:%s:%s" .Release.Namespace (include "kube-dc.dbmanager.fullname" .)) -}}
{{- range .Values.serviceFamilies.extra }}{{ range .operatorServiceAccounts }}{{ if not (has . $out) }}{{ $out = append $out . }}{{ end }}{{ end }}{{ end -}}
{{ join "," $out }}
{{- end }}
