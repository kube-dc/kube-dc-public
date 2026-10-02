package clusterinit

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
	"gopkg.in/yaml.v3"
)

const (
	cellKeySecretName = "kube-dc-service-cell-signing-key"
	cellKeyNamespace  = "kube-dc-services"
	cellIDAnnotation  = "services.kube-dc.com/cell-id"
	keyIDAnnotation   = "services.kube-dc.com/key-id"
)

type cellKeyEscrow struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name        string            `yaml:"name"`
		Namespace   string            `yaml:"namespace"`
		Annotations map[string]string `yaml:"annotations"`
	} `yaml:"metadata"`
	Type string            `yaml:"type"`
	Data map[string]string `yaml:"data"`
	SOPS map[string]any    `yaml:"sops,omitempty"`
}

// EscrowManagedServicesCellKey backs up the hub-generated Ed25519 seed in an
// encrypted, unreferenced Fleet file. It refuses a changed cell or key, so a
// resumed install cannot silently replace the recovery identity.
func EscrowManagedServicesCellKey(ctx context.Context, fleetRepo, cluster, expectedCellID string, data map[string][]byte, sops ports.SOPSClient) (string, error) {
	if fleetRepo == "" || !ValidManagedServicesClusterPath(cluster) || expectedCellID == "" || sops == nil {
		return "", fmt.Errorf("managed-services cell escrow needs a Fleet repo, DNS-label cluster, expected cell ID and SOPS client")
	}
	if err := validateCellKeyData(expectedCellID, data); err != nil {
		return "", err
	}
	clusterDir := filepath.Join(fleetRepo, "clusters", cluster)
	if info, err := os.Lstat(clusterDir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("managed-services cell escrow requires a real cluster overlay directory")
	}
	escrowDir := filepath.Join(clusterDir, "escrow")
	if err := os.Mkdir(escrowDir, 0o700); err != nil && !os.IsExist(err) {
		return "", fmt.Errorf("create managed-services escrow directory: %w", err)
	}
	if info, err := os.Lstat(escrowDir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("managed-services escrow directory must not be a symlink")
	}
	path := filepath.Join(escrowDir, "services-cell-signing-key.enc.yaml")
	if err := requireCellEscrowAgeRule(fleetRepo, path); err != nil {
		return "", err
	}
	release, err := lockFile(sopsLockPath(path) + ".identity")
	if err != nil {
		return "", err
	}
	defer release()

	var doc cellKeyEscrow
	doc.APIVersion, doc.Kind, doc.Type = "v1", "Secret", "Opaque"
	doc.Metadata.Name, doc.Metadata.Namespace = cellKeySecretName, cellKeyNamespace
	doc.Metadata.Annotations = map[string]string{cellIDAnnotation: expectedCellID, keyIDAnnotation: string(data["keyID"])}
	doc.Data = make(map[string]string, len(data))
	for _, key := range []string{"seed", "keyID", "public", "cellID"} {
		doc.Data[key] = base64.StdEncoding.EncodeToString(data[key])
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("managed-services cell escrow is not a regular file")
		}
		if err := compareExistingCellEscrow(ctx, path, doc, sops); err != nil {
			return "", err
		}
		return path, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect managed-services cell escrow: %w", err)
	}
	plaintext, err := yaml.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("render managed-services cell escrow: %w", err)
	}
	defer clearSecretBytes(plaintext)
	seed := string(data["seed"])
	if err := sopsEncryptToFile(fleetRepo, path, string(plaintext), []string{seed, base64.StdEncoding.EncodeToString(data["seed"])}); err != nil {
		return "", err
	}
	if err := compareExistingCellEscrow(ctx, path, doc, sops); err != nil {
		if removeErr := os.Remove(path); removeErr != nil {
			return "", fmt.Errorf("encrypted cell escrow did not round-trip (%v), and removal failed: %w", err, removeErr)
		}
		return "", err
	}
	return path, nil
}

