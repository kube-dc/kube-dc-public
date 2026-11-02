package openbao

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"time"
)

// ErrSharesNotCommitted means OpenBao is initialized but the fleet holds no
// shares for it.
var ErrSharesNotCommitted = errors.New("openbao: shares not committed")

// ResumeOptions configures ResumeCommitted.
type ResumeOptions struct {
	Unseal UnsealOptions
	// ControllerAuthInstalled skips the controller-auth ceremony when its
	// marker is already on svc/openbao.
	ControllerAuthInstalled bool
}

// ResumeCommitted finishes an init whose shares are already encrypted and
// committed but whose Phase C stopped (for example a follower that could not
// join yet): it unseals every member from the committed shares, installs
// controller auth with a single-use root token from a generate-root ceremony
// (revoked before returning), and stamps the bootstrap-finalized marker.
// Every step is idempotent, so a further interruption is resumed the same way.
func ResumeCommitted(ctx context.Context, opts ResumeOptions) error {
	u := opts.Unseal
	if err := validateUnsealOptions(u); err != nil {
		return err
	}
	if u.K8s == nil {
		return fmt.Errorf("%w: K8s", ErrUnsealMissingDependency)
	}
	out := u.Out
	if out == nil {
		out = io.Discard
	}
	if u.FollowerWait == 0 {
		u.FollowerWait = initFollowerWait
	}
	fmt.Fprintln(out, "[openbao] initialized with committed shares — resuming: unseal, controller auth, finalize")
	if err := Unseal(ctx, u); err != nil {
		if errors.Is(err, ErrUnsealMissingShares) {
			return fmt.Errorf("%w: OpenBao is initialized but %s/clusters/%s/secrets.enc.yaml lacks its shares; do not run init again. "+
				"The shares were captured to the write-ahead copy ~/.kube-dc/openbao-emergency-shares-%s.yaml: encrypt them into secrets.enc.yaml, commit, and re-run",
				ErrSharesNotCommitted, u.FleetRepo, u.ClusterName, url.PathEscape(u.ClusterName))
		}
		return err
	}

	// The generate-root ceremony runs even when controller auth is already
	// installed: it proves the committed shares belong to this vault before
	// the finalized marker vouches for them.
	{
		secretsPath := filepath.Join(u.FleetRepo, "clusters", u.ClusterName, "secrets.enc.yaml")
		decrypted, err := u.SOPS.Decrypt(ctx, secretsPath)
		if err != nil {
			return fmt.Errorf("openbao: decrypt %s: %w", secretsPath, err)
		}
		shares, err := extractThresholdShares(decrypted)
		for i := range decrypted {
			decrypted[i] = 0
		}
		if err != nil {
			return err
		}
		fmt.Fprintln(out, "[openbao] running generate-root ceremony (single-use token)")
		token, err := u.OpenBao.GenerateRoot(ctx, shares)
		for _, s := range shares {
			for i := range s {
				s[i] = 0
			}
		}
		if err != nil {
			return fmt.Errorf("openbao: generate-root: %w", err)
		}
		defer func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if rerr := u.OpenBao.RevokeSelf(cleanupCtx, token); rerr != nil {
				fmt.Fprintf(out, "[openbao] WARNING root-token revoke failed: %v — find it with `bao list auth/token/accessors` and revoke it with `bao token revoke -accessor <id>`\n", rerr)
			}
			for i := range token {
				token[i] = 0
			}
		}()
		if !opts.ControllerAuthInstalled {
			if err := SetupControllerAuth(ctx, SetupControllerAuthOptions{
				Token: token, RefreshMode: RefreshFull, OpenBao: u.OpenBao, K8s: u.K8s, Out: out,
			}); err != nil {
				return fmt.Errorf("openbao: controller auth: %w", err)
			}
		}
	}

	stamp := time.Now().UTC().Format(time.RFC3339)
	fmt.Fprintf(out, "[openbao] stamping %s=%s on svc/openbao\n", AnnotationBootstrapFinalized, stamp)
	if err := u.OpenBao.SetAnnotations(ctx, "openbao", map[string]string{AnnotationBootstrapFinalized: stamp}); err != nil {
		return fmt.Errorf("annotate svc/openbao: %w", err)
	}
	fmt.Fprintln(out, "[openbao] resume complete — all members unsealed, controller auth installed, finalized")
	return nil
}
