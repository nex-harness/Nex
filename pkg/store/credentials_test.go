package store

import (
	"context"
	"strings"
	"testing"
)

func TestVaultEncryptionAndCredentialLifecycle(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	// 1. Encrypt & decrypt roundtrip
	ct, err := st.EncryptSecret("ghp_1234567890abcdef")
	if err != nil {
		t.Fatalf("EncryptSecret: %v", err)
	}
	if !strings.HasPrefix(ct, "v1:") || strings.Contains(ct, "ghp_1234567890abcdef") {
		t.Fatalf("unexpected ciphertext: %q", ct)
	}
	pt, err := st.DecryptSecret(ct)
	if err != nil || pt != "ghp_1234567890abcdef" {
		t.Fatalf("DecryptSecret got %q, err=%v", pt, err)
	}
	if hint := MaskSecretHint("ghp_1234567890abcdef"); hint != "ghp_••••cdef" {
		t.Fatalf("unexpected hint: %q", hint)
	}
	if hint := MaskSecretHint("short"); hint != "••••••••" {
		t.Fatalf("unexpected short hint: %q", hint)
	}

	// 2. Upsert credential group with runner overrides
	saved, err := st.UpsertCredential(ctx, CredentialRecord{
		Name:        "GitHub CLI",
		Preset:      "github",
		StorageMode: "server_synced",
		UsagePolicy: "all",
		Scope:       "global",
		Entries: []CredentialEntry{
			{Key: "GH_TOKEN", Value: "ghp_global_secret_token_9999", Note: "dev"},
		},
		RunnerOverrides: map[string][]CredentialEntry{
			"run_linux": {{Key: "GH_TOKEN", Value: "ghp_linux_override_token_8888"}},
		},
		ProbeCommand: "gh auth status",
		Enabled:      true,
	})
	if err != nil {
		t.Fatalf("UpsertCredential: %v", err)
	}

	// Sanitized view must never leak plaintext or ciphertext
	san := saved.Sanitized()
	if san.Entries[0].Value != "" || san.Entries[0].EncryptedVal != "" || !san.Entries[0].HasValue {
		t.Fatalf("sanitized entry leaked or missing has_value: %+v", san.Entries[0])
	}

	// 3. Partial update omitting Value preserves existing encrypted secret
	saved2, err := st.UpsertCredential(ctx, CredentialRecord{
		ID:          saved.ID,
		Name:        "GitHub CLI",
		StorageMode: "server_synced",
		UsagePolicy: "all",
		Scope:       "global",
		Entries: []CredentialEntry{
			{Key: "GH_TOKEN", Note: "updated note"},
		},
		RunnerOverrides: map[string][]CredentialEntry{
			"run_linux": {{Key: "GH_TOKEN"}},
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("partial UpsertCredential: %v", err)
	}

	// 4. DecryptCredentialForRunner isolates overrides per runner
	forMac := st.DecryptCredentialForRunner(saved2, "run_mac")
	if forMac.Entries[0].Value != "ghp_global_secret_token_9999" {
		t.Fatalf("expected preserved global secret, got %q", forMac.Entries[0].Value)
	}
	if len(forMac.RunnerOverrides) != 0 {
		t.Fatalf("run_mac must not receive run_linux overrides")
	}
	forLinux := st.DecryptCredentialForRunner(saved2, "run_linux")
	if len(forLinux.RunnerOverrides["run_linux"]) != 1 || forLinux.RunnerOverrides["run_linux"][0].Value != "ghp_linux_override_token_8888" {
		t.Fatalf("expected run_linux override, got %+v", forLinux.RunnerOverrides)
	}

	// 5. ResolveEffectiveEnv precedence & chat vs cron policy
	_, err = st.UpsertCredential(ctx, CredentialRecord{
		Name:        "Chat Only AWS",
		StorageMode: "server_synced",
		UsagePolicy: "chat_only",
		Scope:       "global",
		Entries:     []CredentialEntry{{Key: "AWS_SECRET", Value: "aws_chat_only_secret"}},
		Enabled:     true,
	})
	if err != nil {
		t.Fatalf("upsert chat_only: %v", err)
	}
	cronOnly, err := st.UpsertCredential(ctx, CredentialRecord{
		Name:        "Nightly Release",
		StorageMode: "server_synced",
		UsagePolicy: "cron_explicit",
		Scope:       "global",
		Entries:     []CredentialEntry{{Key: "RELEASE_TOKEN", Value: "rel_cron_secret_1234"}},
		Enabled:     true,
	})
	if err != nil {
		t.Fatalf("upsert cron_explicit: %v", err)
	}

	chatEnv, _ := st.ResolveEffectiveEnv(ctx, "run_linux", "/tmp/ws", "chat", nil)
	if chatEnv["GH_TOKEN"] != "ghp_linux_override_token_8888" {
		t.Fatalf("expected runner override in chatEnv, got %q", chatEnv["GH_TOKEN"])
	}
	if chatEnv["AWS_SECRET"] != "aws_chat_only_secret" {
		t.Fatalf("expected chat_only secret in chatEnv")
	}
	if _, leaked := chatEnv["RELEASE_TOKEN"]; leaked {
		t.Fatalf("cron_explicit secret must not appear in chatEnv")
	}

	cronEnvNoGrant, _ := st.ResolveEffectiveEnv(ctx, "run_linux", "/tmp/ws", "cron", nil)
	if _, leaked := cronEnvNoGrant["AWS_SECRET"]; leaked {
		t.Fatalf("chat_only secret must not appear in cronEnv")
	}
	if _, leaked := cronEnvNoGrant["RELEASE_TOKEN"]; leaked {
		t.Fatalf("ungranted cron_explicit secret must not appear in cronEnv")
	}

	cronEnvGranted, _ := st.ResolveEffectiveEnv(ctx, "run_linux", "/tmp/ws", "cron", []string{cronOnly.ID})
	if cronEnvGranted["RELEASE_TOKEN"] != "rel_cron_secret_1234" {
		t.Fatalf("granted cron_explicit secret missing")
	}

	// 6. Runner-local encrypted value overrides
	if _, err := st.SetLocalCredentialValue(ctx, saved.ID, "LOCAL_ONLY_KEY", "local_secret_value_777"); err != nil {
		t.Fatalf("SetLocalCredentialValue: %v", err)
	}
	envWithLocal, _ := st.ResolveEffectiveEnv(ctx, "run_linux", "/tmp/ws", "chat", nil)
	if envWithLocal["LOCAL_ONLY_KEY"] != "local_secret_value_777" {
		t.Fatalf("expected runner-local secret, got %q", envWithLocal["LOCAL_ONLY_KEY"])
	}
}

func TestParseDotEnv(t *testing.T) {
	raw := `
# comment
export GH_TOKEN="ghp_abc123456789"
AWS_REGION=us-east-1
SINGLE='hello world'
INVALID-KEY=nope
`
	entries := ParseDotEnv(raw)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d: %+v", len(entries), entries)
	}
	if entries[0].Key != "GH_TOKEN" || entries[0].Value != "ghp_abc123456789" {
		t.Fatalf("unexpected entry 0: %+v", entries[0])
	}
	if entries[2].Key != "SINGLE" || entries[2].Value != "hello world" {
		t.Fatalf("unexpected entry 2: %+v", entries[2])
	}
}
