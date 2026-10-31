{{/* The native sidecar restarts its Router after issued TLS material changes.
The SQL startup webhook independently proves the live root, account, Role,
Certificate/Secret ownership and actual material hashes. This policy exception
preserves all executable fields and markers and never applies to CREATE. */}}
{{- define "kube-dc.mysqlRouterTLSRefresh.variable" -}}
- name: mysqlRouterTLSRefresh
{{- if .Values.projectPolicies.protectManagedServices.sharedStartup.mysql }}
  expression: |-
    request.operation == 'UPDATE' && request.resource.group == 'apps' && request.resource.resource == 'deployments' && (!has(request.subResource) || request.subResource == '') && oldObject != null && oldObject.metadata.uid != '' && object.metadata.uid == oldObject.metadata.uid && object.metadata.namespace == request.namespace && oldObject.metadata.namespace == request.namespace && object.metadata.name == oldObject.metadata.name && !has(object.metadata.deletionTimestamp) && !has(oldObject.metadata.deletionTimestamp) && object.metadata.?labels.orValue({}) == oldObject.metadata.?labels.orValue({}) && ['services.kube-dc.com/startup-contract', 'services.kube-dc.com/family', 'services.kube-dc.com/managed-by', 'services.kube-dc.com/instance-uid'].all(k, k in oldObject.metadata.?labels.orValue({})) && oldObject.metadata.labels['services.kube-dc.com/startup-contract'] == 'shared-service-startup-v1' && oldObject.metadata.labels['services.kube-dc.com/family'] == 'mysql' && oldObject.metadata.labels['services.kube-dc.com/managed-by'] == 'kube-dc-services' && oldObject.metadata.labels['services.kube-dc.com/instance-uid'] != '' && object.metadata.?annotations.orValue({}) == oldObject.metadata.?annotations.orValue({}) && object.metadata.?finalizers.orValue([]) == oldObject.metadata.?finalizers.orValue([]) && object.metadata.?ownerReferences.orValue([]) == oldObject.metadata.?ownerReferences.orValue([]) && size(oldObject.metadata.?ownerReferences.orValue([])) == 1 && oldObject.metadata.ownerReferences[0].apiVersion == 'mysql.oracle.com/v2' && oldObject.metadata.ownerReferences[0].kind == 'InnoDBCluster' && oldObject.metadata.ownerReferences[0].uid != '' && oldObject.metadata.ownerReferences[0].controller == true && oldObject.metadata.ownerReferences[0].name.matches('^mss-v1-my-[a-f0-9]{24}$') && object.metadata.name == oldObject.metadata.ownerReferences[0].name + '-router' && request.userInfo.username == 'system:serviceaccount:' + request.namespace + ':' + oldObject.metadata.ownerReferences[0].name + '-sidecar-sa' && has(object.spec.template.metadata) && has(oldObject.spec.template.metadata) && object.spec.all(k, k == 'template' || (k in oldObject.spec && object.spec[k] == oldObject.spec[k])) && oldObject.spec.all(k, k == 'template' || k in object.spec) && object.spec.template.spec == oldObject.spec.template.spec && object.spec.template.metadata.all(k, k == 'annotations' || (k in oldObject.spec.template.metadata && object.spec.template.metadata[k] == oldObject.spec.template.metadata[k])) && oldObject.spec.template.metadata.all(k, k == 'annotations' || k in object.spec.template.metadata) && object.spec.template.metadata.?annotations.orValue({}).all(k,
      k in ['mysql.oracle.com/ca.pem.sha256', 'mysql.oracle.com/router_tls.crt.sha256', 'mysql.oracle.com/router_tls.key.sha256', 'kubectl.kubernetes.io/restartedAt'] ||
      (k in oldObject.spec.template.metadata.?annotations.orValue({}) && object.spec.template.metadata.annotations[k] == oldObject.spec.template.metadata.annotations[k])) &&
    oldObject.spec.template.metadata.?annotations.orValue({}).all(k,
      k in ['mysql.oracle.com/ca.pem.sha256', 'mysql.oracle.com/router_tls.crt.sha256', 'mysql.oracle.com/router_tls.key.sha256', 'kubectl.kubernetes.io/restartedAt'] ||
      k in object.spec.template.metadata.?annotations.orValue({})) &&
    ['mysql.oracle.com/ca.pem.sha256', 'mysql.oracle.com/router_tls.crt.sha256', 'mysql.oracle.com/router_tls.key.sha256'].all(k,
      !(k in oldObject.spec.template.metadata.?annotations.orValue({})) || k in object.spec.template.metadata.?annotations.orValue({})) &&
    object.spec.template.metadata.?annotations.orValue({}).all(k,
      !(k in ['mysql.oracle.com/ca.pem.sha256', 'mysql.oracle.com/router_tls.crt.sha256', 'mysql.oracle.com/router_tls.key.sha256']) || object.spec.template.metadata.annotations[k].matches('^[a-f0-9]{64}$')) &&
    (!('kubectl.kubernetes.io/restartedAt' in object.spec.template.metadata.?annotations.orValue({})) ||
      object.spec.template.metadata.annotations['kubectl.kubernetes.io/restartedAt'].matches('^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$'))
{{- else }}
  expression: "false"
{{- end }}
{{- end -}}
