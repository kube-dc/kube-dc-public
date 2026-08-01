# Community and Support

Use community channels for general questions and the support contact associated
with your account for private, billing, availability, or security-sensitive
issues.

## Self-Service

Start with:

1. Search this documentation for the resource and error message.
2. Check the relevant resource status and events in your Project.
3. Search existing [GitHub issues](https://github.com/kube-dc/kube-dc-public/issues).
4. Ask a general question in [GitHub Discussions](https://github.com/kube-dc/kube-dc-public/discussions).

The [Kube-DC Slack community](https://join.slack.com/t/kube-dc/shared_invite/zt-31mr5c6ci-W3kYQ7qGDULlGQ5QJjsxmA)
is available for community conversation. Do not post credentials, kubeconfigs,
tokens, private logs, customer data, or security reports in a public channel.

## Open an Issue

Use GitHub for reproducible bugs in public components. Include:

- expected and actual behavior
- the smallest reproducible example
- Kube-DC CLI and relevant Kubernetes versions
- timestamps with timezone
- sanitized error messages and events
- whether the issue affects a Project or a Managed Cluster

Before posting, remove Organization names if private, Project data, IP addresses,
tokens, Secret values, kubeconfigs, and customer information.

## Contact Support

For account-specific help, use the support channel included with your
subscription or email [support@kube-dc.com](mailto:support@kube-dc.com).
Response targets and coverage are defined by your subscription or support
agreement; this page does not add an SLA.

Include:

- Organization and Project names
- affected resource type and name
- impact and start time
- recent change, if known
- request or correlation ID, if the UI shows one
- sanitized status, conditions, and events
- a safe way to reproduce the problem

Never send a bearer token, password, private key, kubeconfig, or unredacted
Secret. Support can request a safer diagnostic through the authenticated
channel when needed.

## Route the Request

| Request | Best channel |
|---------|--------------|
| How-to or design question | Documentation, Discussions, or Slack |
| Reproducible public-component bug | GitHub Issues |
| Billing or subscription | Account support |
| Production availability | Account support |
| Suspected security vulnerability | Private support channel; do not open a public issue |
| Feature idea | GitHub Discussions or account contact |

## Contributions

Contributions and documentation feedback are welcome through the
[kube-dc-public repository](https://github.com/kube-dc/kube-dc-public). Open an
issue or discussion before a large change so maintainers can confirm scope and
the appropriate repository.
