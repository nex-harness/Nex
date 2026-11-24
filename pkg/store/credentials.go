package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

var envKeyRegex = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// IsValidEnvKey checks whether k is a valid POSIX environment variable name.
func IsValidEnvKey(k string) bool {
	return envKeyRegex.MatchString(k)
}

// CredentialEntry represents a single environment variable inside a credential group.
type CredentialEntry struct {
	Key          string `json:"key"`
	Value        string `json:"value,omitempty"`         // Transient plaintext on write / runner sync only
	EncryptedVal string `json:"encrypted_val,omitempty"` // AES-256-GCM ciphertext stored in DB
	ValueHint    string `json:"value_hint,omitempty"`    // Masked preview (e.g. ghp_••••a1b2)
	HasValue     bool   `json:"has_value,omitempty"`     // True when a non-empty value is stored
	ClearValue   bool   `json:"clear_value,omitempty"`   // Explicit clear request on update
	Note         string `json:"note,omitempty"`
}

// CredentialRecord represents a named group of CLI/environment credentials with multi-runner scoping.
type CredentialRecord struct {
	ID              string                       `json:"id"`
	Name            string                       `json:"name"`
	Description     string                       `json:"description,omitempty"`
	Preset          string                       `json:"preset,omitempty"`
	StorageMode     string                       `json:"storage_mode"` // "server_synced" | "runner_local"
	UsagePolicy     string                       `json:"usage_policy"` // "all" | "chat_only" | "cron_explicit"
	Scope           string                       `json:"scope"`        // "global" | "workspace" | "runner"
	WorkspacePath   string                       `json:"workspace_path,omitempty"`
	TargetRunners   []string                     `json:"target_runners,omitempty"`
	Entries         []CredentialEntry            `json:"entries"`
	RunnerOverrides map[string][]CredentialEntry `json:"runner_overrides,omitempty"`
	ProbeCommand    string                       `json:"probe_command,omitempty"`
	Enabled         bool                         `json:"enabled"`
	UpdatedAt       int64                        `json:"updated_at"`
}

// Sanitized returns a write-only safe copy with all plaintext values and ciphertexts stripped.
func (r CredentialRecord) Sanitized() CredentialRecord {
	cp := r
	cp.Entries = make([]CredentialEntry, 0, len(r.Entries))
	for _, e := range r.Entries {
		cp.Entries = append(cp.Entries, CredentialEntry{
			Key:       e.Key,
			ValueHint: e.ValueHint,
			HasValue:  e.HasValue || e.EncryptedVal != "" || e.Value != "",
			Note:      e.Note,
		})
	}
	if len(r.RunnerOverrides) > 0 {
		cp.RunnerOverrides = make(map[string][]CredentialEntry, len(r.RunnerOverrides))
		for rid, list := range r.RunnerOverrides {
			sList := make([]CredentialEntry, 0, len(list))
			for _, e := range list {
				sList = append(sList, CredentialEntry{
					Key:       e.Key,
					ValueHint: e.ValueHint,
					HasValue:  e.HasValue || e.EncryptedVal != "" || e.Value != "",
					Note:      e.Note,
				})
			}
			cp.RunnerOverrides[rid] = sList
		}
	}
	return cp
}

func normalizeCredentialRecord(r *CredentialRecord) error {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" {
		return errors.New("credential group name is required")
	}
	if r.ID == "" {
		r.ID = newID("cred")
	}
	if r.StorageMode != "server_synced" && r.StorageMode != "runner_local" {
		r.StorageMode = "server_synced"
	}
	if r.UsagePolicy != "all" && r.UsagePolicy != "chat_only" && r.UsagePolicy != "cron_explicit" {
		r.UsagePolicy = "all"
	}
	if r.Scope != "global" && r.Scope != "workspace" && r.Scope != "runner" {
		r.Scope = "global"
	}
	r.WorkspacePath = NormalizeWorkspaceKey(r.WorkspacePath)
	r.ProbeCommand = strings.TrimSpace(r.ProbeCommand)
	r.Description = strings.TrimSpace(r.Description)
	r.Preset = strings.TrimSpace(r.Preset)

	seen := map[string]bool{}
	cleaned := make([]CredentialEntry, 0, len(r.Entries))
	for _, e := range r.Entries {
		k := strings.TrimSpace(e.Key)
		if k == "" {
			continue
		}
		if !IsValidEnvKey(k) {
			return fmt.Errorf("invalid environment variable name %q", k)
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		e.Key = k
		e.Note = strings.TrimSpace(e.Note)
		cleaned = append(cleaned, e)
	}
	r.Entries = cleaned
	return nil
}

