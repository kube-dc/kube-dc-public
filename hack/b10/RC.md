# B-10 release candidate

**Tag:** `v0.5.35-b10rc1`
**Built:** 2026-07-29T06:52:35Z
**Commit:** `7c5424db29bc8a4fb5528e5ab7ebb3cf48b61781`
**Tree:** code paths clean at build time (only docs were dirty)

| Image | Digest |
|---|---|
| `shalb/kube-dc-manager` | `sha256:64f6dcdfdffae9076af0cedd92870a3315dc79fe7629691b3447bbc9536d614c` |
| `shalb/kube-dc-ui-backend` | `sha256:7856f8bd3d1c0e5ee2a9be14907e600905e97d4a3798b448bf581a64038e7cb9` |
| `shalb/kube-dc-admin-frontend` | `sha256:5a3d9f40cea7fc8bbdafb5a9fff97c64738b9c9c4a68f36d817944fea734320a` |
| `shalb/kube-dc-ui-frontend` | `sha256:4e165ea872a03daed90c4a3b4fb56d2cd56f5baa30cadb37dbe1ccf8d083b937` |

Contains the full tenant-VLAN feature: M0-M4 controller + admission,
M6 admin console and tenant card, and both review rounds
(`515c3d2a`, `247b79b4`) including verified TLS on every VLAN path.
