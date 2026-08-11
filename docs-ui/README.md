# Kube-DC Documentation

This directory contains the Docusaurus site published at
[docs.kube-dc.com](https://docs.kube-dc.com). The documentation source is split
between three independent surfaces: the user-facing Cloud Guide, the
operator-facing Platform Docs, and the capability-focused Platform Datasheet.

## Prerequisites

- Node.js version 20.0 or above
- npm (comes with Node.js)

## Installation

Install the locked dependency set:

```bash
npm ci
```

## Local Development

Start the development server:

```bash
npm run start
```

This starts a local development server at `http://localhost:3000`. Most changes
appear without restarting the server.

## Validation

Run the complete local validation before opening a pull request:

```bash
npm run typecheck
npm run build
```

The production build validates internal links and writes static content to
`build`. Broken internal links fail the build.

To inspect the production build locally:

```bash
npm run serve
```

## Project Structure

```text
docs/
|-- cloud/                    # User-facing Cloud Guide
|-- platform/                 # Operator-facing Platform Docs
`-- datasheet/                # Public overview and capability datasheets
docs-ui/
|-- static/                   # Site-wide static assets
|-- src/                      # Theme, components, styles, and pages
|-- docusaurus.config.ts      # Site and content-plugin configuration
|-- sidebarsCloud.ts          # Cloud Guide navigation
|-- sidebarsPlatform.ts       # Platform Docs navigation
`-- sidebarsDatasheet.ts      # Datasheet navigation
```

## Publishing

The public source mirror and GitHub Pages target are
[`kube-dc/kube-dc-public`](https://github.com/kube-dc/kube-dc-public). For a
documentation release, mirror `README.md`, `docs/cloud/`, `docs/platform/`, the
curated public files from `docs/datasheet/`, `docs-ui/`, and `static/diagrams/`
from the product source. Keep public-owned `.github/` workflows and `skills/`
content in the public repository.

A push to `main` that changes `docs/**` or `docs-ui/**` starts the
[Deploy Docs to GitHub Pages](https://github.com/kube-dc/kube-dc-public/actions/workflows/deploy-docs.yml)
workflow. It installs the locked dependencies, type-checks and builds Docusaurus,
uploads
`docs-ui/build` as a Pages artifact, and deploys it to the `github-pages`
environment. The same workflow can be started manually from GitHub Actions.

GitHub Pages serves the artifact at `docs.kube-dc.com` using the CNAME in
`docs-ui/static/CNAME`. The Dagger `docs-check` function performs the
non-publishing governance, dependency, type, and build checks in an isolated
Node.js 20 container.

Do not use `npm run deploy` for normal releases. That Docusaurus command
writes the legacy `gh-pages` branch and bypasses the reviewed Pages workflow.
