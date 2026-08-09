{{/*
Shared CEL fragments for the VM and VMI GPU admission policies.

Each policy declares a guestSpec variable first because the native object path
differs (VM: object.spec.template.spec; VMI: object.spec). Keeping every other
variable and validation here prevents the two security boundaries from
silently drifting as rules are added.
*/}}
{{- define "kube-dc.gpuVirtualMachineVariables" -}}
- name: gpuDevices
  expression: >-
    [] +
    (has(variables.guestSpec.domain.devices) &&
     has(variables.guestSpec.domain.devices.gpus) ?
     variables.guestSpec.domain.devices.gpus : [])
- name: hostDevices
  expression: >-
    [] +
    (has(variables.guestSpec.domain.devices) &&
     has(variables.guestSpec.domain.devices.hostDevices) ?
     variables.guestSpec.domain.devices.hostDevices : [])
- name: hasGPU
  expression: size(variables.gpuDevices) > 0
- name: hasProfileLabel
  expression: >-
    has(object.metadata.labels) &&
    {{ .profileLabel | quote }} in object.metadata.labels
{{- end -}}

{{- define "kube-dc.gpuVirtualMachineValidations" -}}
- expression: size(variables.hostDevices) == 0
  message: "Project virtual machines must use domain.devices.gpus; generic hostDevices are not allowed."
{{- range $profile := .profiles }}
- expression: >-
    !variables.hasProfileLabel ||
    object.metadata.labels[{{ $.profileLabel | quote }}] != {{ $profile.id | quote }} ||
    variables.gpuDevices.all(d, d.deviceName == {{ $profile.resourceName | quote }})
  message: "Every GPU device must match the selected kube-dc GPU profile."
{{- end }}
- expression: >-
    !variables.hasGPU ||
    (has(variables.guestSpec.evictionStrategy) &&
     variables.guestSpec.evictionStrategy == 'None')
  message: "GPU virtual machines must explicitly use evictionStrategy: None."
- expression: >-
    !variables.hasGPU ||
    ((!has(variables.guestSpec.nodeSelector) ||
      !(variables.guestSpec.nodeSelector.exists(k,
        k in ['kubernetes.io/hostname', 'beta.kubernetes.io/hostname']))) &&
     (!has(variables.guestSpec.affinity) ||
      !has(variables.guestSpec.affinity.nodeAffinity) ||
      ((!has(variables.guestSpec.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution) ||
        variables.guestSpec.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms.all(t,
          (!has(t.matchExpressions) || t.matchExpressions.all(r,
            !(r.key in ['kubernetes.io/hostname', 'beta.kubernetes.io/hostname']))) &&
          (!has(t.matchFields) || t.matchFields.all(r, r.key != 'metadata.name')))) &&
       (!has(variables.guestSpec.affinity.nodeAffinity.preferredDuringSchedulingIgnoredDuringExecution) ||
        variables.guestSpec.affinity.nodeAffinity.preferredDuringSchedulingIgnoredDuringExecution.all(p,
          (!has(p.preference.matchExpressions) || p.preference.matchExpressions.all(r,
            !(r.key in ['kubernetes.io/hostname', 'beta.kubernetes.io/hostname']))) &&
          (!has(p.preference.matchFields) || p.preference.matchFields.all(r,
            r.key != 'metadata.name')))))))
  message: "GPU virtual machines cannot select an internal node name."
{{- end -}}
