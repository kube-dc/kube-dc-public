# Certificate Management and Monitoring Requirements

**Document Version:** 1.0  
**Last Updated:** January 12, 2026  
**Status:** Production Critical

---

## Executive Summary

Kube-DC relies on numerous webhook-based components that require valid TLS certificates. A certificate audit revealed that while most components use cert-manager for automatic renewal, several critical components use self-managed certificates with varying expiration policies. **Failure to monitor and maintain these certificates can result in complete platform outages.**

**Critical Issue Resolved:** CAPK webhook certificate expired on January 3, 2026, preventing all KdcCluster worker pool creation. This has been fixed and integrated into the installer for future deployments.

---

## Certificate Management Architecture

### Components with cert-manager Auto-Renewal (✅ Safe)

These components are properly managed and will automatically renew:

#### Cluster API Webhooks
- **capi-system/capi-webhook-service-cert**
  - Validity: 1 year
  - Renewal: 30 days before expiry
  - Management: cert-manager + self-signed issuer

- **capi-k3s-bootstrap-system/capi-k3s-bootstrap-webhook-service-cert**
- **capi-k3s-control-plane-system/capi-k3s-control-plane-webhook-service-cert**
- **capi-kubeadm-bootstrap-system/capi-kubeadm-bootstrap-webhook-service-cert**
- **capk-system/capk-webhook-service-cert** ⚡ **Fixed January 2026**
  - Validity: 1 year
  - Renewal: 30 days before expiry
  - Management: cert-manager + self-signed issuer
  - **Now integrated into installer** (see Installer Integration section)

#### Kamaji Certificates
- **kamaji-system/kamaji-webhook-server-cert**
  - Validity: 1 year
  - Management: cert-manager

- **44+ etcd certificate resources** (tenant cluster datastores)
  - Issuer: `etcd-ca-issuer` (ClusterIssuer)
  - Automatically created per KdcClusterDatastore
  - Management: cert-manager

---

### Components with Self-Managed Certificates (⚠️ Requires Monitoring)

#### KubeVirt Certificates (⚠️ High Risk - 24h Validity)

**Certificate Details:**
```
kubevirt-operator-certs:       24 hours validity
kubevirt-virt-api-certs:       24 hours validity
kubevirt-controller-certs:     24 hours validity
kubevirt-virt-handler-certs:   24 hours validity
```

**Affected Webhooks:**
- `virt-api-validator` (ValidatingWebhookConfiguration)
- `virt-api-mutator` (MutatingWebhookConfiguration)
- `virt-operator-validator` (ValidatingWebhookConfiguration)

**Risk Assessment:**
- ⚠️ **Very short validity period** (24 hours)
- ✅ Built-in automatic rotation via `virt-operator`
- ⚠️ **Single point of failure:** If virt-operator pod fails/crashes, certificates won't rotate
- ⚠️ **Impact if expired:** All VM operations fail, including creation, deletion, and live migration

**Monitoring Requirements:**
```bash
# Check virt-operator health
kubectl get pods -n kubevirt -l kubevirt.io=virt-operator

# Check certificate rotation logs
kubectl logs -n kubevirt deployment/virt-operator --tail=100 | grep -i cert

# Verify certificate validity
kubectl get secret -n kubevirt kubevirt-virt-api-certs -o jsonpath='{.data.tls\.crt}' | \
  base64 -d | openssl x509 -noout -dates
```

---

#### CDI Certificates (⚠️ High Risk - 24h Validity)

**Certificate Details:**
```
cdi-apiserver-server-cert:     24 hours validity
cdi-uploadproxy-server-cert:   24 hours validity
cdi-uploadserver-client-cert:  24 hours validity
```

**Affected Webhooks:**
- `cdi-api-dataimportcron-validate` (ValidatingWebhookConfiguration)
- `cdi-api-datavolume-validate` (ValidatingWebhookConfiguration)
- `cdi-api-datavolume-mutate` (MutatingWebhookConfiguration)
- `objecttransfer-api-validate` (ValidatingWebhookConfiguration)

