package clusterinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const repairPubkey = "age10mskwx065akee5mw4txeqtnn90t724phdzx9k4jxnrgcp9cces6sqfkwvu"

func TestEnrollEmptySOPSConfig(t *testing.T) {
	const head = "creation_rules:\n  - path_regex: '\\.enc\\.yaml$'\n    encrypted_regex: '^(data|stringData)$'\n"
	for _, empty := range []string{"    age: ''\n", "    age: \"\"\n", "    age:\n", "    age:   \n"} {
		got, ok := EnrollEmptySOPSConfig([]byte(head+empty), repairPubkey)
		if !ok {
			t.Fatalf("%q was not repaired", empty)
		}
		if want := head + "    age: '" + repairPubkey + "'\n"; string(got) != want {
			t.Fatalf("repair of %q:\n%s\nwant:\n%s", empty, got, want)
		}
		if r := ParseSOPSConfigRecipients(got); len(r) != 1 || r[0] != repairPubkey {
			t.Fatalf("repaired file parses to %v", r)
		}
	}
	for name, body := range map[string]string{
		"has a recipient": head + "    age: '" + repairPubkey + "'\n",
		"two empty rules": head + "    age: ''\n  - path_regex: x\n    age: ''\n",
		"no age line":     head,
	} {
		if _, ok := EnrollEmptySOPSConfig([]byte(body), repairPubkey); ok {
			t.Errorf("%s: repaired, want refused", name)
		}
	}
	if _, ok := EnrollEmptySOPSConfig([]byte(head+"    age: ''\n"), "age1short"); ok {
		t.Errorf("an invalid public key was enrolled")
	}
}

func TestEncryptedFilesUnder(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("clusters/a/secrets.enc.yaml", "data:\n  x: ENC[AES256_GCM,data:x]\nsops:\n  age: []\n")
	write("clusters/a/plain.enc.yaml", "data:\n  x: y\n")
	write("clusters/a/notes.yaml", "sops:\n")
	write(".git/objects/x.enc.yaml", "sops:\n")
	got, err := EncryptedFilesUnder(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != filepath.Join("clusters", "a", "secrets.enc.yaml") {
		t.Fatalf("EncryptedFilesUnder = %v", got)
	}
}

func TestSOPSDefaultKeyFiles(t *testing.T) {
	home := "/home/op"
	linux := filepath.Join(home, ".config", "sops", "age", "keys.txt")
	if got := SOPSDefaultKeyFiles(home, "linux", ""); len(got) != 1 || got[0] != linux {
		t.Errorf("linux: %v", got)
	}
	mac := filepath.Join(home, "Library", "Application Support", "sops", "age", "keys.txt")
	if got := SOPSDefaultKeyFiles(home, "darwin", ""); len(got) != 2 || got[0] != linux || got[1] != mac {
		t.Errorf("darwin: %v", got)
	}
	if got := SOPSDefaultKeyFiles(home, "darwin", filepath.Join(home, ".config")); len(got) != 1 || got[0] != linux {
		t.Errorf("darwin with XDG_CONFIG_HOME=~/.config: %v", got)
	}
	if got := SOPSDefaultKeyFiles(home, "darwin", "/xdg"); len(got) != 2 || got[1] != "/xdg/sops/age/keys.txt" {
		t.Errorf("darwin with XDG_CONFIG_HOME=/xdg: %v", got)
	}
}

func TestAppendAgeKey(t *testing.T) {
	dir := t.TempDir()
	keys := filepath.Join(dir, "sops", "age", "keys.txt")
	key := []byte("# public key: " + repairPubkey + "\nAGE-SECRET-KEY-1TEST\n")
	added, err := AppendAgeKey(keys, key, repairPubkey, "fleet key")
	if err != nil || !added {
		t.Fatalf("first append: added=%v err=%v", added, err)
	}
	if added, err := AppendAgeKey(keys, key, repairPubkey, "fleet key"); err != nil || added {
		t.Fatalf("second append: added=%v err=%v", added, err)
	}
	info, err := os.Stat(keys)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("keys file mode: %v %v", info.Mode(), err)
	}
	if info, err := os.Stat(filepath.Dir(keys)); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("keys directory mode: %v %v", info.Mode(), err)
	}

	// An existing file without a trailing newline keeps its key intact.
	other := filepath.Join(dir, "other.txt")
	if err := os.WriteFile(other, []byte("AGE-SECRET-KEY-1OTHER"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := AppendAgeKey(other, key, repairPubkey, "fleet key"); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(other)
	if !strings.HasPrefix(string(body), "AGE-SECRET-KEY-1OTHER\n\n# fleet key\n") || !strings.HasSuffix(string(body), "AGE-SECRET-KEY-1TEST\n") {
		t.Fatalf("appended file:\n%s", body)
	}
}
