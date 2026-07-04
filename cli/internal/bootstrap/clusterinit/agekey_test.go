package clusterinit

import (
	"errors"
	"strings"
	"testing"
)

func TestParseSOPSConfigRecipients_LiveFleetShape(t *testing.T) {
	// Canonical shape from the live ~/projects/kube-dc-fleet/.sops.yaml:
	// `age:` value is a comma-separated string of pubkeys.
	body := []byte(`# SOPS configuration for kube-dc-fleet
creation_rules:
  - path_regex: '\.enc\.yaml$'
    age: 'age10mskwx065akee5mw4txeqtnn90t724phdzx9k4jxnrgcp9cces6sqfkwvu,age1fugkeh0yhf56d6t2qm8gwqdl3kx7963wv2qq5jax2kewldsfeqgq6p67pm,age16nk3t6chcrjntd76s3an32hx3p2y5cup7vnkywny0uy9gn0tkcuqxzav8s'
`)
	got := ParseSOPSConfigRecipients(body)
	want := []string{
		"age10mskwx065akee5mw4txeqtnn90t724phdzx9k4jxnrgcp9cces6sqfkwvu",
		"age1fugkeh0yhf56d6t2qm8gwqdl3kx7963wv2qq5jax2kewldsfeqgq6p67pm",
		"age16nk3t6chcrjntd76s3an32hx3p2y5cup7vnkywny0uy9gn0tkcuqxzav8s",
	}
	if len(got) != len(want) {
		t.Fatalf("recipient count: got %d, want %d (%v)", len(got), len(want), got)
	}
	for i, r := range want {
		if got[i] != r {
			t.Errorf("recipient %d: got %s, want %s", i, got[i], r)
		}
	}
}

func TestParseSOPSConfigRecipients_YAMLListShape(t *testing.T) {
	// Some installs prefer the YAML-list shape rather than the
	// inline-comma. The regex catches both.
	body := []byte(`creation_rules:
  - path_regex: '\.enc\.yaml$'
    age:
      - 'age10mskwx065akee5mw4txeqtnn90t724phdzx9k4jxnrgcp9cces6sqfkwvu'
      - 'age1fugkeh0yhf56d6t2qm8gwqdl3kx7963wv2qq5jax2kewldsfeqgq6p67pm'
`)
	got := ParseSOPSConfigRecipients(body)
	if len(got) != 2 {
		t.Fatalf("expected 2 recipients in list shape, got %d: %v", len(got), got)
	}
}

func TestParseSOPSConfigRecipients_Dedupes(t *testing.T) {
	body := []byte(`age: 'age10mskwx065akee5mw4txeqtnn90t724phdzx9k4jxnrgcp9cces6sqfkwvu,age10mskwx065akee5mw4txeqtnn90t724phdzx9k4jxnrgcp9cces6sqfkwvu'`)
	got := ParseSOPSConfigRecipients(body)
	if len(got) != 1 {
		t.Errorf("expected dedup → 1, got %d: %v", len(got), got)
	}
}

func TestParseSOPSConfigRecipients_NoRecipients(t *testing.T) {
	body := []byte(`# no age block here
creation_rules:
  - path_regex: 'foo'
    pgp: 'fake-fingerprint'
`)
	got := ParseSOPSConfigRecipients(body)
	if len(got) != 0 {
		t.Errorf("expected 0 recipients, got %v", got)
	}
}

func TestParseSOPSConfigRecipients_Empty(t *testing.T) {
	if got := ParseSOPSConfigRecipients(nil); got != nil {
		t.Errorf("nil body should produce nil/empty, got %v", got)
	}
	if got := ParseSOPSConfigRecipients([]byte("")); got != nil {
		t.Errorf("empty body should produce nil/empty, got %v", got)
	}
}

