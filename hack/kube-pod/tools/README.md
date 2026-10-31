# Build cloud-shell clients

Release engineers use these locks to rebuild the Linux AMD64 clients bundled in
the cloud shell. The platform Go modules retain their own toolchain selection;
these external clients use Go 1.27.3 because Stern requires Go 1.27.2 or later.

Before building, install Go 1.27.3, Python 3, Docker and Bash. Use a clean checkout
with the released CLI version recorded in `runtime-lock.json`. Publishing also
requires the source commit on `origin/main` and an unused registry tag.

Build and check the image without publishing it:

```sh
KUBE_POD_TAG=v0.9.12 KUBE_DC_CLI_VERSION=v0.9.12 \
  GO=/opt/go1.27.3/bin/go bash hack/kube-pod/build.sh --no-push
```

Replace `/opt/go1.27.3/bin/go` with your Go 1.27.3 executable. The builder verifies
the upstream Stern and K9s module checksums and source revisions, overlays the
committed `go.mod` and `go.sum`, and builds those clients and kubectl. It verifies
the checksums of the Helm, virtctl, ttyd and released CLI assets before extracting
individual executables. Every rebuilt Go client must also match the qualified
executable hash in the runtime lock and assessment record. A dependency change
requires new scan evidence and hashes before the release preflight passes.
The ttyd checksum is a recorded HTTPS download hash;
that upstream release does not publish an asset digest.

The Docker context includes only the runtime clients and startup scripts.
The image verifies all seven executable checksums and generates Bash, Zsh and
Fish completions. Its smoke check runs every client without network access.
The Alpine 3.24 base and terminal HTML source use immutable image digests.
Packages receive security updates during the build. GNU coreutils preserve the
existing date and timeout behavior; Bash, Zsh and Fish remain available.
ZMODEM uses lrzsz 0.13.1 with the checksummed Debian receive-buffer patches and
its upstream quickcheck suite. The update removes unsafe remote-command and
pipe modes; ordinary `rz`/`sz` file transfers remain available.

For dependency changes, run the upstream short package suites for Stern and K9s
in a network namespace with only loopback, an empty kubeconfig and no Linux
capabilities. Keep the test logs and binary build information with the release
evidence. The cloud-shell source locks reuse the kubectl entry point and patched
dependency selection from Kube-OVN fork commit `8eb945ce17f40a41854072a6ffb155ceeab9486f`.

Before pushing, retain a complete image SBOM and vulnerability report, investigate
each Critical or High match against its upstream advisory, and check the
actual shell session against the target Kubernetes and KubeVirt versions.
Do not publish affected Critical or High vulnerabilities. Keep false-positive
assessments tied to the exact module, version and executable hash, together with
the unsuppressed report. The recorded K9s assessment distinguishes containerd v1
from the fixed v2 module; it does not waive findings in another executable.
Record the published image digest in the backend's project-console launcher and
the release manifest. Rebuild the backend after changing that active digest.

The terminal runtime retains Bash, Zsh, Fish, GNU coreutils, curl, BusyBox wget,
SSH, editors and the pinned Kubernetes clients. Midnight Commander and GNU Wget
are omitted: they are not used by console startup and introduced vulnerable
optional libraries or download modes. Use curl for downloads that need features
beyond BusyBox wget.
