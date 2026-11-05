# Installation decisions

Propose each decision with its reason; the operator confirms. Input keys are
for the `bootstrap init --config` file (@install-input.env.example); the
matching flags are in `kube-dc bootstrap init --help`.

## 1. Roles

| Servers | Proposal |
|---|---|
| 1 | Lab only: one control-plane node, `rook-ceph-local`, layer `none` |
| 3 | Three control-plane nodes that also run workloads; HA etcd |
| 4 or more | Three control-plane nodes; the rest join as workers (`--role worker`) |

- `KUBE_OVN_MASTER_NODES`: the control-plane nodes' internal IPs.
- `KUBE_OVN_GW_NODES`: gateway node names, normally the three control-plane
  nodes. They announce the VIP and carry tenant external traffic.
- Ingress nodes (`KUBE_DC_INIT_INGRESS_NODES`): leave empty to use the gateway
  set, the recommended shape. Under a VIP layer they must be a subset of the
  gateway nodes; do not mix control-plane and worker ingress nodes.
- Manager nodes: with three or more, the Grafana and Keycloak PostgreSQL
  databases are placed one instance per manager node on `local-path`, and
  OpenBao too when `OPENBAO_REPLICAS` is at least 3 and at most the manager
  count (set `OPENBAO_REPLICAS=3`; the starter default is one member).

## 2. Network

| Preset | Use when |
|---|---|
| `cloud-vlan` | Projects use one external pool (normally private) on one VLAN; the site gateway provides internet access |
| `cloud+public-vlan` | Projects also need a public pool on a second VLAN; set `EXT_PUBLIC_CIDR`, `_GATEWAY`, `_VLAN_ID`, `_EXCLUDE_IPS_1`, `_EXCLUDE_IPS_2` |
| `internal-only` | Same network defaults as `cloud-vlan` (it adds no egress block) |
| `custom` | You maintain the whole environment |

External (cloud) network:

- `EXT_NET_INTERFACE`: the carrier (bond or NIC); `--node-nic NODE=IFACE`
  where a server differs. `EXT_NET_VLAN_ID`: the tag, `0` for untagged.
- `EXT_NET_CIDR`, `EXT_NET_GATEWAY`, `EXT_NET_MTU` (start at 1500).
- `EXT_NET_EXCLUDE_IPS`: the gateway and everything the site already uses,
  as one `a..b` range from the start; `EXT_NET_EXTRA_EXCLUDE_IPS` for further
  reserved ranges. `EXT_NET_MGMT_SNAT_IP`: one reserved address for the
  management VPC's SNAT.
- Keep `KUBE_DC_INIT_NODE_EGRESS_ENABLED=false` unless the site gateway cannot
  forward tenant traffic; it turns gateway nodes into NAT routers.

Internal ranges must not overlap any site network: `POD_CIDR` (default
`10.100.0.0/16`), `SVC_CIDR` (`10.101.0.0/16`, `CLUSTER_DNS` `.11`),
`JOIN_CIDR` (`172.30.0.0/22`), `INFRA_ATTACHMENT_CIDR` (default
`100.66.0.0/16`, shared address space). Pass
the same `POD_CIDR`, `SVC_CIDR` and `CLUSTER_DNS` to `bootstrap install`.
`INFRA_ATTACHMENT_ROUTES` lists the node subnet, join subnet and pod CIDR.

## 3. Front door

`INGRESS_ADDRESS_LAYER` decides who owns the address users dial. Envoy runs
on the ingress nodes' host network in every case.

| Layer | Needs | Propose when |
|---|---|---|
| `metallb-l2` | A reserved VIP (`METALLB_FLOATING_IP`) on a segment all ingress nodes share; `METALLB_INTERFACE` | Recommended: one floating address for DNS, fails over in seconds |
| `metallb-bgp` | A VIP plus router peering: `METALLB_BGP_LOCAL_ASN`, `_PEER_ASN`, `_PEER_ADDRESS` | Routed fabric, no shared L2 |
| `none` | DNS points at the ingress nodes' own addresses (or an upstream NAT rule) | One node, or no spare address |

`NODE_EXTERNAL_IP` is the client-facing address (the VIP, or the node address
with `none`), even on a private network.

Then check how Project Pods will reach that address. Project Pods have two
interfaces: the Project network (default route) and a locked infra interface
that only routes `INFRA_ATTACHMENT_ROUTES`. An address inside those routes is
dialled over the locked interface and dropped.

| Class | Layout | What to do |
|---|---|---|
| A | One public address behind 1:1 NAT; nodes never see it | Pass `--ssh-host`: `init` detects the NAT and the address that arrives, and with managed services on derives an internal front door on the cloud network. Reserve its addresses with the site |
| B | VIP or node addresses on a network outside `INFRA_ATTACHMENT_ROUTES` | Nothing extra |
| C | Cloud-provider load balancer | Nothing extra |
| D | VIP on the node network, inside `INFRA_ATTACHMENT_ROUTES` | With managed services on, layer `metallb-l2` and Envoy on the control-plane nodes, `init` derives an internal front door: consecutive cloud addresses after `EXT_NET_EXCLUDE_IPS` become the internal VIP and one anchor per gateway node (`KUBE_OVN_GW_NODES`), and Project DNS answers the domain with it. Reserve that range with the site (calculated, not checked free). With Envoy on workers `init` warns; with layer `none` or `metallb-bgp` it does nothing: configure the internal front door by hand (link below) |

