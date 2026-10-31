{{- /*
The managed-references hand-off (docs/prd/admission-webhooks-remaining-design.md,
item 2; as built: docs/prd/admission-webhooks-managed-references-as-built.md),
staged under projectPolicies.protectManagedServices.referencesHandoffPhase:

  0  today's shape, byte for byte;
  1  the policies with params (bound Audit), the parameterless policy (Audit),
     the creation route's marked-PVC rule and the narrow fail-closed entries
     managed-references-claim-custody, -copies and -reserved-claims;
     managed-references unchanged;
  2  the policies' bindings Deny; routing unchanged;
  3  the core rules move to managed-references-core (failurePolicy Ignore);
     managed-references keeps only its non-core rules.

kube-dc.referencesHandoff.phase -- the validated phase as a string. An unknown
value, or a phase above 0 without its prerequisites, fails the render.
*/ -}}
{{- define "kube-dc.referencesHandoff.phase" -}}
{{- $p := .Values.projectPolicies.protectManagedServices -}}
{{- $phase := "0" -}}
{{- if hasKey $p "referencesHandoffPhase" -}}
{{- $phase = toString $p.referencesHandoffPhase -}}
{{- end -}}
{{- if not (has $phase (list "0" "1" "2" "3")) -}}
{{- fail "projectPolicies.protectManagedServices.referencesHandoffPhase must be 0, 1, 2 or 3 (docs/prd/admission-webhooks-remaining-design.md, item 2)" -}}
{{- end -}}
{{- if ne $phase "0" -}}
{{- if not (and .Values.manager.webhook.enabled $p.enabled) -}}
{{- fail "projectPolicies.protectManagedServices.referencesHandoffPhase above 0 requires manager.webhook.enabled and projectPolicies.protectManagedServices.enabled" -}}
{{- end -}}
{{- if lt (int .Values.manager.webhook.broadGatesHandoffPhase) 1 -}}
{{- fail "projectPolicies.protectManagedServices.referencesHandoffPhase above 0 requires manager.webhook.broadGatesHandoffPhase 1 or 2: the creation route it extends renders only from there (docs/prd/admission-webhooks-remaining-design.md, item 2, dependencies between keys)" -}}
{{- end -}}
{{- end -}}
{{- $phase -}}
{{- end -}}

{{- /*
kube-dc.referencesHandoff.pureReferenceChecks -- "true" when the reference
gate no longer matches Secrets and PodDisruptionBudgets and routes only each
extra family's registered operations (step 2's
pureReferenceChecksJudgedByPolicy): set by hand, or implied by reference
phase 1 and above. The phase flips it, never the default: at phase 0 the flag
alone decides, as before. The step 2 policies that then judge those requests
alone (vap-managed-references.yaml) have rendered since chart v0.9.11-rc.9, so
on a cluster that ran that chart they are cached before the routes leave.
*/ -}}
{{- define "kube-dc.referencesHandoff.pureReferenceChecks" -}}
{{- if or .Values.projectPolicies.protectManagedServices.pureReferenceChecksJudgedByPolicy (ne (include "kube-dc.referencesHandoff.phase" .) "0") -}}
true
{{- end -}}
{{- end -}}

{{- /*
kube-dc.referencesHandoff.copySourceCEL (dict "spec" "<CEL path to a claim spec>")
-- the claim spec has a data source the handler resolves by a lookup: any kind
but VolumeImportSource and VolumeUploadSource, which claimSource() admits
without one (populated from outside the cluster). Either field counts, so a
spec that sets both is routed whenever one of them names a copy.
*/ -}}
{{- define "kube-dc.referencesHandoff.copySourceCEL" -}}
((has({{ .spec }}.dataSource) && !(has({{ .spec }}.dataSource.kind) && {{ .spec }}.dataSource.kind in ['VolumeImportSource', 'VolumeUploadSource'])) || (has({{ .spec }}.dataSourceRef) && !(has({{ .spec }}.dataSourceRef.kind) && {{ .spec }}.dataSourceRef.kind in ['VolumeImportSource', 'VolumeUploadSource'])))
{{- end -}}