// LoadManagedServicesCellKeyEscrow verifies the ciphertext and decrypts the
// signing key for an operator-led rebuild. The caller owns the returned bytes
// and must clear them after applying the Secret.
func LoadManagedServicesCellKeyEscrow(ctx context.Context, fleetRepo, cluster, expectedCellID string, sops ports.SOPSClient) (map[string][]byte, error) {
	if fleetRepo == "" || !ValidManagedServicesClusterPath(cluster) || expectedCellID == "" || sops == nil {
		return nil, fmt.Errorf("managed-services key restore needs a Fleet repo, cluster, cell ID and SOPS client")
	}
	clusterDir := filepath.Join(fleetRepo, "clusters", cluster)
	if info, err := os.Lstat(clusterDir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("managed-services key restore requires a real cluster overlay directory")
	}
	escrowDir := filepath.Join(clusterDir, "escrow")
	if info, err := os.Lstat(escrowDir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("managed-services key restore requires a real escrow directory")
	}
	path := filepath.Join(escrowDir, "services-cell-signing-key.enc.yaml")
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("managed-services key escrow is absent or not a regular file")
	}
	encrypted, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ciphertext cellKeyEscrow
	if err := yaml.Unmarshal(encrypted, &ciphertext); err != nil || len(ciphertext.SOPS) == 0 || !strings.HasPrefix(ciphertext.Data["seed"], "ENC[") {
		return nil, fmt.Errorf("managed-services key escrow is not encrypted")
	}
	if ciphertext.Kind != "Secret" || ciphertext.Metadata.Name != cellKeySecretName ||
		ciphertext.Metadata.Namespace != cellKeyNamespace ||
		ciphertext.Metadata.Annotations[cellIDAnnotation] != expectedCellID {
		return nil, fmt.Errorf("managed-services key escrow belongs to another cell or Secret")
	}
	plain, err := sops.Decrypt(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("decrypt managed-services key escrow: %w", err)
	}
	defer clearSecretBytes(plain)
	var doc cellKeyEscrow
	if err := yaml.Unmarshal(plain, &doc); err != nil || doc.Kind != "Secret" ||
		doc.Metadata.Name != cellKeySecretName || doc.Metadata.Namespace != cellKeyNamespace ||
		doc.Metadata.Annotations[cellIDAnnotation] != expectedCellID {
		return nil, fmt.Errorf("decrypted managed-services key escrow has an unexpected identity")
	}
	data := make(map[string][]byte, 4)
	for _, key := range []string{"seed", "keyID", "public", "cellID"} {
		value, err := base64.StdEncoding.DecodeString(doc.Data[key])
		if err != nil {
			clearCellKeyData(data)
			return nil, fmt.Errorf("managed-services key escrow has invalid %s data", key)
		}
		data[key] = value
	}
	if err := validateCellKeyData(expectedCellID, data); err != nil ||
		doc.Metadata.Annotations[keyIDAnnotation] != string(data["keyID"]) ||
		ciphertext.Metadata.Annotations[keyIDAnnotation] != string(data["keyID"]) {
		clearCellKeyData(data)
		return nil, fmt.Errorf("managed-services key escrow identity or signature is invalid")
	}
	return data, nil
}

func clearCellKeyData(data map[string][]byte) {
	for _, value := range data {
		clearSecretBytes(value)
	}
}

// ValidManagedServicesClusterPath accepts Fleet's region/cluster overlays
// while rejecting traversal, absolute paths and unexpected path separators.
func ValidManagedServicesClusterPath(cluster string) bool {
	if cluster == "" || len(cluster) > 253 {
		return false
	}
	for _, segment := range strings.Split(cluster, "/") {
		if len(segment) == 0 || len(segment) > 63 || !dnsSubdomainLabel.MatchString(segment) {
			return false
		}
	}
	return true
}