**Risk Assessment:**
- ⚠️ **Very short validity period** (24 hours)
- ✅ Built-in automatic rotation via `cdi-operator`
- ⚠️ **Single point of failure:** If cdi-operator pod fails/crashes, certificates won't rotate
- ⚠️ **Impact if expired:** DataVolume operations fail, preventing disk provisioning and VM creation

**Monitoring Requirements:**
```bash
# Check cdi-operator health
kubectl get pods -n cdi -l cdi.kubevirt.io=cdi-operator

# Check certificate rotation logs
kubectl logs -n cdi deployment/cdi-operator --tail=100 | grep -i cert

# Verify certificate validity
kubectl get secret -n cdi cdi-apiserver-server-cert -o jsonpath='{.data.tls\.crt}' | \
  base64 -d | openssl x509 -noout -dates
```

---

#### Kyverno Certificates (⚠️ Medium Risk - ~5 Months)

**Certificate Details (as of Jan 12, 2026):**
```
kyverno-svc CA:                     Valid until Aug 6, 2026 (1 year)
kyverno-svc certificate:            Valid until May 31, 2026 (~5 months)
kyverno-cleanup-controller CA:      Valid until Aug 6, 2026 (1 year)
kyverno-cleanup-controller cert:    Valid until May 31, 2026 (~5 months)
```

**Affected Webhooks:**
- `kyverno-policy-validating-webhook-cfg`
- `kyverno-resource-validating-webhook-cfg`
- `kyverno-exception-validating-webhook-cfg`
- `kyverno-cleanup-validating-webhook-cfg`
- `kyverno-policy-mutating-webhook-cfg`
- `kyverno-resource-mutating-webhook-cfg`
- `kyverno-verify-mutating-webhook-cfg`

**Risk Assessment:**
- ⚠️ **Manual renewal required** - No automatic rotation
- ⚠️ **Next renewal deadline:** May 31, 2026
- ⚠️ **Impact if expired:** Policy enforcement fails, security policies not applied

**Action Required:**
- **Before May 2026:** Migrate Kyverno certificates to cert-manager management
- **Alternative:** Manual certificate renewal procedure

---

## Monitoring and Alerting Requirements

### Prometheus Alerts

**Certificate Expiration Alert:**
```yaml
apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: certificate-expiration-alerts
  namespace: monitoring
spec:
  groups:
  - name: certificates
    interval: 1h
    rules:
    - alert: WebhookCertificateExpiringSoon
      expr: |
        (cert_manager_certificate_expiration_timestamp_seconds - time()) / 86400 < 30
      for: 1h
      labels:
        severity: warning
      annotations:
        summary: "Certificate {{ $labels.namespace }}/{{ $labels.name }} expires in < 30 days"
        description: "Certificate will expire in {{ $value | humanizeDuration }}"
    
    - alert: WebhookCertificateExpired
      expr: |
        cert_manager_certificate_expiration_timestamp_seconds - time() < 0
      for: 1m
      labels:
        severity: critical
      annotations:
        summary: "Certificate {{ $labels.namespace }}/{{ $labels.name }} has EXPIRED"
        description: "Immediate action required to restore service"
```

### Manual Verification Commands