func (s *Store) mergeAndEncryptEntries(incoming []CredentialEntry, existing []CredentialEntry, storeValues bool) ([]CredentialEntry, error) {
	prevByKey := make(map[string]CredentialEntry, len(existing))
	for _, e := range existing {
		prevByKey[e.Key] = e
	}
	out := make([]CredentialEntry, 0, len(incoming))
	for _, e := range incoming {
		k := strings.TrimSpace(e.Key)
		if k == "" {
			continue
		}
		if !IsValidEnvKey(k) {
			return nil, fmt.Errorf("invalid environment variable name %q", k)
		}
		prev := prevByKey[k]
		entry := CredentialEntry{
			Key:  k,
			Note: strings.TrimSpace(e.Note),
		}
		if !storeValues {
			// runner_local mode: Server never stores secret values
			out = append(out, entry)
			continue
		}
		switch {
		case e.ClearValue:
			entry.EncryptedVal = ""
			entry.ValueHint = ""
			entry.HasValue = false
		case e.Value != "":
			enc, err := s.EncryptSecret(e.Value)
			if err != nil {
				return nil, err
			}
			entry.EncryptedVal = enc
			entry.ValueHint = MaskSecretHint(e.Value)
			entry.HasValue = true
		case e.EncryptedVal != "":
			entry.EncryptedVal = e.EncryptedVal
			entry.ValueHint = e.ValueHint
			entry.HasValue = true
		case prev.EncryptedVal != "":
			entry.EncryptedVal = prev.EncryptedVal
			entry.ValueHint = prev.ValueHint
			entry.HasValue = true
		}
		out = append(out, entry)
	}
	return out, nil
}

// UpsertCredential encrypts and persists a credential group, preserving existing secret values when omitted on update.
func (s *Store) UpsertCredential(ctx context.Context, r CredentialRecord) (CredentialRecord, error) {
	if err := normalizeCredentialRecord(&r); err != nil {
		return CredentialRecord{}, err
	}

	var prev *CredentialRecord
	if existing, err := s.GetCredential(ctx, r.ID); err == nil && existing != nil {
		prev = existing
	} else if byName, err := s.GetCredentialByName(ctx, r.Name); err == nil && byName != nil {
		prev = byName
		r.ID = byName.ID
	}

	storeValues := r.StorageMode == "server_synced"
	var prevEntries []CredentialEntry
	var prevOverrides map[string][]CredentialEntry
	if prev != nil {
		prevEntries = prev.Entries
		prevOverrides = prev.RunnerOverrides
	}

	encEntries, err := s.mergeAndEncryptEntries(r.Entries, prevEntries, storeValues)
	if err != nil {
		return CredentialRecord{}, err
	}
	r.Entries = encEntries

	encOverrides := make(map[string][]CredentialEntry)
	if storeValues {
		for rid, ovList := range r.RunnerOverrides {
			rid = strings.TrimSpace(rid)
			if rid == "" || len(ovList) == 0 {
				continue
			}
			merged, mErr := s.mergeAndEncryptEntries(ovList, prevOverrides[rid], true)
			if mErr != nil {
				return CredentialRecord{}, mErr
			}
			if len(merged) > 0 {
				encOverrides[rid] = merged
			}
		}
	}
	r.RunnerOverrides = encOverrides
	r.UpdatedAt = time.Now().UnixMilli()

	trJSON, _ := json.Marshal(r.TargetRunners)
	if len(trJSON) == 0 || string(trJSON) == "null" {
		trJSON = []byte("[]")
	}
	entJSON, _ := json.Marshal(r.Entries)
	if len(entJSON) == 0 || string(entJSON) == "null" {
		entJSON = []byte("[]")
	}
	ovJSON, _ := json.Marshal(r.RunnerOverrides)
	if len(ovJSON) == 0 || string(ovJSON) == "null" {
		ovJSON = []byte("{}")
	}

	_, err = s.mainDB.ExecContext(ctx, `
		INSERT INTO credentials (
			id, name, description, preset, storage_mode, usage_policy, scope,
			workspace_path, target_runners_json, entries_json, runner_overrides_json,
			probe_command, enabled, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, description=excluded.description, preset=excluded.preset,
			storage_mode=excluded.storage_mode, usage_policy=excluded.usage_policy,
			scope=excluded.scope, workspace_path=excluded.workspace_path,
			target_runners_json=excluded.target_runners_json, entries_json=excluded.entries_json,
			runner_overrides_json=excluded.runner_overrides_json, probe_command=excluded.probe_command,
			enabled=excluded.enabled, updated_at=excluded.updated_at
	`, r.ID, r.Name, r.Description, r.Preset, r.StorageMode, r.UsagePolicy, r.Scope,
		r.WorkspacePath, string(trJSON), string(entJSON), string(ovJSON),
		r.ProbeCommand, b2i(r.Enabled), r.UpdatedAt)
	if err == nil {
		_, _ = s.BumpConfigRev(ctx)
	}
	return r, err
}

