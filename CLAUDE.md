# Kube-DC — Claude Code Instructions

See @_agent-instructions.md for full platform instructions, CRD reference, naming conventions, and safety rules.

## Knowledge Base

- @knowledge/index.md — Master catalog of all CRDs, skills, docs, and examples
- @docs/cloud/ — User-facing documentation per resource domain
- @examples/ — Ready-to-use YAML manifests

## Skills

Skills are in @skills/ — each skill has a SKILL.md with step-by-step procedures and supporting templates.

## Key Rules

- Follow Kubernetes naming conventions for CRDs and resources
- All user resources MUST be created in project namespaces (`{org}-{project}`)
- VMs MUST include `qemu-guest-agent` in cloud-init
- Prefer `egressNetworkType: cloud` for new projects
- Users are managed via UI only (Keycloak) — no User CRD exists
- Never log database passwords or kubeconfig contents in chat output
