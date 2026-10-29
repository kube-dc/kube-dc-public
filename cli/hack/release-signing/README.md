# CLI release signing

**Off for now.** Released CLIs update themselves (`cli/internal/selfupdate`)
and verify each binary against the release's `checksums.txt`, fetched over
HTTPS from GitHub. That protects against corruption and tampering in
transit, but not against a compromised release. This directory turns on the
next layer: an ed25519 signature of `checksums.txt` (`checksums.txt.sig`)
that every CLI carrying the matching `selfupdate.PublicKey` requires. Once a
key is in a released CLI, that CLI refuses unsigned releases.

To turn it on, in this order:

- complete the setup below;
- restore the `signs:` entry in `cli/.goreleaser.yaml`:

  ```yaml
  signs:
    - id: checksums
      artifacts: checksum
      cmd: go
      args: ["run", "./hack/signchecksums", "${artifact}", "${signature}"]
      signature: "${artifact}.sig"
  ```

- set `PublicKey`;
- release.

Releases made before that remain installable only by CLIs without a key.

The private key lives only as the environment secret
`KUBE_DC_UPDATE_SIGNING_KEY` of the GitHub Environment `release` in
kube-dc/kube-dc-public. That environment admits only `v*` tags and needs an
approval by bohdaq or voatsap, so a modified workflow, or an unapproved run,
never reads it.

One-time setup, by a repo admin of kube-dc-public:

1. `bash cli/hack/release-signing/setup.sh .` from a kube-dc checkout. It:
   - creates the environment;
   - generates the key pair on that machine, stores the private seed and
     destroys it;
   - prints the public key.
2. Put the printed public key in `cli/internal/selfupdate/selfupdate.go`
   (`PublicKey`) and release the CLI with it.
3. Apply `cli-release-workflow.patch` to kube-dc-public's
   `.github/workflows/cli-release.yaml` (`environment: release` and the
   secret passed to GoReleaser).

Until all three are done, a release's signature does not match the key in
the CLI, and CLIs refuse to install it: the failure is safe. Rotating the key
is the same procedure. CLIs that carry the old public key then stop updating
themselves until they are updated once by hand (`kube-dc update` refuses as
well), so rotate only when the key is compromised.
