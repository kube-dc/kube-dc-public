{{- /*
Child marking (mark-managed-children.kube-dc.com and, from
manager.webhook.childMarkingHandoffPhase 1, its kube-system routes): step 4 of
docs/prd/admission-webhooks-must-not-block-the-platform.md; phases 3 to 5 (the
operators' split): docs/prd/admission-webhooks-step4-operators-as-built.md.

kube-dc.childMarking.phase -- the validated hand-off phase as a string; an
unknown value fails the render.
*/ -}}
{{- define "kube-dc.childMarking.phase" -}}
{{- $phase := toString (.Values.manager.webhook.childMarkingHandoffPhase | default 0) -}}
{{- if not (has $phase (list "0" "1" "2" "3" "4" "5")) -}}
{{- fail "manager.webhook.childMarkingHandoffPhase must be 0, 1, 2, 3, 4 or 5 (docs/prd/admission-webhooks-must-not-block-the-platform.md, step 4; 3 to 5: docs/prd/admission-webhooks-step4-operators-as-built.md)" -}}
{{- end -}}
{{- $phase -}}
{{- end -}}

{{- /*
kube-dc.childMarking.kubeControllers -- CEL: the requester is the controller
manager (its own identity, or a kube-system ServiceAccount when kube-system is
an exempt namespace). These create the children of every workload in a project
from the owner's templates: ReplicaSets, Pods, Jobs, StatefulSet and ephemeral
claims, and the per-namespace default account and root CA ConfigMap.
*/ -}}
{{- define "kube-dc.childMarking.kubeControllers" -}}
has(request.userInfo) && has(request.userInfo.username) &&
(request.userInfo.username == 'system:kube-controller-manager'
{{- if has "kube-system" (include "kube-dc.managedServices.exemptNamespaces" . | fromJsonArray) }} ||
 request.userInfo.username.startsWith('system:serviceaccount:kube-system:')
{{- end }})
{{- end -}}

{{- /*
kube-dc.childMarking.kubeControllersInline -- kube-dc.childMarking.kubeControllers
for an expression that is the include alone, at the include's indentation:
its first two lines joined by a space, exactly as the `>-` block these
entries used read them. The entries are `|-` blocks now (a `>-` block with a
more-indented line does not survive Flux's post-renderer byte for byte;
internal/chartcontract/postrender_stability_test.go), and a literal block
keeps every line break.
*/ -}}
{{- define "kube-dc.childMarking.kubeControllersInline" -}}
{{- include "kube-dc.childMarking.kubeControllers" . | replace "&&\n(" "&& (" -}}
{{- end -}}

{{- /*
kube-dc.childMarking.conditions -- the identity conditions every child-marking
entry shares: platform identities only, and RBAC from CNPG alone.
*/ -}}
{{- define "kube-dc.childMarking.conditions" -}}
- name: platform-identities-only
  # The exempt ServiceAccount namespaces, and the controller manager's
  # own identity where per-controller credentials are off.
  expression: |-
    has(request.userInfo) && has(request.userInfo.username) && (request.userInfo.username == 'system:kube-controller-manager' ||
     [{{ range $i, $ns := (include "kube-dc.managedServices.exemptNamespaces" $ | fromJsonArray) }}{{ if $i }}, {{ end }}'system:serviceaccount:{{ $ns }}:'{{ end }}].exists(prefix,
      request.userInfo.username.startsWith(prefix)))
{{- if and .Values.manager.webhook.enabled .Values.projectPolicies.protectManagedServices.enabled .Values.projectPolicies.protectManagedServices.postgresqlStartup.enabled }}
- name: cnpg-pooler-rbac-only
  expression: >-
    request.resource.group != 'rbac.authorization.k8s.io' ||
    request.userInfo.username == 'system:serviceaccount:cnpg-system:cnpg-cloudnative-pg'
{{- end }}
{{- end -}}

{{- /*
kube-dc.childMarking.rules -- the CREATE rules every child-marking entry
shares.
*/ -}}
{{- define "kube-dc.childMarking.rules" -}}
- apiGroups: [""]
  apiVersions: ["v1"]
  operations: ["CREATE"]
  resources: ["secrets", "configmaps", "services", "persistentvolumeclaims", "serviceaccounts", "pods"]
# What an operator derives as a workload (CNPG's pooler Deployment, the
# ReplicaSet the deployment controller makes of it, StatefulSets, Jobs):
# marked, so the pods they create are trusted by their owner.
- apiGroups: ["apps"]
  apiVersions: ["v1"]
  operations: ["CREATE"]
  resources: ["deployments", "replicasets", "statefulsets"]