func scanCredentialRow(scan func(dest ...any) error) (CredentialRecord, error) {
	var r CredentialRecord
	var trS, entS, ovS string
	var en int
	if err := scan(
		&r.ID, &r.Name, &r.Description, &r.Preset, &r.StorageMode, &r.UsagePolicy, &r.Scope,
		&r.WorkspacePath, &trS, &entS, &ovS, &r.ProbeCommand, &en, &r.UpdatedAt,
	); err != nil {
		return r, err
	}
	_ = json.Unmarshal([]byte(trS), &r.TargetRunners)
	_ = json.Unmarshal([]byte(entS), &r.Entries)
	_ = json.Unmarshal([]byte(ovS), &r.RunnerOverrides)
	r.Enabled = en == 1
	return r, nil
}

const credentialSelectCols = `id, name, description, preset, storage_mode, usage_policy, scope, workspace_path, target_runners_json, entries_json, runner_overrides_json, probe_command, enabled, updated_at`

func (s *Store) GetCredential(ctx context.Context, id string) (*CredentialRecord, error) {
	row := s.mainDB.QueryRowContext(ctx, `SELECT `+credentialSelectCols+` FROM credentials WHERE id = ?`, id)
	r, err := scanCredentialRow(row.Scan)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) GetCredentialByName(ctx context.Context, name string) (*CredentialRecord, error) {
	row := s.mainDB.QueryRowContext(ctx, `SELECT `+credentialSelectCols+` FROM credentials WHERE name = ?`, name)
	r, err := scanCredentialRow(row.Scan)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) ListCredentials(ctx context.Context) ([]CredentialRecord, error) {
	rows, err := s.mainDB.QueryContext(ctx, `SELECT `+credentialSelectCols+` FROM credentials ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CredentialRecord
	for rows.Next() {
		r, err := scanCredentialRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) DeleteCredential(ctx context.Context, id string) error {
	_, err := s.mainDB.ExecContext(ctx, `DELETE FROM credentials WHERE id = ?`, id)
	if err == nil {
		_, _ = s.mainDB.ExecContext(ctx, `DELETE FROM credential_local_values WHERE credential_id = ?`, id)
		_, _ = s.BumpConfigRev(ctx)
	}
	return err
}

// ReplaceSyncedCredentials reconciles Runner-local credentials against an authoritative ConfigSnapshot.
func (s *Store) ReplaceSyncedCredentials(ctx context.Context, creds []CredentialRecord) error {
	keepIDs := make(map[string]bool, len(creds))
	for _, c := range creds {
		if _, err := s.UpsertCredential(ctx, c); err != nil {
			return err
		}
		keepIDs[c.ID] = true
	}
	existing, err := s.ListCredentials(ctx)
	if err != nil {
		return err
	}
	for _, ex := range existing {
		if !keepIDs[ex.ID] {
			_, _ = s.mainDB.ExecContext(ctx, `DELETE FROM credentials WHERE id = ?`, ex.ID)
			_, _ = s.mainDB.ExecContext(ctx, `DELETE FROM credential_local_values WHERE credential_id = ?`, ex.ID)
		}
	}
	return nil
}

// DecryptCredentialForRunner returns a copy of r with plaintext Values decrypted for targetRunnerID
// (stripping other runners' overrides). Used by Server when building a scoped ConfigSnapshot.
func (s *Store) DecryptCredentialForRunner(r CredentialRecord, targetRunnerID string) CredentialRecord {
	cp := r
	cp.Entries = make([]CredentialEntry, 0, len(r.Entries))
	for _, e := range r.Entries {
		item := CredentialEntry{
			Key:       e.Key,
			ValueHint: e.ValueHint,
			HasValue:  e.HasValue,
			Note:      e.Note,
		}
		if r.StorageMode == "server_synced" && e.EncryptedVal != "" {
			if plain, err := s.DecryptSecret(e.EncryptedVal); err == nil && plain != "" {
				item.Value = plain
				item.HasValue = true
			}
		}
		cp.Entries = append(cp.Entries, item)
	}
	cp.RunnerOverrides = nil
	if r.StorageMode == "server_synced" && targetRunnerID != "" {
		if ovList, ok := r.RunnerOverrides[targetRunnerID]; ok && len(ovList) > 0 {
			decOv := make([]CredentialEntry, 0, len(ovList))
			for _, e := range ovList {
				item := CredentialEntry{
					Key:       e.Key,
					ValueHint: e.ValueHint,
					HasValue:  e.HasValue,
					Note:      e.Note,
				}
				if e.EncryptedVal != "" {
					if plain, err := s.DecryptSecret(e.EncryptedVal); err == nil && plain != "" {
						item.Value = plain
						item.HasValue = true
					}
				}
				decOv = append(decOv, item)
			}
			cp.RunnerOverrides = map[string][]CredentialEntry{targetRunnerID: decOv}
		}
	}
	return cp
}

// --- Runner-Local Encrypted Values (Zero-Server-Storage mode) ---

// SetLocalCredentialValue encrypts and stores a runner-local secret value; empty value deletes it.
func (s *Store) SetLocalCredentialValue(ctx context.Context, credentialID, envKey, plaintext string) (string, error) {
	credentialID = strings.TrimSpace(credentialID)
	envKey = strings.TrimSpace(envKey)
	if credentialID == "" || !IsValidEnvKey(envKey) {
		return "", errors.New("invalid credential_id or env_key")
	}
	if plaintext == "" {
		_, err := s.mainDB.ExecContext(ctx, `DELETE FROM credential_local_values WHERE credential_id = ? AND env_key = ?`, credentialID, envKey)
		return "", err
	}
	enc, err := s.EncryptSecret(plaintext)
	if err != nil {
		return "", err
	}
	hint := MaskSecretHint(plaintext)
	_, err = s.mainDB.ExecContext(ctx, `
		INSERT INTO credential_local_values (credential_id, env_key, encrypted_val, value_hint, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(credential_id, env_key) DO UPDATE SET
			encrypted_val=excluded.encrypted_val, value_hint=excluded.value_hint, updated_at=excluded.updated_at
	`, credentialID, envKey, enc, hint, time.Now().UnixMilli())
	return hint, err
}

// ListLocalCredentialValues returns decrypted key->value map for a credential_id on the local Runner.
func (s *Store) ListLocalCredentialValues(ctx context.Context, credentialID string) (map[string]string, error) {
	rows, err := s.mainDB.QueryContext(ctx, `SELECT env_key, encrypted_val FROM credential_local_values WHERE credential_id = ?`, credentialID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, enc string
		if err := rows.Scan(&k, &enc); err != nil {
			return nil, err
		}
		if plain, err := s.DecryptSecret(enc); err == nil && plain != "" {
			out[k] = plain
		}
	}
	return out, rows.Err()
}

// ResolveEffectiveEnv resolves the effective environment variable map for a Runner session or Cron job.
// Precedence (lowest to highest): global < runner < workspace; within a group: entries < runner_overrides < local_values.
func (s *Store) ResolveEffectiveEnv(ctx context.Context, runnerID, workDir, triggerSource string, cronCredentialIDs []string) (map[string]string, []string) {
	creds, err := s.ListCredentials(ctx)
	if err != nil || len(creds) == 0 {
		return nil, nil
	}

	wsNorm := NormalizeWorkspaceKey(workDir)
	cronAllow := make(map[string]bool, len(cronCredentialIDs))
	for _, cid := range cronCredentialIDs {
		if strings.TrimSpace(cid) != "" {
			cronAllow[strings.TrimSpace(cid)] = true
		}
	}

	isCron := strings.EqualFold(triggerSource, "cron")

	matchesRunner := func(c CredentialRecord) bool {
		if len(c.TargetRunners) == 0 {
			return c.Scope != "runner"
		}
		for _, rid := range c.TargetRunners {
			if rid == runnerID {
				return true
			}
		}
		return false
	}

	scopeRank := func(sc string) int {
		switch sc {
		case "global":
			return 1
		case "runner":
			return 2
		case "workspace":
			return 3
		default:
			return 1
		}
	}

	var matched []CredentialRecord
	for _, c := range creds {
		if !c.Enabled {
			continue
		}
		if !matchesRunner(c) {
			continue
		}
		if c.Scope == "workspace" {
			if wsNorm == "" || NormalizeWorkspaceKey(c.WorkspacePath) != wsNorm {
				continue
			}
		}
		if isCron {
			if c.UsagePolicy == "chat_only" {
				continue
			}
			if c.UsagePolicy == "cron_explicit" && !cronAllow[c.ID] {
				continue
			}
		} else {
			if c.UsagePolicy == "cron_explicit" {
				continue
			}
		}
		matched = append(matched, c)
	}

	sort.SliceStable(matched, func(i, j int) bool {
		ri, rj := scopeRank(matched[i].Scope), scopeRank(matched[j].Scope)
		if ri != rj {
			return ri < rj
		}
		return matched[i].Name < matched[j].Name
	})

	envMap := map[string]string{}
	for _, c := range matched {
		if c.StorageMode == "server_synced" {
			for _, e := range c.Entries {
				if e.EncryptedVal != "" {
					if plain, err := s.DecryptSecret(e.EncryptedVal); err == nil && plain != "" {
						envMap[e.Key] = plain
					}
				}
			}
			if runnerID != "" {
				for _, e := range c.RunnerOverrides[runnerID] {
					if e.EncryptedVal != "" {
						if plain, err := s.DecryptSecret(e.EncryptedVal); err == nil && plain != "" {
							envMap[e.Key] = plain
						}
					}
				}
			}
		}
		if localVals, err := s.ListLocalCredentialValues(ctx, c.ID); err == nil {
			for k, v := range localVals {
				if v != "" {
					envMap[k] = v
				}
			}
		}
	}

	if len(envMap) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(envMap))
	for k := range envMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return envMap, keys
}

// ParseDotEnv parses pasted .env content into ordered key-value pairs.
func ParseDotEnv(raw string) []CredentialEntry {
	var out []CredentialEntry
	seen := map[string]int{}
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	for _, line := range lines {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		if strings.HasPrefix(s, "export ") {
			s = strings.TrimSpace(strings.TrimPrefix(s, "export "))
		}
		eq := strings.IndexByte(s, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(s[:eq])
		if !IsValidEnvKey(key) {
			continue
		}
		val := strings.TrimSpace(s[eq+1:])
		if len(val) >= 2 && ((val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'')) {
			val = val[1 : len(val)-1]
		}
		if idx, exists := seen[key]; exists {
			out[idx].Value = val
		} else {
			seen[key] = len(out)
			out = append(out, CredentialEntry{Key: key, Value: val})
		}
	}
	return out
}

// Ensure sql.ErrNoRows import is used if needed in future queries.
var _ = sql.ErrNoRows
