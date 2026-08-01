---
name: ssh-into-vm
description: Connect securely to a Kube-DC virtual machine through the browser, an approved private route, a Floating IP, or an EIP-bound LoadBalancer Service.
---

# SSH into a VM

A VM's VMI address is private to its Project network unless the environment
provides a route to it. `kubectl proxy` does not make that address reachable,
and standard Project roles cannot use VM/VMI port-forward.

## Prerequisites

- The VM is Running.
- QEMU Guest Agent reports connected and the VM references the intended
  authorized-key Secret.
- The default SSH user comes from the live OS catalog.
- A reachable path exists: browser SSH, private routing, FIP, or LoadBalancer.
- The caller is authorized to read the selected private-key Secret.

The Kube-DC browser SSH terminal is the simplest path and does not require
writing the Project private key to disk.

## 1. Check VM access state

```bash
kubectl get virtualmachine {vm} \
  -o jsonpath='{.status.printableStatus}{"\n"}'
kubectl get virtualmachineinstance {vm} \
  -o jsonpath='{.status.conditions[?(@.type=="AgentConnected")].status}{"\n"}'
kubectl get virtualmachineinstance {vm} \
  -o jsonpath='{.status.interfaces[?(@.name=="default")].ipAddress}{"\n"}'
```

`AgentConnected=True` confirms the guest agent connection. It does not prove
that sshd is listening or that a workstation can route to the address.

## 2. Choose the reachable address

### Private route

Use the VMI address only from a host connected to the Project network through
an operator-approved route, VPN, bastion, or equivalent network path.

### Floating IP

```bash
kubectl get fip {vm}-fip \
  -o custom-columns='READY:.status.ready,ADDRESS:.status.externalIP,TARGET:.status.resolvedTargetIP'
```

Wait for `READY=true` and use `ADDRESS`. Follow `manage-networking` when the FIP
does not exist.

### EIP-bound LoadBalancer

```bash
kubectl get service {vm}-ssh \
  -o jsonpath='{.status.loadBalancer.ingress[0].ip}{"\n"}'
```

Use the Service's external port, which may differ from guest port 22. Follow
`expose-service` for Service and EIP creation.

## 3. Handle the private key

Projects generate:

| Secret | Keys | Purpose |
|---|---|---|
| `ssh-keypair-default` | `id_rsa`, `id_rsa.pub` | Project RSA keypair |
| `authorized-keys-default` | `admin` | Public key injected by KubeVirt |

When the generated key is appropriate, extract it to a protected temporary
file. Do not echo it:

```bash
SSH_KEY="$(mktemp)"
trap 'rm -f "$SSH_KEY"' EXIT

kubectl get secret ssh-keypair-default \
  -o jsonpath='{.data.id_rsa}' | base64 -d > "$SSH_KEY"
chmod 600 "$SSH_KEY"
```

The private key is shared at Project scope. For narrower access, create a
separate Secret containing only approved public keys and reference that Secret
from the VM's `accessCredentials` instead of changing the generated keypair.

```bash
kubectl create secret generic {vm}-authorized-keys \
  --from-file={key-owner}=/path/to/id_ed25519.pub
```

VM fragment:

```yaml
accessCredentials:
  - sshPublicKey:
      source:
        secret:
          secretName: "{vm}-authorized-keys"
      propagationMethod:
        qemuGuestAgent:
          users:
            - "{catalog-default-user}"
```

## 4. Connect

Direct address:

```bash
ssh -i "$SSH_KEY" {catalog-default-user}@{reachable-address}
```

LoadBalancer with a translated port:

```bash
ssh -i "$SSH_KEY" -p {external-port} \
  {catalog-default-user}@{loadbalancer-address}
```

Verify the server host key through the provider's trusted channel before
accepting it. Do not disable host-key checking in automation.

## Troubleshooting

- **Permission denied**: confirm the catalog user, referenced public-key Secret,
  guest-agent connection, and filesystem permissions on the private key.
- **Connection refused**: confirm sshd is installed and running in the guest and
  the Service `targetPort` is correct.
- **Timeout**: confirm the selected address is reachable, FIP or Service status
  is Ready, quota allowed the public IP, and intervening firewalls permit SSH.
- **No VMI address**: inspect VM/VMI events and guest-agent configuration.
- **Key changed but login still fails**: guest-agent propagation is
  asynchronous; check `AgentConnected` and wait for the VM access credential
  condition.

Use the browser console for guest-side repair when SSH is unavailable.

## Safety

- Never include private-key bytes in chat, logs, tickets, or shell history.
- Use `mktemp`, mode `0600`, and cleanup on exit.
- Prefer per-VM public keys when Project-wide sharing is too broad.
- Remove public exposure when it is no longer required.
- Do not present `kubectl proxy`, `virtctl ssh`, or port-forward as standard
  tenant access paths.