- apiGroups: ["batch"]
  apiVersions: ["v1"]
  operations: ["CREATE"]
  resources: ["jobs"]
{{- if and .Values.manager.webhook.enabled .Values.projectPolicies.protectManagedServices.enabled .Values.projectPolicies.protectManagedServices.postgresqlStartup.enabled }}
# CNPG derives the pooler Role and RoleBinding from the marked Pooler.
# Apply the same live-owner UID inheritance before startup validation.
- apiGroups: ["rbac.authorization.k8s.io"]
  apiVersions: ["v1"]
  operations: ["CREATE"]
  resources: ["roles", "rolebindings"]
{{- end }}
# Custom ownership hops (for example Sentinel) must inherit the root's
# marker before their own workload children are created.
{{- range .Values.serviceFamilies.extra }}
{{- range .engineRoots }}
{{- range .via }}
{{- if and .operatorCreatesChildren (not (has .group (list "" "apps" "batch"))) }}
- apiGroups: [{{ .group | quote }}]
  apiVersions: [{{ .version | quote }}]
  operations: ["CREATE"]
  resources: [{{ .resource | quote }}]
{{- end }}
{{- end }}
{{- end }}
{{- end }}
- apiGroups: ["snapshot.storage.k8s.io"]
  apiVersions: ["v1"]
  operations: ["CREATE"]
  resources: ["volumesnapshots"]
{{- end -}}

{{- /*
Operator creations (manager.webhook.childMarkingHandoffPhase 3 to 5):
docs/prd/admission-webhooks-step4-operators-as-built.md.

kube-dc.childMarking.probeUser -- the synthetic user of the probes: never
issued by an authentication provider, and project roles cannot impersonate.
Every request it makes must be a server-side dry run
(child-marking-probe-dry-run-only).
*/ -}}
{{- define "kube-dc.childMarking.probeUser" -}}
child-marking-probe@kube-dc.internal
{{- end -}}

{{- /*
kube-dc.childMarking.probeCEL -- CEL: the requester is the probe.
*/ -}}
{{- define "kube-dc.childMarking.probeCEL" -}}
(has(request.userInfo.username) && request.userInfo.username == '{{ include "kube-dc.childMarking.probeUser" . }}')
{{- end -}}

{{- /*
kube-dc.childMarking.operatorCEL -- CEL: a platform identity other than the
controller manager: exactly platform-identities-only and not-kube-controllers
of the child-marking entries.
*/ -}}
{{- define "kube-dc.childMarking.operatorCEL" -}}
(has(request.userInfo) && has(request.userInfo.username) &&
 (request.userInfo.username == 'system:kube-controller-manager' ||
  [{{ range $i, $ns := (include "kube-dc.managedServices.exemptNamespaces" $ | fromJsonArray) }}{{ if $i }}, {{ end }}'system:serviceaccount:{{ $ns }}:'{{ end }}].exists(prefix,
   request.userInfo.username.startsWith(prefix))) &&
 !({{ include "kube-dc.childMarking.kubeControllers" . | nindent 2 | trim }}))
{{- end -}}

{{- /*
kube-dc.childMarking.conditionsWithProbe -- kube-dc.childMarking.conditions
with the probe added to the identities (platform-identities-or-probe). Used by
the entry that takes an operator's unmarked arrival from phase 3 on, for the
route probe.
*/ -}}
{{- define "kube-dc.childMarking.conditionsWithProbe" -}}
- name: platform-identities-or-probe
  # The exempt ServiceAccount namespaces, the controller manager's own
  # identity, and the probe user (route probe; it is only ever a dry run).
  expression: |-
    has(request.userInfo) && has(request.userInfo.username) && (request.userInfo.username == 'system:kube-controller-manager' ||
     request.userInfo.username == '{{ include "kube-dc.childMarking.probeUser" . }}' ||
     [{{ range $i, $ns := (include "kube-dc.managedServices.exemptNamespaces" $ | fromJsonArray) }}{{ if $i }}, {{ end }}'system:serviceaccount:{{ $ns }}:'{{ end }}].exists(prefix,
      request.userInfo.username.startsWith(prefix)))
{{- if and .Values.manager.webhook.enabled .Values.projectPolicies.protectManagedServices.enabled .Values.projectPolicies.protectManagedServices.postgresqlStartup.enabled }}
- name: cnpg-pooler-rbac-only
  expression: >-
    request.resource.group != 'rbac.authorization.k8s.io' ||
    request.userInfo.username == 'system:serviceaccount:cnpg-system:cnpg-cloudnative-pg'
{{- end }}
{{- end -}}

