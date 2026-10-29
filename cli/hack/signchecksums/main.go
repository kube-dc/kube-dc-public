// Command signchecksums signs a release's checksums.txt for the CLI's
// self-update (internal/selfupdate verifies it with selfupdate.PublicKey).
//
//	signchecksums -keygen PRIVATE_KEY_FILE   write a new key (mode 0600), print its public half
//	signchecksums ARTIFACT SIGNATURE         sign ARTIFACT with $KUBE_DC_UPDATE_SIGNING_KEY
//
// The key is the base64 of an ed25519 seed (32 bytes). GoReleaser calls the
// second form from the release workflow (cli/.goreleaser.yaml, signs:).
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "signchecksums:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 2 && args[0] == "-keygen" {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		seed := base64.StdEncoding.EncodeToString(priv.Seed())
		if err := os.WriteFile(args[1], []byte(seed+"\n"), 0o600); err != nil {
			return err
		}
		fmt.Println(base64.StdEncoding.EncodeToString(pub))
		return nil
	}
	if len(args) != 2 {
		return errors.New("usage: signchecksums -keygen KEYFILE | signchecksums ARTIFACT SIGNATURE")
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("KUBE_DC_UPDATE_SIGNING_KEY")))
	if err != nil || len(seed) != ed25519.SeedSize {
		return errors.New("KUBE_DC_UPDATE_SIGNING_KEY must be the base64 of a 32-byte ed25519 seed")
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	sig := ed25519.Sign(ed25519.NewKeyFromSeed(seed), data)
	return os.WriteFile(args[1], []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0o644)
}
