# GitHub App: deployment statuses

With a GitHub App configured, Kube-DC reports rollouts to GitHub. For a
Deployment that names the repository and commit it runs, GitHub shows a
deployment for that commit: **success** with the app's URL once it rolled out,
or **failure** when it did not. Pull requests and commits then show the real
state on the cluster, also for apps deployed outside CI (for example by an AI
agent through the hosted MCP tool `deploy_app`).

The feature is off unless configured. Each installation registers **its own**
App: the App's name is what customers see on their commits, and its private
key belongs to the operator of that installation.

| Installation | App |
|---|---|
| Kube-DC cloud (operated by SHALB) | [kube-dc-cloud](https://github.com/apps/kube-dc-cloud), owned by the `kube-dc` GitHub organization |
| Kube-DC stage (operated by SHALB) | A separate stage App, owned by the `kube-dc` GitHub organization |
| A provider (white label) or a self-hosted installation | One you register as below, under your own name; or leave the feature off |

## Register the App

As an owner of your GitHub organization: **Settings → Developer settings →
GitHub Apps → New GitHub App**.

| Field | Value |
|---|---|
| GitHub App name | Your product's name, for example `Example Cloud Deploys` |
| Homepage URL | Your product's site |
| Webhook | Uncheck **Active** (Kube-DC does not receive GitHub events) |
| Repository permissions | **Deployments: Read and write**. Metadata (read) is added by GitHub |
| Where can this GitHub App be installed? | **Any account**, so your customers can install it |

After **Create GitHub App**:

1. Note the **App ID** at the top of the App's page.
2. Under **Private keys**, click **Generate a private key**. GitHub downloads a
   `.pem` file once and keeps only its fingerprint. Keep the file like a
   password: whoever holds it can act as the App on every repository that
   installed it.

## Give the cluster the key

Create a Secret in the Kube-DC namespace (with Fleet, encrypt it with SOPS like
the other platform credentials):

```bash
kubectl -n kube-dc create secret generic github-app \
  --from-file=private-key.pem=./your-app.private-key.pem
```

Enable it in the kube-dc chart values:

```yaml
githubApp:
  enabled: true
  appId: "1234567"
  secretName: github-app          # default
  privateKeyKey: private-key.pem  # default
```

Only the backend process that records platform events (the worker, or the
single API pod without a worker) mounts the key. Then delete your local copy of
the `.pem`.

## Rotate or revoke the key

Generate a new private key on the App's page, replace the Secret's
`private-key.pem`, restart the backend worker, and delete the old key on GitHub.
Deleting the key alone stops every status at once.

## What the cluster sends

For each `DeploymentRolledOut` or `DeploymentFailed` platform event of a
Deployment annotated with `github.kube-dc.com/repository` (`owner/repo`) and
`github.kube-dc.com/sha` (the commit), the backend:

1. signs a 9-minute App token with the private key;
2. finds the App's installation on that repository; a repository that did not
   install the App is skipped and logged at info level;
3. takes an installation token limited to that repository and to deployments
   (one hour, cached);
4. creates a GitHub deployment for the commit in the environment named by
   `github.kube-dc.com/environment` (default `production`) and sets its status,
   with the app's HTTPS URL on success.

Nothing is retried after a GitHub error; the platform event itself is kept.