**Check all webhook certificates:**
```bash
#!/bin/bash
# certificate-check.sh

echo "=== CAPK Webhook Certificate ==="
kubectl get certificate capk-serving-cert -n capk-system -o wide

echo -e "\n=== KubeVirt Operator Health ==="
kubectl get pods -n kubevirt -l kubevirt.io=virt-operator

echo -e "\n=== KubeVirt Certificate Expiry ==="
kubectl get secret -n kubevirt kubevirt-virt-api-certs -o jsonpath='{.data.tls\.crt}' | \
  base64 -d | openssl x509 -noout -dates

echo -e "\n=== CDI Operator Health ==="
kubectl get pods -n cdi -l cdi.kubevirt.io=cdi-operator

echo -e "\n=== CDI Certificate Expiry ==="
kubectl get secret -n cdi cdi-apiserver-server-cert -o jsonpath='{.data.tls\.crt}' | \
  base64 -d | openssl x509 -noout -dates

echo -e "\n=== Kyverno Certificate Expiry ==="
kubectl get secret -n kyverno kyverno-svc.kyverno.svc.kyverno-tls-pair -o jsonpath='{.data.tls\.crt}' | \
  base64 -d | openssl x509 -noout -dates

echo -e "\n=== All cert-manager Certificates ==="
kubectl get certificate -A -o wide | grep -v "True.*up to date"
```

**Add to cron for daily checks:**
```bash
# Run daily at 8 AM
0 8 * * * /path/to/certificate-check.sh | mail -s "Kube-DC Certificate Status" ops@example.com
```

---

## Incident Response Procedures

### Scenario 1: KubeVirt/CDI Certificate Expired (24h)

**Symptoms:**
- VM creation fails with webhook errors
- DataVolume operations rejected
- Webhook timeout errors in logs

**Emergency Recovery:**
```bash
# 1. Restart the operator to trigger certificate rotation
kubectl rollout restart deployment/virt-operator -n kubevirt
# or
kubectl rollout restart deployment/cdi-operator -n cdi

# 2. Wait for operator to issue new certificates
kubectl rollout status deployment/virt-operator -n kubevirt --timeout=5m

# 3. Verify webhook pods reload certificates
kubectl rollout restart deployment/virt-api -n kubevirt

# 4. Verify certificate validity
kubectl get secret -n kubevirt kubevirt-virt-api-certs -o jsonpath='{.data.tls\.crt}' | \
  base64 -d | openssl x509 -noout -dates
```

---

### Scenario 2: CAPK Webhook Certificate Expired

**Symptoms:**
- KdcCluster worker pool creation fails
- Error: "x509: certificate has expired"
- Cannot create KubevirtMachineTemplate resources

**Emergency Recovery:**
```bash
# 1. Manually apply certificate resources
kubectl apply -f /path/to/kube-dc/installer/kube-dc/templates/kube-dc/cluster-api/capk-webhook-cert.yaml

# 2. Wait for cert-manager to issue certificate
kubectl wait --for=condition=Ready certificate/capk-serving-cert -n capk-system --timeout=60s

# 3. Restart CAPK controller to reload certificate
kubectl rollout restart deployment/capk-controller-manager -n capk-system

# 4. Verify webhook is operational
kubectl rollout status deployment/capk-controller-manager -n capk-system
```

**Prevention:**
- ✅ **Fixed:** Certificate now managed by cert-manager in installer
- ✅ **Automatic renewal:** 30 days before expiry

---

### Scenario 3: Kyverno Certificate Expired (May 2026)

**Symptoms:**
- Policy validation fails
- Resources created without policy enforcement
- Webhook errors in admission controller

**Manual Renewal (Temporary):**
```bash
# Force Kyverno to regenerate certificates
kubectl delete secret -n kyverno kyverno-svc.kyverno.svc.kyverno-tls-pair
kubectl delete secret -n kyverno kyverno-cleanup-controller.kyverno.svc.kyverno-tls-pair

# Restart Kyverno controllers
kubectl rollout restart deployment -n kyverno
```

**Permanent Solution (Recommended):**
Migrate Kyverno to cert-manager management (see Future Improvements section).

---

## Installer Integration

### CAPK Webhook Certificate Auto-Configuration

**Location:** `/home/voa/projects/kube-dc/installer/kube-dc/templates/kube-dc/cluster-api/`

**Files Added:**
1. `capk-webhook-cert.yaml` - Certificate and Issuer manifests
2. `install.sh` - Updated to apply certificates after clusterctl init

