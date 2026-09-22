# Dedicated publisher; the controller-manager remains explicitly denied these
# paths. '+' is a complete Org/mount segment (OpenBao has no 'kv-+' wildcard).
# The publisher validates the Project's provisioned UID-bound publication mount before use.
# No list, ordinary tenant-secret, auth administration or database grants.
path "+/+/data/kube-dc-svc-*" {
  capabilities = ["create", "read", "update"]
}
# Read the current CAS counter when a version was soft-deleted.
path "+/+/metadata/kube-dc-svc-*" {
  capabilities = ["read"]
}
# Retirement keeps a value-free CAS tombstone and destroys historical values.
path "+/+/destroy/kube-dc-svc-*" {
  capabilities = ["update"]
}
path "auth/token/renew-self" {
  capabilities = ["update"]
}