func TestCheckAgeKeyEnrollment_Enrolled(t *testing.T) {
	pubkey := "age10mskwx065akee5mw4txeqtnn90t724phdzx9k4jxnrgcp9cces6sqfkwvu"
	recipients := []string{
		"age1fugkeh0yhf56d6t2qm8gwqdl3kx7963wv2qq5jax2kewldsfeqgq6p67pm",
		pubkey,
		"age16nk3t6chcrjntd76s3an32hx3p2y5cup7vnkywny0uy9gn0tkcuqxzav8s",
	}
	if err := CheckAgeKeyEnrollment(pubkey, "operator", recipients); err != nil {
		t.Fatalf("enrolled pubkey should pass, got %v", err)
	}
}

func TestCheckAgeKeyEnrollment_NotEnrolled(t *testing.T) {
	pubkey := "age1notenrolledxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
	recipients := []string{
		"age10mskwx065akee5mw4txeqtnn90t724phdzx9k4jxnrgcp9cces6sqfkwvu",
		"age1fugkeh0yhf56d6t2qm8gwqdl3kx7963wv2qq5jax2kewldsfeqgq6p67pm",
	}
	err := CheckAgeKeyEnrollment(pubkey, "vtsap", recipients)
	if !errors.Is(err, ErrAgeKeyNotEnrolled) {
		t.Fatalf("expected ErrAgeKeyNotEnrolled, got %v", err)
	}
	// The error must include the operator-actionable remediation:
	// the pubkey + the add-engineer.sh command with the operator's
	// name pre-filled.
	for _, want := range []string{
		"age1notenrolled",
		"bootstrap/add-engineer.sh vtsap age1notenrolled",
		"--reencrypt",
		"git push",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q\nFULL:\n%s", want, err.Error())
		}
	}
}

func TestCheckAgeKeyEnrollment_EmptyOperatorName(t *testing.T) {
	// When no operator name is supplied (programmatic call), the
	// printed command uses `<your-name>` so the result is still
	// readable.
	pubkey := "age1xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
	recipients := []string{"age10mskwx065akee5mw4txeqtnn90t724phdzx9k4jxnrgcp9cces6sqfkwvu"}
	err := CheckAgeKeyEnrollment(pubkey, "", recipients)
	if !errors.Is(err, ErrAgeKeyNotEnrolled) {
		t.Fatalf("expected ErrAgeKeyNotEnrolled, got %v", err)
	}
	if !strings.Contains(err.Error(), "<your-name>") {
		t.Errorf("empty operatorName should leave the <your-name> placeholder; got %s", err.Error())
	}
}

func TestCheckAgeKeyEnrollment_EmptyPubkey(t *testing.T) {
	// Defensive: empty pubkey means DerivePubKey failed earlier.
	// Don't silently say "not enrolled" — surface the real cause.
	err := CheckAgeKeyEnrollment("", "operator", []string{"age10mskwx065akee5mw4txeqtnn90t724phdzx9k4jxnrgcp9cces6sqfkwvu"})
	if !errors.Is(err, ErrAgeKeyNotEnrolled) {
		t.Fatalf("expected ErrAgeKeyNotEnrolled, got %v", err)
	}
	if !strings.Contains(err.Error(), "empty pubkey") {
		t.Errorf("error should explain empty-pubkey cause; got %s", err.Error())
	}
}

func TestCheckAgeKeyEnrollment_RecipientsListedSorted(t *testing.T) {
	// The "current recipients:" line should be deterministically
	// sorted so the error message doesn't drift across runs (Go
	// slice iteration is stable but tests benefit from explicit
	// ordering).
	pubkey := "age1notenrolledxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
	recipients := []string{
		"age1zzzlast",
		"age1aaafirst",
	}
	err := CheckAgeKeyEnrollment(pubkey, "operator", recipients)
	if err == nil {
		t.Fatal("expected error")
	}
	got := err.Error()
	i := strings.Index(got, "age1aaafirst")
	j := strings.Index(got, "age1zzzlast")
	if i < 0 || j < 0 || i > j {
		t.Errorf("recipients not sorted alphabetically: %s", got)
	}
}
