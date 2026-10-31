{{- /*
kube-dc.sharedStartup.handoffPhase: the hand-off phase (0, 1 or 2) of one
shared startup gate, read from
projectPolicies.protectManagedServices.sharedStartupHandoff.<family>.handoffPhase
(docs/prd/admission-webhooks-must-not-block-the-platform.md, step 3). A family
without an entry is at phase 0; any other value fails the render. Call with
(dict "root" $ "family" "<family>"); the result is a string, pipe it to int.
*/ -}}
{{- define "kube-dc.sharedStartup.handoffPhase" -}}
{{- $all := .root.Values.projectPolicies.protectManagedServices.sharedStartupHandoff | default dict -}}
{{- $entry := index $all .family | default dict -}}
{{- $phase := 0 -}}
{{- if hasKey $entry "handoffPhase" -}}
{{- $phase = $entry.handoffPhase -}}
{{- end -}}
{{- if or (kindIs "bool" $phase) (not (has (toString $phase) (list "0" "1" "2"))) -}}
{{- fail (printf "projectPolicies.protectManagedServices.sharedStartupHandoff.%s.handoffPhase must be 0, 1 or 2 (docs/prd/admission-webhooks-must-not-block-the-platform.md, step 3)" .family) -}}
{{- end -}}
{{- toString $phase -}}
{{- end -}}

{{- /*
kube-dc.sqlSharedStartup.selectedCEL: CEL that is true when the given object
("object" or "oldObject") carries the labels the family's phase-2 authority
entry selects on (services.kube-dc.com/managed-by Exists,
startup-contract In [shared-service-startup-v1], family In [<family>]). The
references entry excludes exactly these requests, so every request reaches
one of the two entries. Call with (dict "family" "<family>" "object" "object").
*/ -}}
{{- define "kube-dc.sqlSharedStartup.selectedCEL" -}}
(has({{ .object }}.metadata.labels) && 'services.kube-dc.com/managed-by' in {{ .object }}.metadata.labels && {{ .object }}.metadata.labels[?'services.kube-dc.com/startup-contract'].orValue('') == 'shared-service-startup-v1' && {{ .object }}.metadata.labels[?'services.kube-dc.com/family'].orValue('') == '{{ .family }}')
{{- end -}}
{{- /* The operator API rules and cluster-scoped RBAC both entries route,
       exactly as the previous single entry routes them for MySQL and
       MariaDB (CREATE/UPDATE). */}}
{{- define "kube-dc.sqlSharedStartup.operatorRules" }}
      - apiGroups: ["cert-manager.io"]
        apiVersions: ["v1"]
        operations: ["CREATE", "UPDATE"]
        resources: ["issuers", "certificates", "certificaterequests"]
        scope: Namespaced
      - apiGroups: ["rbac.authorization.k8s.io"]
        apiVersions: ["v1"]
        operations: ["CREATE", "UPDATE"]
        resources: ["clusterroles", "clusterrolebindings"]
        scope: Cluster
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
{{- end }}
