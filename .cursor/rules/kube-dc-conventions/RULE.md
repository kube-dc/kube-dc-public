---
description: Kube-DC platform conventions for CRDs, namespaces, networking, and security
alwaysApply: true
---

# Kube-DC Conventions

## Namespace Rules
- All user resources MUST be created in project namespaces (`{org}-{project}`)
- Organization resources live in namespace `{org}`
- Never create resources in `kube-system`, `kube-dc`, or other system namespaces

## CRD Naming
- Follow Kubernetes naming conventions: lowercase, alphanumeric, hyphens
- API groups: `kube-dc.com`, `k8s.kube-dc.com`, `db.kube-dc.com`
- Use short names where available: `vm`, `dv`, `kdcdb`, `kdc-cl`, `obc`

## VM Requirements
- MUST include `qemu-guest-agent` in cloud-init
- MUST use `networkName: {namespace}/default` with Multus bridge
- MUST reference `authorized-keys-default` for SSH key injection
- Use `storageClassName: local-path` for DataVolumes

## Service Exposure
- HTTP/HTTPS → Gateway Route: `service.nlb.kube-dc.com/expose-route: "https"`
- TCP/UDP → Direct EIP: create EIp + `service.nlb.kube-dc.com/bind-on-eip`
- Never mix FIP and LoadBalancer on the same target

## Database Conventions
- PostgreSQL secret: `{name}-app`, endpoint: `{name}-rw.{ns}.svc:5432`
- MariaDB secret: `{name}-password`, endpoint: `{name}.{ns}.svc:3306`
- Default to `internal` exposure; use `gateway` only when external access is needed

## Security
- Never log passwords, kubeconfig contents, or API tokens in output
- Users are managed via UI only (Keycloak) — no User CRD exists
- Prefer `egressNetworkType: cloud` for new projects
- OBC resources MUST have label `kube-dc.com/organization: {org}`

## Reference
- Docs: `docs/cloud/` and `docs/platform/`
- Examples: `examples/`
- Skills: `skills/`
- Knowledge: `knowledge/`