{{- /*
kube-dc.childMarking.stagedOperators -- the staged identities of phase 4 as a
JSON list, validated: exact ServiceAccount usernames in an exempt namespace,
never kube-system, the engine operators' namespaces, cert-manager, rook-ceph
or a registered family's operator namespace (those move only at phase 5).
*/ -}}
{{- define "kube-dc.childMarking.stagedOperators" -}}
{{- $exempt := include "kube-dc.managedServices.exemptNamespaces" . | fromJsonArray -}}
{{- $forbidden := list "kube-system" "cnpg-system" "mariadb-system" "cert-manager" "rook-ceph" -}}
{{- range .Values.serviceFamilies.extra }}{{ range .operatorServiceAccounts }}{{ $parts := splitList ":" . }}{{ if eq (len $parts) 4 }}{{ $forbidden = append $forbidden (index $parts 2) }}{{ end }}{{ end }}{{ end -}}
{{- $out := list -}}
{{- range (.Values.manager.webhook.childMarkingStagedOperators | default list) }}
{{- if not (kindIs "string" .) }}{{ fail "manager.webhook.childMarkingStagedOperators entries must be strings" }}{{ end }}
{{- if not (regexMatch "^system:serviceaccount:[a-z0-9]([-a-z0-9]*[a-z0-9])?:[a-z0-9]([-a-z0-9.]*[a-z0-9])?$" .) }}{{ fail (printf "manager.webhook.childMarkingStagedOperators: %q is not an exact ServiceAccount username (system:serviceaccount:<namespace>:<name>)" .) }}{{ end }}
{{- $ns := index (splitList ":" .) 2 -}}
{{- if not (has $ns $exempt) }}{{ fail (printf "manager.webhook.childMarkingStagedOperators: %q is not in an exempt namespace, so it never reaches the child mutator" .) }}{{ end }}
{{- if has $ns $forbidden }}{{ fail (printf "manager.webhook.childMarkingStagedOperators: %q is a controller-manager, engine-operator, cert-manager or rook-ceph identity; those leave the fail-closed entry only at phase 5 (docs/prd/admission-webhooks-step4-operators-as-built.md)" .) }}{{ end }}
{{- if not (has . $out) }}{{ $out = append $out . }}{{ end }}
{{- end -}}
{{ toJson $out }}
{{- end -}}

{{- /*
kube-dc.childMarking.ownerKinds -- the covered owner kinds of the arrive-marked
and escaped-children policies, as a JSON list of {group, version, kind, name}:
the built-in engines and helpers, the built-in kinds an operator's child can
name as its owner, VolumeSnapshots, every registered family's engine roots and
operator-created hops, and manager.webhook.childMarkingProtectedOwnerKinds.
Secrets and ConfigMaps are never covered: managed-children-no-secret-owners
refuses them as owners of an operator's child. PhysicalBackup (mariadb-operator
creates its ServiceAccount, Role, RoleBinding, staging claim and
Job), EphemeralRunnerSet (ARC creates its proxy Secret) and EphemeralRunner
(ARC creates the runner Pod and its JIT-config Secret, which ARC does not label
from the marked runner) own operator children that carry authority. The marked kinds deliberately left uncovered, each with its reason,
are childMarkingExcludedOwnerKinds in
internal/webhook/core/child_marking_operators_audit.go (the audit accounts for
them and reports any child one owns; a contract test keeps them disjoint from
this list); docs/prd/admission-webhooks-step4-operators-as-built.md, "Coverage".
*/ -}}
{{- define "kube-dc.childMarking.ownerKinds" -}}
{{- $kinds := list
  (dict "group" "postgresql.cnpg.io" "version" "v1" "kind" "Cluster")
  (dict "group" "postgresql.cnpg.io" "version" "v1" "kind" "Pooler")
  (dict "group" "k8s.mariadb.com" "version" "v1alpha1" "kind" "MariaDB")
  (dict "group" "cert-manager.io" "version" "v1" "kind" "Certificate")
  (dict "group" "objectbucket.io" "version" "v1alpha1" "kind" "ObjectBucketClaim")
  (dict "group" "" "version" "v1" "kind" "Pod")
  (dict "group" "" "version" "v1" "kind" "Service")
  (dict "group" "" "version" "v1" "kind" "PersistentVolumeClaim")
  (dict "group" "" "version" "v1" "kind" "ServiceAccount")
  (dict "group" "" "version" "v1" "kind" "ReplicationController")
  (dict "group" "apps" "version" "v1" "kind" "Deployment")
  (dict "group" "apps" "version" "v1" "kind" "ReplicaSet")
  (dict "group" "apps" "version" "v1" "kind" "StatefulSet")
  (dict "group" "apps" "version" "v1" "kind" "DaemonSet")
  (dict "group" "batch" "version" "v1" "kind" "Job")
  (dict "group" "batch" "version" "v1" "kind" "CronJob")
  (dict "group" "snapshot.storage.k8s.io" "version" "v1" "kind" "VolumeSnapshot")
  (dict "group" "k8s.mariadb.com" "version" "v1alpha1" "kind" "PhysicalBackup")
  (dict "group" "actions.github.com" "version" "v1alpha1" "kind" "EphemeralRunnerSet")
  (dict "group" "actions.github.com" "version" "v1alpha1" "kind" "EphemeralRunner") -}}
{{- $extra := list -}}
{{- range .Values.serviceFamilies.extra }}
{{- range .engineRoots }}
{{- $extra = append $extra (dict "group" .group "version" .version "kind" .kind) }}
{{- range .via }}
{{- if and .operatorCreatesChildren (not (has .group (list "" "apps" "batch"))) }}
{{- $extra = append $extra (dict "group" .group "version" .version "kind" .kind) }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}
{{- range (.Values.manager.webhook.childMarkingProtectedOwnerKinds | default list) }}
{{- if not (and (kindIs "map" .) (kindIs "string" .group) (kindIs "string" .version) (kindIs "string" .kind)) }}{{ fail "manager.webhook.childMarkingProtectedOwnerKinds entries are {group, version, kind}" }}{{ end }}
{{- if not (and (regexMatch "^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*)?$" .group) (regexMatch "^v[0-9]+((alpha|beta)[0-9]+)?$" .version) (regexMatch "^[A-Z][A-Za-z0-9]*$" .kind)) }}{{ fail (printf "manager.webhook.childMarkingProtectedOwnerKinds: %v is not a group, version and kind" .) }}{{ end }}
{{- if and (eq .group "") (has .kind (list "Secret" "ConfigMap")) }}{{ fail "manager.webhook.childMarkingProtectedOwnerKinds: Secrets and ConfigMaps never own an operator's child (managed-children-no-secret-owners)" }}{{ end }}
{{- $extra = append $extra (dict "group" .group "version" .version "kind" .kind) }}
{{- end }}
{{- $out := list -}}
{{- $seen := dict -}}
{{- range (concat $kinds $extra) }}
{{- $key := printf "%s/%s" .group .kind -}}
{{- if not (hasKey $seen $key) }}
{{- $_ := set $seen $key true -}}
{{- $name := lower .kind -}}
{{- if .group }}{{ $name = printf "%s.%s" (lower .kind) .group }}{{ end -}}
{{- $out = append $out (dict "group" .group "version" .version "kind" .kind "name" $name) }}
{{- end }}
{{- end -}}
{{ toJson $out }}
{{- end -}}

