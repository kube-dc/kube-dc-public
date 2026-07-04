package ports

import "context"

// SOPSClient is the contract for SOPS encryption operations against the
// fleet repo's `secrets.enc.yaml` files. Real adapter shells out to `sops`
// (the canonical CLI matches the fleet team's existing tooling); mock
// adapter maintains an in-memory keyed map.
//
// **Why `SetStringData` exists separately from `Encrypt`**: when M5-T01
// writes 5 OpenBao shares into an existing `secrets.enc.yaml`, doing
// 5× full-file decrypt → mutate → re-encrypt creates noisy diffs in
// git history and risks plaintext-on-disk windows. The fleet's
// `setup-keycloak-oidc.sh` already solved this with `sops --set` for
// atomic per-key updates; SOPSClient exposes the same primitive.
//
// **Plaintext discipline**: every Decrypt call hands the caller bytes
// that MUST be scrubbed after use (use `secrets.Buffer` from M0-T04).
// The adapter itself never persists plaintext — tempfiles are removed
// with `defer os.Remove`, mode 0600, never inside a git working tree.
type SOPSClient interface {
	// Encrypt encrypts a file in place (overwrites). Used for the
	// first encryption of a newly-scaffolded `secrets.enc.yaml`
	// (M4-T10 + M5-T01 first-time path). For UPDATES to an existing
	// encrypted file, use SetStringData — never re-encrypt the whole
	// file just to change one key.
	//
	// The adapter chooses recipients from the nearest .sops.yaml up
	// the directory tree (standard sops behaviour). Caller doesn't
	// pass recipients explicitly.
	Encrypt(ctx context.Context, path string) error

	// Decrypt returns the entire decrypted file contents in memory.
	// Caller MUST scrub the returned slice after use. Used by:
	//   - M5-T02 unseal (reads OPENBAO_UNSEAL_KEY_{1..3})
	//   - M5-T05 reveal-shares
	//   - Day-2 config editor V3 (when shipped)
	Decrypt(ctx context.Context, path string) ([]byte, error)

	// SetStringData updates a single `stringData.<key>` entry inside an
	// existing SOPS-encrypted YAML Secret manifest, atomically, via
	// `sops --set`. This is the canonical write path for OpenBao share
	// injection and per-key secret edits.
	//
	// Adapter MUST:
	//   - Verify decrypt-round-trip against the supplied value before
	//     committing the file (catches a malformed --set that produces
	//     a file that decrypts to garbage)
	//   - Write to a tempfile + atomic os.Rename to avoid partial
	//     writes on disk-full / process-killed
	//   - Never log the value (M0-T05 redaction layer covers the
	//     defence-in-depth; this is the first line)
	//
	// `path` is the SOPS-encrypted file path; `key` is the YAML path
	// underneath `stringData` (e.g. "OPENBAO_UNSEAL_KEY_1"); `value`
	// is the raw secret bytes (UTF-8 expected — base64 / hex is the
	// caller's encoding decision).
	SetStringData(ctx context.Context, path, key string, value []byte) error

	// Recipients returns the age public keys listed as recipients on
	// the file (read from the encrypted file's metadata, not from
	// .sops.yaml — those can diverge if .sops.yaml changed but the
	// file wasn't re-encrypted). Used by M1 doctor to cross-check
	// the operator's local key against the file's recipients.
	Recipients(path string) ([]string, error)

	// DerivePubKey computes the age public key from a private key
	// file. Used by M1 doctor (cross-check operator's local age key
	// against `.sops.yaml` recipients) and by M4-T09 age-key handling.
	//
	// `keyPath` typically resolves from one of (in order):
	//   - --age-key flag
	//   - fleet-repo /age.key
	//   - $SOPS_AGE_KEY_FILE
	//   - ~/.config/sops/age/keys.txt
	DerivePubKey(keyPath string) (string, error)
}