**Installation Flow:**
```bash
# 1. clusterctl init creates capk-system namespace and webhook
# 2. Script waits for namespace to be ready
# 3. Applies Issuer + Certificate resources
# 4. cert-manager issues certificate immediately
# 5. CAPK webhook loads new certificate
```

**Certificate Specification:**
```yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: capk-serving-cert
  namespace: capk-system
spec:
  dnsNames:
  - capk-webhook-service.capk-system.svc
  - capk-webhook-service.capk-system.svc.cluster.local
  issuerRef:
    kind: Issuer
    name: capk-selfsigned-issuer
  secretName: capk-webhook-service-cert
  duration: 8760h  # 1 year
  renewBefore: 720h  # 30 days before expiry
```

**Template Integration:**
```yaml
# In template.yaml
create_files:
  - file: capk-webhook-cert.yaml
    content: {{ insertYAML (readFile "./cluster-api/capk-webhook-cert.yaml") }}
```

**Benefits:**
- ✅ Automatic deployment on new installations
- ✅ cert-manager handles renewal
- ✅ No manual intervention required
- ✅ 30-day renewal window ensures safety margin

---

## Future Improvements

### Priority 1: Migrate Kyverno to cert-manager

**Deadline:** Before May 2026

**Implementation:**
1. Create Certificate resources for Kyverno webhooks
2. Configure cert-manager Issuer
3. Update Kyverno webhook configurations to use cert-manager certs
4. Test certificate renewal process
5. Document rollback procedure

### Priority 2: Enhanced Monitoring Dashboard

**Grafana Dashboard:**
- Certificate expiration timeline (all components)
- Webhook health status
- Certificate rotation events
- Alert history

### Priority 3: Automated Certificate Health Checks

**Kubernetes CronJob:**
```yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: certificate-health-check
  namespace: monitoring
spec:
  schedule: "0 */6 * * *"  # Every 6 hours
  jobTemplate:
    spec:
      template:
        spec:
          containers:
          - name: cert-check
            image: alpine/openssl
            command:
            - /bin/sh
            - -c
            - |
              # Check all webhook certificates
              # Send alerts if < 7 days remaining
              # Report to monitoring system
```

---

## Maintenance Calendar

### Monthly Tasks
- [ ] Review certificate expiration status
- [ ] Verify cert-manager health
- [ ] Check operator pod logs for cert rotation
- [ ] Review certificate monitoring alerts

### Quarterly Tasks
- [ ] Audit all webhook configurations
- [ ] Verify certificate backup procedures
- [ ] Test certificate renewal process
- [ ] Update documentation

### Critical Dates (2026)
- **May 31, 2026:** Kyverno certificates expire ⚠️ **ACTION REQUIRED**
- **August 6, 2026:** Kyverno CA certificates expire ⚠️ **ACTION REQUIRED**
- **December 2026:** Review all cert-manager certificates for 2027 renewal

---

## References

### Related Documentation
- [CAPK Webhook Certificate Fix](./capk-webhook-certificate-fix.md) (if created)
- [cert-manager Documentation](https://cert-manager.io/docs/)
- [KubeVirt Certificate Management](https://kubevirt.io/user-guide/operations/certificate_rotation/)
- [Kyverno Certificate Management](https://kyverno.io/docs/installation/)

### Support Contacts
- **Platform Team:** Contact for certificate issues
- **On-Call Rotation:** Emergency certificate expiration response
- **Vendor Support:** KubeVirt, Kyverno for upstream certificate issues

---

## Revision History

| Version | Date | Author | Changes |
|---------|------|--------|---------|
| 1.0 | Jan 12, 2026 | System | Initial documentation after CAPK certificate incident |

---

**⚠️ CRITICAL REMINDER:**

**Certificates are security-critical infrastructure components. Expired certificates can cause complete platform outages affecting all tenant clusters. This documentation must be reviewed quarterly and updated whenever new components are added to the platform.**