func validateCellKeyData(expectedCellID string, data map[string][]byte) error {
	if len(data) != 4 || string(data["cellID"]) != expectedCellID || len(data["keyID"]) == 0 {
		return fmt.Errorf("managed-services cell key is incomplete or belongs to a different cell")
	}
	seed, err := base64.StdEncoding.DecodeString(string(data["seed"]))
	if err != nil || len(seed) != ed25519.SeedSize {
		return fmt.Errorf("managed-services cell key has an invalid private seed")
	}
	defer clearSecretBytes(seed)
	public := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	if string(data["public"]) != base64.StdEncoding.EncodeToString(public) {
		return fmt.Errorf("managed-services cell key public identity does not match its seed")
	}
	digest := sha256.Sum256(public)
	if string(data["keyID"]) != "ed25519-"+hex.EncodeToString(digest[:6]) {
		return fmt.Errorf("managed-services cell key ID does not match its public identity")
	}
	return nil
}

func requireCellEscrowAgeRule(fleetRepo, path string) error {
	config, err := os.ReadFile(filepath.Join(fleetRepo, ".sops.yaml"))
	if err != nil {
		return fmt.Errorf("read Fleet SOPS configuration: %w", err)
	}
	var rules struct {
		CreationRules []struct {
			PathRegex      string `yaml:"path_regex"`
			EncryptedRegex string `yaml:"encrypted_regex"`
			Age            string `yaml:"age"`
		} `yaml:"creation_rules"`
	}
	if err := yaml.Unmarshal(config, &rules); err != nil {
		return fmt.Errorf("parse Fleet SOPS configuration: %w", err)
	}
	relative, err := filepath.Rel(fleetRepo, path)
	if err != nil {
		return err
	}
	for _, rule := range rules.CreationRules {
		pattern, err := regexp.Compile(rule.PathRegex)
		if err != nil {
			return fmt.Errorf("invalid Fleet SOPS path rule: %w", err)
		}
		if !pattern.MatchString(relative) {
			continue
		}
		if strings.TrimSpace(rule.Age) == "" || rule.EncryptedRegex != "^(data|stringData)$" {
			return fmt.Errorf("managed-services escrow needs age recipients and encryption of Secret data")
		}
		return nil
	}
	return fmt.Errorf("no Fleet SOPS age rule covers the managed-services escrow")
}

func compareExistingCellEscrow(ctx context.Context, path string, live cellKeyEscrow, sops ports.SOPSClient) error {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read managed-services cell escrow: %w", err)
	}
	var stored cellKeyEscrow
	if err := yaml.Unmarshal(encoded, &stored); err != nil || len(stored.SOPS) == 0 || !strings.HasPrefix(stored.Data["seed"], "ENC[") {
		return fmt.Errorf("managed-services cell escrow is not a valid encrypted Secret")
	}
	if stored.Kind != "Secret" || stored.Metadata.Name != live.Metadata.Name || stored.Metadata.Namespace != live.Metadata.Namespace ||
		stored.Metadata.Annotations[cellIDAnnotation] != live.Metadata.Annotations[cellIDAnnotation] ||
		stored.Metadata.Annotations[keyIDAnnotation] != live.Metadata.Annotations[keyIDAnnotation] {
		return fmt.Errorf("managed-services cell escrow identity changed; refusing to overwrite it")
	}
	plain, err := sops.Decrypt(ctx, path)
	if err != nil {
		return fmt.Errorf("decrypt managed-services cell escrow: %w", err)
	}
	defer clearSecretBytes(plain)
	var decrypted cellKeyEscrow
	if err := yaml.Unmarshal(plain, &decrypted); err != nil || !equalCellEscrowData(decrypted.Data, live.Data) {
		return fmt.Errorf("managed-services cell escrow differs from the live signing key")
	}
	return nil
}

func equalCellEscrowData(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range b {
		if !bytes.Equal([]byte(a[key]), []byte(value)) {
			return false
		}
	}
	return true
}

func clearSecretBytes(data []byte) {
	for i := range data {
		data[i] = 0
	}
}