{{- /*
kube-dc.childMarking.policyRules -- the child-marking rules for a policy:
the same resources as kube-dc.childMarking.rules, with the given operations,
optionally with pods/ephemeralcontainers and pods/resize (the subresources
through which a tenant reaches into a pod; status subresources are
controllers' and kubelets' hot paths, and every param evaluation costs the API
server a conversion of the request: managed-children-status-metadata guards
them without params), never VolumeSnapshots (the mutator judges
those by their source claim; the snapshot policies have their own rules).
Arguments: dict "ctx" . "ops" (list) "sub" (bool).
*/ -}}
{{- define "kube-dc.childMarking.policyRules" -}}
{{- $ctx := .ctx -}}
{{- $ops := .ops | toJson -}}
{{- $groups := list (dict "group" "" "resources" (list "secrets" "configmaps" "services" "persistentvolumeclaims" "serviceaccounts" "pods"))
  (dict "group" "apps" "resources" (list "deployments" "replicasets" "statefulsets"))
  (dict "group" "batch" "resources" (list "jobs")) -}}
{{- if and $ctx.Values.manager.webhook.enabled $ctx.Values.projectPolicies.protectManagedServices.enabled $ctx.Values.projectPolicies.protectManagedServices.postgresqlStartup.enabled }}
{{- $groups = append $groups (dict "group" "rbac.authorization.k8s.io" "resources" (list "roles" "rolebindings")) }}
{{- end }}
{{- range $groups }}
{{- $res := .resources -}}
{{- if and $.sub (eq .group "") }}{{ $res = append (append $res "pods/ephemeralcontainers") "pods/resize" }}{{ end }}
- apiGroups: [{{ .group | quote }}]
  apiVersions: ["v1"]
  operations: {{ $ops }}
  resources: {{ $res | toJson }}
{{- end }}
{{- range $ctx.Values.serviceFamilies.extra }}
{{- range .engineRoots }}
{{- range .via }}
{{- if and .operatorCreatesChildren (not (has .group (list "" "apps" "batch"))) }}
- apiGroups: [{{ .group | quote }}]
  apiVersions: [{{ .version | quote }}]
  operations: {{ $ops }}
  resources: {{ list .resource | toJson }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}
{{- end -}}
