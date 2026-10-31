{{- /*
The narrower fail-closed entries: items 3-9 of
docs/prd/admission-webhooks-remaining-design.md ("3. The narrower rows" and
adjacent finding 1), staged behind manager.webhook.narrowGatesHandoffPhase.

  0  (default) today's shape, byte for byte.
  1  adds the replacement controls: the policies managed-services-proxy,
     service-publication-eso-secrets, cloud-shell-job-tokens,
     project-network-nad-deletes, project-default-network-off-vlans and
     project-default-nads, and narrows the managed-services policies'
     delegation of instance-account and External Secrets writes to objects
     that carry the platform marker (old or new). Every webhook entry is
     unchanged.
  2  narrows the entries: managed-connect skips platform identities and no
     longer routes services/proxy; managed-instance-objects and
     service-publication-writes are selected on the marker (the latter
     without DELETE); cloud-shell-job is removed; mnetworkattach and
     vnetworkattach skip the two exempt shapes, and vnetworkattach routes an
     UPDATE only when a field its handler judges changes; vnetworkattachnad
     skips the manager and platform bookkeeping and no longer matches DELETE.

No new handler path and no manager environment variable: every entry that
stays keeps its path and its failurePolicy, so any manager serves every phase.

kube-dc.narrowGates.phase -- the validated phase as a string; an unknown value
fails the render.
*/ -}}
{{- define "kube-dc.narrowGates.phase" -}}
{{- $v := .Values.manager.webhook.narrowGatesHandoffPhase -}}
{{- /* Only an absent (or null) key means 0: `default` would also turn false or "" into 0. */ -}}
{{- $phase := "0" -}}
{{- if not (kindIs "invalid" $v) }}{{ $phase = toString $v }}{{ end -}}
{{- if not (has $phase (list "0" "1" "2")) -}}
{{- fail "manager.webhook.narrowGatesHandoffPhase must be 0, 1 or 2 (docs/prd/admission-webhooks-remaining-design.md, item 3)" -}}
{{- end -}}
{{- $phase -}}
{{- end -}}

{{- /*
kube-dc.narrowGates.markedClause -- from phase 1, the CEL conjunct that limits
a managed-services policy's delegation of a write to a webhook to objects that
carry services.kube-dc.com/managed-by in the old or the new object: exactly
what an objectSelector `managed-by Exists` admits (old or new on UPDATE, the
new object on CREATE, the old one on DELETE). Unmarked writes are then judged
by the policies again, as any project identity's. Renders nothing at phase 0,
so the delegation conditions keep today's text.
*/ -}}
{{- define "kube-dc.narrowGates.markedClause" -}}
{{- if ne (include "kube-dc.narrowGates.phase" .) "0" }} &&
      ((request.operation != 'CREATE' && has(oldObject.metadata.labels) && 'services.kube-dc.com/managed-by' in oldObject.metadata.labels) ||
       (request.operation != 'DELETE' && has(object.metadata.labels) && 'services.kube-dc.com/managed-by' in object.metadata.labels))
{{- end -}}
{{- end -}}

{{- /*
kube-dc.narrowGates.infraSubnet -- the infra subnet the exempt shape accepts
as a bare ovn.kubernetes.io/logical_switch: manager.infraAttachment.subnet
when the infra mutator is enabled, "" otherwise (then the bare switch must be
absent). It must not be a ProjectNetwork subnet (pn-<name>): the skip relies on
the infra subnet never being a tenant's reserved wire.
*/ -}}
{{- define "kube-dc.narrowGates.infraSubnet" -}}
{{- if .Values.manager.infraAttachment.enabled -}}
{{- $s := .Values.manager.infraAttachment.subnet | default "" -}}
{{- if not (regexMatch "^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$" $s) -}}
{{- fail "manager.infraAttachment.subnet must be a Subnet name for manager.webhook.narrowGatesHandoffPhase 2" -}}
{{- end -}}
{{- if hasPrefix "pn-" $s -}}
{{- fail "manager.infraAttachment.subnet may not be a ProjectNetwork subnet (pn-<name>): manager.webhook.narrowGatesHandoffPhase 2 skips pods whose bare logical_switch names it" -}}
{{- end -}}
{{- $s -}}
{{- end -}}
{{- end -}}

