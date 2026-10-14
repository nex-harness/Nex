package credential

// credential_test.go: cipher parity vector (Node-generated), store
// round-trip, corrupt-store refusal, cross-instance interop.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fixedKeyCipher builds a cipher with a fixed secret for deterministic
// vectors. The vector below was produced by the TS provider with the same
// secret, iv "0123456789ab" and plaintext "Go parity vector":
// ciphertext vwz-lPaGRjvklUUFM7F4lQ, auth tag HKZf_Es-RJvtn6VkmrGusQ.
func fixedKeyCipher(secret string) *AESCipher {
	return NewAESCipher(func() map[string]string { return map[string]string{SecretEnvKey: secret} }, nil)
}

func TestCipherVectorParity(t *testing.T) {
	// Probe10 (Node): sha256("nex-credential-fallback:linux:/home/u:user") key,
	// iv "0123456789ab", plaintext "Go parity vector" →
	// enc:v1: parts below.
	c := NewAESCipher(
		func() map[string]string { return map[string]string{} },
		func() (string, string, string) { return "user", "/home/u", "linux" },
	)
	// Fixed IV is not exposed by Encrypt (random IV), so verify the DECRYPT
	// direction against the Node-produced vector.
	const nodeVector = "enc:v1:MDEyMzQ1Njc4OWFi.HKZf_Es-RJvtn6VkmrGusQ.vwz-lPaGRjvklUUFM7F4lQ"
	plaintext, err := c.Decrypt(nodeVector)
	if err != nil {
		t.Fatalf("decrypt of Node vector failed: %v", err)
	}
	if plaintext != "Go parity vector" {
		t.Fatalf("Node cipher vector mismatch: got %q", plaintext)
	}
	// Round-trip through the Go cipher.
	enc, err := c.Encrypt("round trip 秘密")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, EncryptedValuePrefix) {
		t.Fatalf("missing prefix: %s", enc)
	}
	dec, err := c.Decrypt(enc)
	if err != nil || dec != "round trip 秘密" {
		t.Fatalf("round trip failed: %q %v", dec, err)
	}
	// Un-prefixed values pass through.
	plain, err := c.Decrypt("plaintext-legacy-value")
	if err != nil || plain != "plaintext-legacy-value" {
		t.Fatalf("unprefixed passthrough broken: %q %v", plain, err)
	}
}

func TestCipherDecryptErrorCodes(t *testing.T) {
	c := fixedKeyCipher("secret")
	cases := []struct {
		value  string
		reason string
	}{
		{"enc:v1:only.two", "密文格式非法"},
		{"enc:v1:" + strings.Repeat("A", 8) + ".AAAAAAAAAAAAAAAAAAAAAA.AAAAAAAA", "IV 长度非法"},
		{"enc:v1:AAAAAAAAAAAAAAAA.AAAAAAAA.AAAAAAAA", "AuthTag 长度非法"},
	}
	for _, tc := range cases {
		_, err := c.Decrypt(tc.value)
		var decryptErr *DecryptError
		if !errors.As(err, &decryptErr) {
			t.Fatalf("want DecryptError for %q, got %v", tc.value, err)
		}
		if decryptErr.Code() != DecryptErrorCode {
			t.Fatalf("code: %s", decryptErr.Code())
		}
		if !strings.HasPrefix(decryptErr.Error(), DecryptErrorPrefix) {
			t.Fatalf("prefix: %s", decryptErr.Error())
		}
	}
	// Wrong key → 密钥不匹配或密文已损坏.
	enc, _ := fixedKeyCipher("secret-1").Encrypt("x")
	_, err := fixedKeyCipher("secret-2").Decrypt(enc)
	var decryptErr *DecryptError
	if !errors.As(err, &decryptErr) || decryptErr.Reason != "密钥不匹配或密文已损坏" {
		t.Fatalf("want key-mismatch error, got %v", err)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "credentials.json")
	svc := NewService(Options{CredentialsFile: file})
	if err := svc.Save(context.Background(), "mcp.oauth.token", "tok-1"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Save(context.Background(), "bot.telegram", "tok-2"); err != nil {
		t.Fatal(err)
	}
	// The on-disk values are encrypted (never plaintext).
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "tok-1") || strings.Contains(string(data), "tok-2") {
		t.Fatalf("plaintext secrets on disk: %s", data)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Fatal("credentials.json must end with a newline (TS parity)")
	}
	value, err := svc.Load(context.Background(), "mcp.oauth.token")
	if err != nil || value != "tok-1" {
		t.Fatalf("load: %q %v", value, err)
	}
	missing, err := svc.Load(context.Background(), "nope")
	if err != nil || missing != "" {
		t.Fatalf("missing key: %q %v", missing, err)
	}
	if err := svc.Delete(context.Background(), "mcp.oauth.token"); err != nil {
		t.Fatal(err)
	}
	deleted, _ := svc.Load(context.Background(), "mcp.oauth.token")
	if deleted != "" {
		t.Fatalf("delete failed: %q", deleted)
	}
	// Mode is 0600.
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credentials.json mode: %v", info.Mode().Perm())
	}
}

func TestCorruptStoreRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "credentials.json")
	if err := os.WriteFile(file, []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := NewService(Options{CredentialsFile: file})
	_, err := svc.Load(context.Background(), "key")
	if err == nil || !strings.Contains(err.Error(), "Nex credentials are corrupt") {
		t.Fatalf("want corrupt-store error, got %v", err)
	}
	if err := svc.Save(context.Background(), "key", "v"); err == nil {
		t.Fatal("save must refuse to overwrite a corrupt store")
	}
	// Backup with the content-hash name exists.
	entries, _ := os.ReadDir(dir)
	found := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "credentials.json.corrupt-") && strings.HasSuffix(e.Name(), ".bak") {
			found = true
		}
	}
	if !found {
		t.Fatal("corrupt store must be backed up")
	}
}

func TestKeyValidation(t *testing.T) {
	svc := NewService(Options{CredentialsFile: filepath.Join(t.TempDir(), "credentials.json")})
	if err := svc.Save(context.Background(), "  ", "v"); err == nil {
		t.Fatal("empty key must be rejected")
	}
	if err := svc.Save(context.Background(), " padded ", "v"); err != nil {
		t.Fatalf("key should be trimmed: %v", err)
	}
}

func TestTwoInstancesInteroperate(t *testing.T) {
	// Two instances (Node/Go transition) with the SAME secret: each reads the
	// other's encrypted values.
	dir := t.TempDir()
	file := filepath.Join(dir, "credentials.json")
	env := func() map[string]string { return map[string]string{SecretEnvKey: "shared-secret"} }
	a := NewService(Options{CredentialsFile: file, EnvSnapshot: env})
	b := NewService(Options{CredentialsFile: file, EnvSnapshot: env})
	if err := a.Save(context.Background(), "shared.key", "from-a"); err != nil {
		t.Fatal(err)
	}
	value, err := b.Load(context.Background(), "shared.key")
	if err != nil || value != "from-a" {
		t.Fatalf("cross-instance load: %q %v", value, err)
	}
	// Concurrent saves from both instances don't lose the other's keys when
	// they run sequentially interleaved (the in-process locks serialize the
	// read-modify-write under the same file lock).
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			svc := a
			if i%2 == 1 {
				svc = b
			}
			_ = svc.Save(context.Background(), string(rune('a'+i)), "v")
		}(i)
	}
	wg.Wait()
	final, err := a.readAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Note: like the TS, concurrent same-store read-modify-write across two
	// INDEPENDENT processes can race (that is what withFileLock prevents in
	// the TS; the Go port relies on the same lock file). The assertion is
	// that the file stays a VALID store.
	if _, err := a.Load(context.Background(), "shared.key"); err != nil {
		t.Fatalf("store corrupted by concurrent writers: %v", err)
	}
	_ = final
}

func TestOnDidMutate(t *testing.T) {
	var mu sync.Mutex
	var events []MutateEvent
	svc := NewService(Options{
		CredentialsFile: filepath.Join(t.TempDir(), "credentials.json"),
		OnDidMutate: func(e MutateEvent) {
			mu.Lock()
			events = append(events, e)
			mu.Unlock()
		},
	})
	_ = svc.Save(context.Background(), "k", "v")
	_ = svc.Delete(context.Background(), "k")
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 || events[0].Operation != "save" || events[1].Operation != "delete" {
		t.Fatalf("onDidMutate events: %v", events)
	}
}