{{- /*
kube-dc.referencesHandoff.importSourceCEL (dict "spec" "...") -- the claim
spec's data source is a VolumeImportSource or VolumeUploadSource.
*/ -}}
{{- define "kube-dc.referencesHandoff.importSourceCEL" -}}
((has({{ .spec }}.dataSource) && has({{ .spec }}.dataSource.kind) && {{ .spec }}.dataSource.kind in ['VolumeImportSource', 'VolumeUploadSource']) || (has({{ .spec }}.dataSourceRef) && has({{ .spec }}.dataSourceRef.kind) && {{ .spec }}.dataSourceRef.kind in ['VolumeImportSource', 'VolumeUploadSource']))
{{- end -}}

{{- /* kube-dc.referencesHandoff.markedObjectCEL -- the request's object carries
the platform marker. */ -}}
{{- define "kube-dc.referencesHandoff.markedObjectCEL" -}}
(has(object.metadata.labels) && 'services.kube-dc.com/managed-by' in object.metadata.labels)
{{- end -}}

{{- /*
kube-dc.referencesHandoff.entries -- the validating entries the hand-off adds
behind managed-references.kube-dc.com (rendered inside its configuration):

  phase 1+  managed-references-claim-custody.kube-dc.com   Fail, marker-selected
            managed-references-copies.kube-dc.com           Fail (accepted, owner 2026-10-16)
            managed-references-reserved-claims.kube-dc.com  Fail (accepted, owner 2026-10-16)
  phase 3   managed-references-core.kube-dc.com             Ignore

The three fail-closed entries are served on managed-references' own path:
their verdicts are today's handler's, lookup failures refused. The core entry
has a path of its own, so only it admits a request whose lookups fail
(ADMISSION_REFERENCES_HANDOFF_PHASE=3), and an older manager answers it 404,
which an Ignore entry admits: a chart ahead of its manager fails OPEN only on
the route that is meant to.
*/ -}}
{{- define "kube-dc.referencesHandoff.entries" }}
{{- $root := .root }}
{{- $platform := .platform }}
{{- $copy := include "kube-dc.referencesHandoff.copySourceCEL" (dict "spec" "object.spec") }}
{{- $ephemeralCopy := include "kube-dc.referencesHandoff.copySourceCEL" (dict "spec" "v.ephemeral.volumeClaimTemplate.spec") }}
  # A marked claim's custody: it BECOMES marked (squatter scan, every owner
  # the platform's), or gains a StatefulSet ownerReference (whenDeleted:
  # Delete would garbage-collect the engine's volume with a tenant's
  # StatefulSet). The StatefulSet controller's ownerReference write on a
  # MARKED claim is judged on purpose: a written, narrow exception to rule 5
  # of docs/prd/admission-webhooks-must-not-block-the-platform.md. Fails
  # CLOSED and is object-selected on the platform marker (old or new object),
  # so a tenant's claims never reach it.
  - name: managed-references-claim-custody.kube-dc.com
    admissionReviewVersions: ["v1"]
    sideEffects: None
    failurePolicy: Fail
    timeoutSeconds: 10
    matchPolicy: Equivalent
    namespaceSelector:
      matchExpressions:
        - {key: kube-dc.com/project, operator: Exists}
    objectSelector:
      matchExpressions:
        - {key: services.kube-dc.com/managed-by, operator: Exists}
    matchConditions:
      - name: claim-updates-that-change-custody
        expression: |-
          has(object.metadata.labels) && 'services.kube-dc.com/managed-by' in object.metadata.labels && (!(has(oldObject.metadata.labels) && 'services.kube-dc.com/managed-by' in oldObject.metadata.labels) ||
           (has(object.metadata.ownerReferences) && object.metadata.ownerReferences.exists(r, has(r.kind) && r.kind == 'StatefulSet' &&
             !(has(r.uid) && has(oldObject.metadata.ownerReferences) && oldObject.metadata.ownerReferences.exists(o, has(o.uid) && o.uid == r.uid)))))
    clientConfig:
      service:
        name: {{ include "kube-dc.manager.fullname" $root }}-webhook
        namespace: {{ $root.Release.Namespace }}
        path: /validate-managed-references
    rules:
      - apiGroups: [""]
        apiVersions: ["v1"]
        operations: ["UPDATE"]
        resources: ["persistentvolumeclaims"]
  # Copies of managed data: a claim, or a pod's generic ephemeral volume
  # template, with a data source the handler must resolve (a claim, a
  # VolumeSnapshot and its content's provenance, a VolumeCloneSource, a
  # DataSource; the clone-identity rule that an operator copies managed
  # storage only into a claim with a marked owner). A policy over marked
  # params cannot follow snapshot provenance or owners, so this stays
  # fail-closed: ACCEPTED by the owner (2026-10-16), a confidentiality
  # boundary on copies of managed data. Outage blast radius: claims and pods
  # created from a data source (restores, clones, CDI golden-image clones).
  # VolumeImportSource and VolumeUploadSource are admitted by the handler
  # without a lookup and are not routed here.
  - name: managed-references-copies.kube-dc.com
    admissionReviewVersions: ["v1"]
    sideEffects: None
    failurePolicy: Fail
    timeoutSeconds: 10
    matchPolicy: Equivalent
    namespaceSelector:
      matchExpressions:
        - {key: kube-dc.com/project, operator: Exists}
    matchConditions:
      - name: claims-and-pods-with-a-data-source
        expression: |-
          (request.resource.resource == 'persistentvolumeclaims' && has(object.spec) && {{ $copy }}) || (request.resource.resource == 'pods' && has(object.spec) && has(object.spec.volumes) && object.spec.volumes.exists(v,
            has(v.ephemeral) && has(v.ephemeral.volumeClaimTemplate) && has(v.ephemeral.volumeClaimTemplate.spec) && {{ $ephemeralCopy }}))
    clientConfig:
      service:
        name: {{ include "kube-dc.manager.fullname" $root }}-webhook
        namespace: {{ $root.Release.Namespace }}
        path: /validate-managed-references
    rules:
      - apiGroups: [""]
        apiVersions: ["v1"]
        operations: ["CREATE"]
        resources: ["persistentvolumeclaims", "pods"]
  # An engine's storage name. The creation route admits the platform's pod
  # only once its reserved-name claim exists, marked and not terminating; a
  # Pending pod does not hold its claim (PVC protection ignores unscheduled
  # pods), so a claim deleted after the pod was admitted could be re-created
  # unmarked during an outage and bound. A tenant's direct claim, or a
  # generic ephemeral volume's claim (controller-owned by a Pod, created by
  # the controller manager on a tenant pod's behalf under either of its
  # identities), whose name ends in digits (-wal optional) is therefore
  # judged here, whatever its data source. ACCEPTED by the owner
  # (2026-10-16): it holds an engine's storage name while binding fails
  # open. Outage blast radius: tenants' direct PVC creates and their generic
  # ephemeral volumes' claims under such names. StatefulSet claims come from
  # the StatefulSet controller, a platform identity outside this condition,
  # and do not wait.
  - name: managed-references-reserved-claims.kube-dc.com
    admissionReviewVersions: ["v1"]
    sideEffects: None
    failurePolicy: Fail
    timeoutSeconds: 10
    matchPolicy: Equivalent
    namespaceSelector:
      matchExpressions:
        - {key: kube-dc.com/project, operator: Exists}
    matchConditions:
      - name: reserved-claim-names-from-tenants-or-pods
        expression: |-
          (!{{ $platform }} ||
           (has(object.metadata.ownerReferences) && object.metadata.ownerReferences.exists(r, has(r.controller) && r.controller == true && has(r.kind) && r.kind == 'Pod'))) &&
          (!has(object.metadata.name) || object.metadata.name == '' || object.metadata.name.matches('-[0-9]+(-wal)?$'))
    clientConfig:
      service:
        name: {{ include "kube-dc.manager.fullname" $root }}-webhook
        namespace: {{ $root.Release.Namespace }}
        path: /validate-managed-references
    rules:
      - apiGroups: [""]
        apiVersions: ["v1"]
        operations: ["CREATE"]
        resources: ["persistentvolumeclaims"]
{{- if ge (int .phase) 3 }}
  # The core rules, failing OPEN. What they refuse is held, while the manager
  # is unreachable or impaired, by the policies with params
  # (vap-managed-references-handoff.yaml: marked claims and accounts, MariaDB
  # and KdcDatabase selectors), the marker-selected creation and custody
  # routes, and the two accepted fail-closed entries above. A reachable
  # manager judges every request here in full; one whose lookups fail admits
  # (logged, counted in kube_dc_admission_lookup_failures_admitted_total).
  # The conditions are managed-references' own, minus the marked-claim and
  # data-source clauses for claims: claims from a VolumeImportSource or
  # VolumeUploadSource are routed here, so the reserved-name and marker
  # checks still run on them. Pods keep the ephemeral data-source clause
  # (a superset of what the copies entry routes): an import or upload
  # source still counts toward the handler's 64-claim bound.
  - name: managed-references-core.kube-dc.com
    admissionReviewVersions: ["v1"]
    sideEffects: None
    failurePolicy: Ignore
    timeoutSeconds: 10
    matchPolicy: Equivalent
    namespaceSelector:
      matchExpressions:
        - {key: kube-dc.com/project, operator: Exists}
    matchConditions:
      - name: pods-that-name-a-claim-account-node-or-engine
        expression: |-
          !(request.resource.group == '' && request.resource.resource == 'pods' && request.operation == 'CREATE') || (has(object.spec) && has(object.spec.volumes) && object.spec.volumes.exists(v,
            (has(v.persistentVolumeClaim) && has(v.persistentVolumeClaim.claimName) && v.persistentVolumeClaim.claimName != '') ||
            (has(v.ephemeral) && has(v.ephemeral.volumeClaimTemplate) && has(v.ephemeral.volumeClaimTemplate.spec) &&
             (has(v.ephemeral.volumeClaimTemplate.spec.dataSource) || has(v.ephemeral.volumeClaimTemplate.spec.dataSourceRef))))) ||
          (has(object.spec) && has(object.spec.nodeName) && object.spec.nodeName != '') || (has(object.spec) && has(object.spec.serviceAccountName) && object.spec.serviceAccountName != '' && object.spec.serviceAccountName != 'default') || {{ .mariadbLabels }}
      - name: pod-updates-that-change-what-is-judged
        expression: |-
          !(request.resource.group == '' && request.resource.resource == 'pods' && request.operation == 'UPDATE') || (!({{ $platform }} && {{ .metadataOnly }}) &&
           ((!{{ $platform }} &&
             ((has(object.metadata.ownerReferences) && object.metadata.ownerReferences.exists(r, has(r.controller) && r.controller == true)) ||
              (has(oldObject.metadata.ownerReferences) && oldObject.metadata.ownerReferences.exists(r, has(r.controller) && r.controller == true)) ||
              ((!has(oldObject.spec) || !has(oldObject.spec.nodeName) || oldObject.spec.nodeName == '') && has(object.spec) && has(object.spec.nodeName) && object.spec.nodeName != ''))) ||
            {{ .mariadbLabels }}))
      - name: claims-that-take-a-reserved-name
        expression: >-
          !(request.resource.group == '' && request.resource.resource == 'persistentvolumeclaims') ||
          (has(object.spec) && {{ include "kube-dc.referencesHandoff.importSourceCEL" (dict "spec" "object.spec") }}) ||
          !has(object.metadata.name) || object.metadata.name == '' ||
          object.metadata.name.matches('-[0-9]+(-wal)?$')
      - name: service-accounts-from-tenants
        expression: >-
          !(request.resource.group == '' && request.resource.resource == 'serviceaccounts') || !{{ $platform }}
      - name: tenant-statefulsets-with-claim-templates
        expression: >-
          !(request.resource.group == 'apps' && request.resource.resource == 'statefulsets') ||
          (!{{ $platform }} && has(object.spec) && has(object.spec.volumeClaimTemplates) && size(object.spec.volumeClaimTemplates) > 0)
    clientConfig:
      service:
        name: {{ include "kube-dc.manager.fullname" $root }}-webhook
        namespace: {{ $root.Release.Namespace }}
        path: /validate-managed-references-core
    rules:
      - apiGroups: [""]
        apiVersions: ["v1"]
        operations: ["CREATE"]
        resources: ["persistentvolumeclaims", "serviceaccounts"]
      - apiGroups: [""]
        apiVersions: ["v1"]
        operations: ["CREATE", "UPDATE"]
        resources: ["pods"]
      - apiGroups: ["apps"]
        apiVersions: ["v1"]
        operations: ["CREATE", "UPDATE"]
        resources: ["statefulsets"]
{{- end }}
{{- end }}