{{- /*
kube-dc.narrowGates.annotationsExemptCEL (dict "a" <CEL map expression> "root" $)
-- one annotation map carries no selection outside the exempt shapes:
k8s.v1.cni.cncf.io/networks and v1.multus-cni.io/default-network, when present,
name only the request namespace's default NAD (<ns>/default or default); a
bare ovn.kubernetes.io/logical_switch is absent or the infra subnet; no bare
logical_router or vpc; no provider-scoped logical_switch, logical_router or
vpc. One line, so it sits inside a folded scalar.
*/ -}}
{{- define "kube-dc.narrowGates.annotationsExemptCEL" -}}
{{- $a := .a -}}
{{- $infra := include "kube-dc.narrowGates.infraSubnet" .root -}}
(!('k8s.v1.cni.cncf.io/networks' in {{ $a }}) || {{ $a }}['k8s.v1.cni.cncf.io/networks'] in [request.namespace + '/default', 'default']) && (!('v1.multus-cni.io/default-network' in {{ $a }}) || {{ $a }}['v1.multus-cni.io/default-network'] in [request.namespace + '/default', 'default']) && (!('ovn.kubernetes.io/logical_switch' in {{ $a }}){{ if $infra }} || {{ $a }}['ovn.kubernetes.io/logical_switch'] == '{{ $infra }}'{{ end }}) && !('ovn.kubernetes.io/logical_router' in {{ $a }}) && !('ovn.kubernetes.io/vpc' in {{ $a }}) && !{{ $a }}.exists(k, k != 'ovn.kubernetes.io/logical_switch' && k.endsWith('kubernetes.io/logical_switch')) && !{{ $a }}.exists(k, k != 'ovn.kubernetes.io/logical_router' && k.endsWith('kubernetes.io/logical_router')) && !{{ $a }}.exists(k, k != 'ovn.kubernetes.io/vpc' && k.endsWith('kubernetes.io/vpc'))
{{- end -}}

{{- /*
kube-dc.narrowGates.exemptShapeCEL (dict "o" "object"|"oldObject" "root" $) --
the object is in one of the two exempt shapes of the design: the infra
mutator's default-only shape, or the project-default VM/VMI/launcher shape.
Every annotation map the handler merges (metadata, and for VMs and VMIs
spec.template.metadata and spec.metadata) passes annotationsExemptCEL, and
every Multus entry of a VM's or VMI's networks names the request namespace's
default NAD. Such an object names no NAD but <ns>/default, which
vnetworkattachnad keeps off a reserved wire, and no reserved Subnet, so the
handlers have nothing to stamp or refuse on it.
*/ -}}
{{- define "kube-dc.narrowGates.exemptShapeCEL" -}}
{{- $o := .o -}}
(({{ include "kube-dc.narrowGates.annotationsExemptCEL" (dict "a" (printf "(has(%s.metadata) && has(%s.metadata.annotations) ? %s.metadata.annotations : {})" $o $o $o) "root" .root) }}) && ({{ include "kube-dc.narrowGates.annotationsExemptCEL" (dict "a" (printf "(has(%s.spec) && has(%s.spec.template) && has(%s.spec.template.metadata) && has(%s.spec.template.metadata.annotations) ? %s.spec.template.metadata.annotations : {})" $o $o $o $o $o) "root" .root) }}) && ({{ include "kube-dc.narrowGates.annotationsExemptCEL" (dict "a" (printf "(has(%s.spec) && has(%s.spec.metadata) && has(%s.spec.metadata.annotations) ? %s.spec.metadata.annotations : {})" $o $o $o $o) "root" .root) }}) && (!has({{ $o }}.spec) || !has({{ $o }}.spec.networks) || {{ $o }}.spec.networks.all(n, !has(n.multus) || !has(n.multus.networkName) || n.multus.networkName in [request.namespace + '/default', 'default'])) && (!has({{ $o }}.spec) || !has({{ $o }}.spec.template) || !has({{ $o }}.spec.template.spec) || !has({{ $o }}.spec.template.spec.networks) || {{ $o }}.spec.template.spec.networks.all(n, !has(n.multus) || !has(n.multus.networkName) || n.multus.networkName in [request.namespace + '/default', 'default'])))
{{- end -}}

{{- /*
kube-dc.narrowGates.judgedFieldsChangedCEL -- an UPDATE changes something the
tenant-VLAN gate judges: any annotation (added, removed or changed), the spec
(a Pod's nodeName, a VM's or VMI's networks, template annotations and
everything else under spec), or a VMI's status.nodeName (the node the handler
checks segment delivery on). An UPDATE that changes none of these -- labels,
finalizers, ownerReferences, server bookkeeping -- cannot change what the
object is attached to or how it is protected. The handler reads no label.
*/ -}}
{{- define "kube-dc.narrowGates.judgedFieldsChangedCEL" -}}
((has(object.metadata) && has(object.metadata.annotations) ? object.metadata.annotations : {}) != (has(oldObject.metadata) && has(oldObject.metadata.annotations) ? oldObject.metadata.annotations : {}) || has(object.spec) != has(oldObject.spec) || (has(object.spec) && object.spec != oldObject.spec) || (has(object.status) && has(object.status.nodeName) ? object.status.nodeName : '') != (has(oldObject.status) && has(oldObject.status.nodeName) ? oldObject.status.nodeName : ''))
{{- end -}}

{{- /*
kube-dc.narrowGates.managerIdentity -- the manager's own ServiceAccount: the
identity the NAD handler admits first (isFabricController with
PROJECT_NETWORK_CONTROLLER_USERNAME).
*/ -}}
{{- define "kube-dc.narrowGates.managerIdentity" -}}
{{- printf "system:serviceaccount:%s:%s" .Release.Namespace (include "kube-dc.manager.serviceAccountName" .) -}}
{{- end -}}
