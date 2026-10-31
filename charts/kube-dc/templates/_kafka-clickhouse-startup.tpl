{{- /*
Hand-off phase of the Kafka and ClickHouse shared startup gates (step 3 of
docs/prd/admission-webhooks-must-not-block-the-platform.md; as built in
docs/prd/admission-webhooks-step3-kafka-clickhouse.md), read from
projectPolicies.protectManagedServices.sharedStartupHandoff.<family>.handoffPhase.
The reader is kube-dc.sharedStartup.handoffPhase (_shared-startup-handoff.tpl),
shared with the other shared families; an unknown phase (including false or "")
fails the render.
*/ -}}
{{- define "kube-dc.chkStartup.phase" -}}
{{- include "kube-dc.sharedStartup.handoffPhase" (dict "root" (index . 0) "family" (index . 1)) -}}
{{- end -}}

{{- /* "true" when the family's gate is enabled and at hand-off phase 2. */ -}}
{{- define "kube-dc.chkStartup.split" -}}
{{- $root := index . 0 -}}
{{- $family := index . 1 -}}
{{- if and (index $root.Values.projectPolicies.protectManagedServices.sharedStartup $family) (eq (include "kube-dc.chkStartup.phase" .) "2") -}}
true
{{- end -}}
{{- end -}}

{{- /* The authority selector's match on one family's platform-labelled objects,
as a CEL predicate over o (the request's object or oldObject). */ -}}
{{- define "kube-dc.chkStartup.selectedCEL" -}}
o != null && has(o.metadata) && has(o.metadata.labels) && 'services.kube-dc.com/managed-by' in o.metadata.labels && o.metadata.labels[?'services.kube-dc.com/startup-contract'].orValue('') == 'shared-service-startup-v1' && o.metadata.labels[?'services.kube-dc.com/family'].orValue('') == '{{ . }}'
{{- end -}}

{{- /* The operators' API groups both phase-2 entries route (CREATE/UPDATE), as
the phase-0 entry does (shared-startup-webhooks.yaml), without DELETE. */ -}}
{{- define "kube-dc.chkStartup.operatorRules" }}
      - apiGroups: ["postgresql.cnpg.io"]
        apiVersions: ["v1"]
        operations: ["CREATE", "UPDATE"]
        resources: ["clusters", "backups", "scheduledbackups", "poolers", "databases", "publications", "subscriptions"]
        scope: Namespaced
      - apiGroups: ["valkey.buf.red"]
        apiVersions: ["v1alpha1"]
        operations: ["CREATE", "UPDATE"]
        resources: ["failovers", "sentinels", "users"]
        scope: Namespaced
      - apiGroups: ["mysql.oracle.com"]
        apiVersions: ["v2"]
        operations: ["CREATE", "UPDATE"]
        resources: ["innodbclusters", "mysqlbackups", "mysqlclustersetfailovers"]
        scope: Namespaced
      - apiGroups: ["k8s.mariadb.com"]
        apiVersions: ["v1alpha1"]
        operations: ["CREATE", "UPDATE"]
        resources: ["mariadbs", "users", "grants", "databases", "backups", "physicalbackups", "pointintimerecoveries", "restores", "sqljobs", "maxscales", "connections"]
        scope: Namespaced
      - apiGroups: ["kafka.strimzi.io"]
        apiVersions: ["v1", "v1beta2"]
        operations: ["CREATE", "UPDATE"]
        resources: ["kafkas", "kafkanodepools", "kafkausers", "kafkatopics", "strimzipodsets", "kafkaconnects", "kafkamirrormakers", "kafkamirrormaker2s", "kafkabridges", "kafkarebalances", "kafkaconnectors"]
        scope: Namespaced
      - apiGroups: ["core.strimzi.io"]
        apiVersions: ["v1"]
        operations: ["CREATE", "UPDATE"]
        resources: ["strimzipodsets"]
        scope: Namespaced
      - apiGroups: ["clickhouse.altinity.com", "clickhouse-keeper.altinity.com"]
        apiVersions: ["v1"]
        operations: ["CREATE", "UPDATE"]
        resources: ["clickhouseinstallations", "clickhouseinstallationtemplates", "clickhousekeeperinstallations", "clickhousekeeperinstallationtemplates"]
        scope: Namespaced
{{- end -}}