After installation, `kube-dc bootstrap doctor topology` classifies A, B and C;
for D, test from a Project Pod (SKILL.md step 9). Details:
[internal platform endpoints](https://github.com/kube-dc/kube-dc-public/blob/main/docs/platform/internal-platform-endpoints.md).

## 4. DNS

| Record | Points at |
|---|---|
| `*.DOMAIN` | The front-door address |
| `kube-api.DOMAIN` | The same address (API SNI on 6443) |

Both must resolve from the workstation and from the servers before `init`
(the servers' resolvers are what in-cluster DNS forwards to; `/etc/hosts` is
not enough). S3 is path-style at `s3.DOMAIN` (`S3_HOSTNAME`), covered by the
wildcard.

## 5. Certificates

| Mode (`TLS_MODE`) | Use when | Inputs |
|---|---|---|
| `acme` (default) | Public DNS and inbound port 80 to the front door | `EMAIL`. HTTP-01 issues every per-host certificate but never the platform wildcard (`*.DOMAIN`, used for tenant hostnames): add Cloudflare DNS-01 for the wildcard only (`DNS01_CLOUDFLARE_ZONE` plus the token), or choose a DNS-01 mode or `byo-wildcard` |
| `acme-dns01-route53` | Private or VPN-only front door, zone in Route53 | Zone ID, region, access key ID; secret key file or `KUBE_DC_DNS01_ROUTE53_SECRET_KEY` |
| `acme-dns01-cloudflare` | Private or VPN-only front door, zone on Cloudflare | Token file or `KUBE_DC_DNS01_CLOUDFLARE_API_TOKEN`; optional zone |
| `byo-wildcard` | The site supplies a certificate | `KUBE_DC_INIT_TLS_CERT`, `KUBE_DC_INIT_TLS_KEY`: a chain for `*.DOMAIN` (add `DOMAIN` if the apex is served); nothing renews it, so record its expiry |

DNS-01 needs the challenge records in a publicly resolvable zone (or one
delegated from it); the platform's own addresses may stay private.

A certificate from a private CA also needs the CA-only bundle as
`KUBE_DC_INIT_TRUSTED_CA_BUNDLE`, and the same bundle on every
`bootstrap install` (`--trusted-ca-bundle`). Trust it on the workstation too.

## 6. Storage

| `OBJECT_STORAGE_MODE` | Use when |
|---|---|
| `rook-ceph-multi-node` | Production: one empty raw disk per OSD node (`CEPH_NODE_<n>`, `CEPH_NODE_<n>_DEVICE`), 1 to 16 nodes |
| `rook-ceph-local` | One-node lab: a disk or a backing file (`CEPH_LOCAL_OSD_NODE`, `_DEVICE`, `_SIZE_GB`) |
| `rook-ceph-pvc` | The cluster already has a CSI StorageClass and no raw disks |
| `disabled` | Never for real use: no metrics or log storage, no platform backups |

- `CEPH_REPLICATION_SIZE`: set `3` explicitly with three or more OSD hosts;
  unset, it is one copy on one storage server and two on two or more.
- `CEPH_MON_NODES`: the three control-plane nodes.
- VM root disks: `KUBE_DC_INIT_VM_STORAGE_MODE=shared-rbd` (VM disks on shared
  Ceph RBD, with snapshots and clones; needs a Ceph mode; live migration is a
  separate post-install configuration) or `local`; golden images with
  `KUBE_DC_INIT_VM_GOLDEN` (for example `debian-12`).
- Monitoring storage: `MONITORING_STORAGE_CLASS`, `PROM_STORAGE_CLASS`,
  `LOKI_STORAGE_CLASS` = `ceph-block` with Ceph.
- Managed services (`KUBE_DC_INIT_MANAGED_SERVICES`): `auto` enables them when
  storage and budget qualify; `on` requires that. The database class is
  derived: `ceph-block` for `rook-ceph-local` and `rook-ceph-pvc`, `rbd-vm` for
  `rook-ceph-multi-node` with `shared-rbd` VM disks. Multi-node Ceph with local
  VM disks needs `KUBE_DC_INIT_SERVICES_DATABASE_CLASS`, an expandable block
  StorageClass. `KUBE_DC_INIT_SERVICES_STORAGE_BUDGET` is the total the catalog
  may allocate.
- Capacity: usable Ceph space is raw capacity divided by the copies. Fit
  the managed-services budget, VM disks, monitoring volumes, object data and
  backups inside it with room for recovery after a host loss, and agree the
  numbers with the operator before choosing the budget.

## 7. Fleet repository

| `KUBE_DC_INIT_FLEET_MODE` | Use when |
|---|---|
| `new-repo` | The CLI creates the repository (`KUBE_DC_INIT_PROVIDER`, owner, name) |
| `existing-repo` | A Git checkout without a fleet; the CLI imports the starter's shared trees and keeps the repository's own files |
| `existing-fleet` | Add a cluster to a repository that already has clusters |

`KUBE_DC_INIT_STARTER_REF`: the starter of the release, as an immutable
reference (`oci://ghcr.io/kube-dc/fleet-starter@sha256:...` or a version tag),
never `latest`. Flux follows the checked-out branch.
