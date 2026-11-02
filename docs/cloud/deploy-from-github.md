# Deploy from GitHub

Build and deploy a GitHub repository on every push, with a preview for every
pull request. In the console open your project, then **Overview → Deploy from
GitHub** (or the *Deploy from GitHub* card on the project summary). The page
fills in every value it knows; you type only the repository and the app name.

## 1. Install the GitHub App and allow your organization

1. **Install the GitHub App** on the repository (button on the page). It
   reports deployments and check runs on your commits, builds on push and
   removes previews when a pull request closes.
2. **Allow this organization.** Sign in to GitHub when asked; Kube-DC checks
   that you are an admin of the repository and sets the repository variable
   `KUBE_DC_ORGANIZATION` to your organization.

The page checks both and shows what is missing. Without the button you can
set the variable yourself:

```bash
gh variable set KUBE_DC_ORGANIZATION -R owner/repo -b <organization>
```

**Why the variable:** one GitHub App serves every organization on the
platform. The variable is the repository admins' consent that *this*
organization may build the repository; without it, anyone who knows a
repository name could ask the platform to build its code. Only repository
admins can set repository variables.

## 2. Choose where images go

| | Managed (recommended) | Your own registry (for example GHCR) |
|---|---|---|
| Registry | A [Kube-DC container registry](managed-services-registry.md) in your project | Your GHCR or another registry |
| Secrets in GitHub | None: CI signs in with a short-lived token | None for workflows (GitHub's `GITHUB_TOKEN`); builds on push need a personal token saved in the project |
| Pulling | Kube-DC keeps the pull Secret fresh | Public images need nothing; private ones need a pull Secret |

**Own registry and builds on push: the GitHub token.** GitHub does not let
apps push to GHCR, so builds on push need a classic personal access token:

1. **Create a token on GitHub** (button on the page; scope `write:packages`).
2. **Save it in this project** with the form on the page. It becomes a
   `kubernetes.io/dockerconfigjson` Secret (default `<app>-ghcr-push`) and is
   selected as the push Secret. Developers usually cannot create Secrets: ask
   a project admin.

Never paste the token into a chat or an AI app.

## 3. Choose how builds run

| | On push, by the platform | In a GitHub Actions workflow |
|---|---|---|
| Where it builds | A BuildKit pod in your project, started by the push | A workflow job on [managed runners](managed-services-github-runners.md) or GitHub-hosted runners |
| In your repository | Nothing; the app's Deployment carries `build.kube-dc.com/*` annotations | `.github/workflows/deploy.yml` and `preview.yml` |
| Steps | `docker build` of the Dockerfile, push, roll out | Anything: tests, migrations, several images |
| Feedback | Check run `kube-dc` and a GitHub deployment on the commit | The workflow's log in the Actions tab |
| Previews | `<app>-pr-<n>` in the previews project, removed when the PR closes | Done by `preview.yml` |

Use one per app; both would build and deploy every push twice. Platform
builds suit "deploy my Dockerfile"; repeated builds reuse a layer cache (a
rebuild of an unchanged app took 26 seconds on stage). Use a workflow when you
need tests or custom steps.

## 4. Let your AI app do it (or do it by hand)

Connect an AI app to the project (MCP), then copy the prompt the page builds
from your choices. It uses:

- `deploy_app` with `buildFrom` for builds on push;
- `connect_repository` for a workflow (it opens a pull request with the
  workflows and sets the repository variables; organization admins only);
- `create_managed_service` for a registry or runner pool;
- `registry_login` for a `docker login` command.

*Set it up by hand* on the page shows the same steps as commands and YAML.
For a workflow it fills the repository variables for every project member,
including the Kubernetes API address (`KUBE_DC_API_SERVER`) and its CA
(`KUBE_DC_CA_CERT`). The keyless
machine identity's client id and audience are filled for organization admins;
others see placeholders to ask an admin for.

## Builds on push: the annotations

| On the Deployment | Value |
|---|---|
| label `build.kube-dc.com/on-push` | `"true"` |
| `build.kube-dc.com/repository` | `owner/repo` |
| `build.kube-dc.com/image` | registry path without a tag, e.g. `images-my-project.<domain>/my-project/app` |
| `build.kube-dc.com/push-secret` | own registry only: the Secret from step 2 |
| `build.kube-dc.com/previews` | optional: a project for pull request previews |

A failed build is named in the Deployment's `build.kube-dc.com/last-error`
and in the check run. Builds count against the project quota (they request
200m CPU and may use up to 2 CPU, 26Gi of disk and 60 minutes).
