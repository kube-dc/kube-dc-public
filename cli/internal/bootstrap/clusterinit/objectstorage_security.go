package clusterinit

// New storage must wait for every Linux host's kernel check before Rook can
// generate AES256K keys. A failing DaemonSet keeps this Flux dependency unready.
const infraStoragePrerequisitesYAML = `apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: infra-storage-prerequisites
  namespace: flux-system
spec:
  dependsOn:
    - name: infra-core
  interval: 10m
  retryInterval: 1m
  timeout: 5m
  path: ./infrastructure/object-storage/host-prerequisites
  prune: true
  sourceRef:
    kind: GitRepository
    name: flux-system
  healthChecks:
    - apiVersion: apps/v1
      kind: DaemonSet
      name: rook-ceph-host-compatibility
      namespace: rook-ceph
`

const secureCephXPatch = `patches:
  - target:
      group: ceph.rook.io
      version: v1
      kind: CephCluster
      name: rook-ceph
      namespace: rook-ceph
    patch: |
      apiVersion: ceph.rook.io/v1
      kind: CephCluster
      metadata:
        name: rook-ceph
        namespace: rook-ceph
      spec:
        security:
          cephx:
            allowedCiphers:
              - aes256k
            csi:
              keyType: aes256k
`
