---
trigger: always_on
---

# Kube-DC Conventions

`AGENTS.md` is the canonical product model and safety reference; read it and
the matching `skills/<name>/SKILL.md` before changing anything. The rules most
often missed:

## Namespaces
- All user resources go in the Project's backing namespace `{org}-{project}`.
- Organization resources live in namespace `{org}`.
- Never create resources in `kube-system`, `kube-dc` or other system namespaces.

## Databases, caches and queues
- Use managed services (`ManagedService` + `ServiceBinding`, skill
  `create-database`), never a database in a pod or a chart's bundled database.
- Plans are `{family}-development` (one instance, the default) and
  `{family}-production` (HA, only when the user asks for production or HA).
  Read the offered plans with `kubectl get serviceofferings`.
- Read `host`, `port`, `username`, `password` from the binding Secret; every
  endpoint is TLS-only. Do not build host names.

## Service exposure
- HTTP/HTTPS: a `ClusterIP` Service with
  `service.nlb.kube-dc.com/expose-route: "https"`. The platform issues the
  certificate and assigns `{svc}-{namespace}.{installation domain}`; no Issuer,
  no external IP. Never `type: LoadBalancer` for web traffic.
- TCP/UDP: a `LoadBalancer` on the Project gateway address
  (`bind-on-default-gw-eip: "true"`); a dedicated EIP or FIP only when the user
  asks for one, after checking quota.
- Never make one VM or pod both a public FIP target and a cloud LoadBalancer
  backend.

## VMs
- Include `qemu-guest-agent` in cloud-init.
- Use the Project's `authorized-keys-default` Secret for SSH keys.
- Use a VM StorageClass the Project offers; do not assume `local-path`.

## Security
- Never print passwords, kubeconfig contents or API tokens.
- Users are managed in the console (Keycloak); there is no User CRD.
- Prefer `egressNetworkType: cloud` for new Projects.
- Give ObjectBucketClaims the label `kube-dc.com/organization: {org}`.
