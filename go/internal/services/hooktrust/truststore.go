// truststore.go: the workspace-hook trust store file codec. The file
// (~/.nex/security/workspace-hook-trust-v1.json) is a permission boundary
// read by BOTH the services layer and the agent runtime, so the parse rules
// and the write format must be byte-compatible with the Node implementation
// (apps/nex-cli/packages/adapters/src/storage/workspace-hook-trust-store.ts):
// strict schema (no unknown fields), unique (identity, digest) keys, records
// written via JSON.stringify(value, null, 2) + "\n".
//
// Parse failures are uniformly corrupt — a consumer must NEVER act on a
// partial digest list (the services layer once did and showed "trusted"
// while the runtime hard-blocked: a display/runtime divergence).
package hooktrust

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// TrustStoreSchemaVersion ports WORKSPACE_HOOK_TRUST_STORE_SCHEMA_VERSION.
const TrustStoreSchemaVersion = 1

// TrustStoreFileName is the store file name.
const TrustStoreFileName = "workspace-hook-trust-v1.json"

var sha256DigestRe = regexp.MustCompile(`^[a-f0-9]{64}$`)
var isoDateTimeRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}[Tt]\d{2}:\d{2}:\d{2}(\.\d+)?([Zz]|[+-]\d{2}:\d{2})$`)

// TrustRecord ports WorkspaceHookTrustRecord (strict: every field checked).
type TrustRecord struct {
	WorkspaceIdentity           string  `json:"workspaceIdentity"`
	HookDeclarationDigest       string  `json:"hookDeclarationDigest"`
	DigestAlgorithm             string  `json:"digestAlgorithm"` // literal "sha256"
	Decision                    string  `json:"decision"`        // literal "trusted"
	GrantedAt                   string  `json:"grantedAt"`       // ISO datetime
	LastUsedAt                  string  `json:"lastUsedAt,omitempty"`
	BundleDigestAtGrant         string  `json:"bundleDigestAtGrant,omitempty"`
	EventAtGrant                string  `json:"eventAtGrant"`
	DisplayCommandAtGrant       string  `json:"displayCommandAtGrant"`
	SourcePathAtGrant           string  `json:"sourcePathAtGrant"`
	SourceDiscoveryOrderAtGrant *int64  `json:"sourceDiscoveryOrderAtGrant,omitempty"`
	MatcherAtGrant              *string `json:"matcherAtGrant,omitempty"`
	MatcherIndexAtGrant         *int64  `json:"matcherIndexAtGrant,omitempty"`
	HookIndexAtGrant            *int64  `json:"hookIndexAtGrant,omitempty"`
	AppVersionAtGrant           string  `json:"appVersionAtGrant,omitempty"`
}

// TrustStoreFile ports WorkspaceHookTrustStoreFile.
type TrustStoreFile struct {
	SchemaVersion int           `json:"schemaVersion"`
	Records       []TrustRecord `json:"records"`
}

// TrustStoreParseResult ports WorkspaceHookTrustStoreParseResult.
type TrustStoreParseResult struct {
	// Status: "ok" | "invalid".
	Status string
	File   *TrustStoreFile
}

// ParseTrustStoreContent ports parseWorkspaceHookTrustStoreContent: JSON
// syntax errors and schema failures both land in "invalid" — no partial
// results, ever (fail-closed).
func ParseTrustStoreContent(content string) TrustStoreParseResult {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return TrustStoreParseResult{Status: "invalid"}
	}
	// Strict: exactly two top-level keys.
	if len(raw) != 2 {
		return TrustStoreParseResult{Status: "invalid"}
	}
	var schemaVersion int64
	if data, ok := raw["schemaVersion"]; ok {
		if err := json.Unmarshal(data, &schemaVersion); err != nil || schemaVersion != TrustStoreSchemaVersion {
			return TrustStoreParseResult{Status: "invalid"}
		}
	} else {
		return TrustStoreParseResult{Status: "invalid"}
	}
	recordsData, ok := raw["records"]
	if !ok {
		return TrustStoreParseResult{Status: "invalid"}
	}
	var records []map[string]json.RawMessage
	if err := json.Unmarshal(recordsData, &records); err != nil {
		return TrustStoreParseResult{Status: "invalid"}
	}
	parsed := &TrustStoreFile{SchemaVersion: TrustStoreSchemaVersion}
	seenKeys := map[string]bool{}
	for _, recordRaw := range records {
		record, err := parseTrustRecord(recordRaw)
		if err != nil {
			return TrustStoreParseResult{Status: "invalid"}
		}
		key := record.WorkspaceIdentity + "\x00" + record.HookDeclarationDigest
		if seenKeys[key] {
			return TrustStoreParseResult{Status: "invalid"}
		}
		seenKeys[key] = true
		parsed.Records = append(parsed.Records, *record)
	}
	return TrustStoreParseResult{Status: "ok", File: parsed}
}

// trustRecordKnownFields is the strict field set of a record.
var trustRecordKnownFields = map[string]bool{
	"workspaceIdentity": true, "hookDeclarationDigest": true, "digestAlgorithm": true,
	"decision": true, "grantedAt": true, "lastUsedAt": true, "bundleDigestAtGrant": true,
	"eventAtGrant": true, "displayCommandAtGrant": true, "sourcePathAtGrant": true,
	"sourceDiscoveryOrderAtGrant": true, "matcherAtGrant": true, "matcherIndexAtGrant": true,
	"hookIndexAtGrant": true, "appVersionAtGrant": true,
}

func parseTrustRecord(raw map[string]json.RawMessage) (*TrustRecord, error) {
	// No unknown fields.
	for key := range raw {
		if !trustRecordKnownFields[key] {
			return nil, fmt.Errorf("unknown field %q", key)
		}
	}
	record := &TrustRecord{}
	required := []string{
		"workspaceIdentity", "hookDeclarationDigest", "digestAlgorithm", "decision",
		"grantedAt", "eventAtGrant", "displayCommandAtGrant", "sourcePathAtGrant",
	}
	for _, key := range required {
		if _, ok := raw[key]; !ok {
			return nil, fmt.Errorf("missing field %q", key)
		}
	}
	if err := strictString(raw["workspaceIdentity"], &record.WorkspaceIdentity, true); err != nil {
		return nil, err
	}
	if err := strictString(raw["hookDeclarationDigest"], &record.HookDeclarationDigest, false); err != nil {
		return nil, err
	}
	if !sha256DigestRe.MatchString(record.HookDeclarationDigest) {
		return nil, fmt.Errorf("bad digest")
	}
	if err := strictString(raw["digestAlgorithm"], &record.DigestAlgorithm, false); err != nil {
		return nil, err
	}
	if record.DigestAlgorithm != "sha256" {
		return nil, fmt.Errorf("bad digestAlgorithm")
	}
	if err := strictString(raw["decision"], &record.Decision, false); err != nil {
		return nil, err
	}
	if record.Decision != "trusted" {
		return nil, fmt.Errorf("bad decision")
	}
	if err := strictString(raw["grantedAt"], &record.GrantedAt, false); err != nil {
		return nil, err
	}
	if !isoDateTimeRe.MatchString(record.GrantedAt) {
		return nil, fmt.Errorf("bad grantedAt")
	}
	if err := strictString(raw["eventAtGrant"], &record.EventAtGrant, false); err != nil {
		return nil, err
	}
	if !IsEventName(record.EventAtGrant) {
		return nil, fmt.Errorf("bad eventAtGrant")
	}
	if err := strictString(raw["displayCommandAtGrant"], &record.DisplayCommandAtGrant, true); err != nil {
		return nil, err
	}
	if err := strictString(raw["sourcePathAtGrant"], &record.SourcePathAtGrant, true); err != nil {
		return nil, err
	}
	if data, ok := raw["lastUsedAt"]; ok {
		if err := strictString(data, &record.LastUsedAt, false); err != nil {
			return nil, err
		}
		if !isoDateTimeRe.MatchString(record.LastUsedAt) {
			return nil, fmt.Errorf("bad lastUsedAt")
		}
	}
	if data, ok := raw["bundleDigestAtGrant"]; ok {
		if err := strictString(data, &record.BundleDigestAtGrant, false); err != nil {
			return nil, err
		}
		if !sha256DigestRe.MatchString(record.BundleDigestAtGrant) {
			return nil, fmt.Errorf("bad bundleDigestAtGrant")
		}
	}
	if value, err := strictOptionalInt(raw, "sourceDiscoveryOrderAtGrant"); err != nil {
		return nil, err
	} else if value != nil {
		record.SourceDiscoveryOrderAtGrant = value
	}
	if value, err := strictOptionalInt(raw, "matcherIndexAtGrant"); err != nil {
		return nil, err
	} else if value != nil {
		record.MatcherIndexAtGrant = value
	}
	if value, err := strictOptionalInt(raw, "hookIndexAtGrant"); err != nil {
		return nil, err
	} else if value != nil {
		record.HookIndexAtGrant = value
	}
	if data, ok := raw["matcherAtGrant"]; ok {
		var matcher any
		if err := json.Unmarshal(data, &matcher); err != nil {
			return nil, err
		}
		switch value := matcher.(type) {
		case string:
			record.MatcherAtGrant = &value
		case nil:
			record.MatcherAtGrant = nil
		default:
			return nil, fmt.Errorf("bad matcherAtGrant")
		}
	}
	if data, ok := raw["appVersionAtGrant"]; ok {
		if err := strictString(data, &record.AppVersionAtGrant, true); err != nil {
			return nil, err
		}
	}
	return record, nil
}

func strictString(data json.RawMessage, target *string, nonEmpty bool) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	trimmed := strings.TrimSpace(value)
	if nonEmpty && trimmed == "" {
		return fmt.Errorf("empty string")
	}
	*target = value
	return nil
}

func strictOptionalInt(raw map[string]json.RawMessage, key string) (*int64, error) {
	data, ok := raw[key]
	if !ok {
		return nil, nil
	}
	var value float64
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	if value != float64(int64(value)) || value < 0 {
		return nil, fmt.Errorf("bad %s", key)
	}
	intValue := int64(value)
	return &intValue, nil
}

// SerializeTrustStore writes the store the way the Node writer does:
// JSON.stringify(store, null, 2) + "\n" with records in the given order.
func SerializeTrustStore(store *TrustStoreFile) ([]byte, error) {
	var builder strings.Builder
	builder.WriteString("{\n  \"schemaVersion\": ")
	builder.WriteString(fmt.Sprintf("%d", store.SchemaVersion))
	builder.WriteString(",\n  \"records\": [")
	if len(store.Records) == 0 {
		builder.WriteString("]\n}")
	} else {
		for index, record := range store.Records {
			if index > 0 {
				builder.WriteString(",")
			}
			builder.WriteString("\n    ")
			writeRecordJSON(&builder, record, 2)
		}
		builder.WriteString("\n  ]\n}")
	}
	return []byte(builder.String() + "\n"), nil
}

// writeRecordJSON emits one record with JSON.stringify(v, null, 2) layout at
// the given indent depth (fields in struct order, blank separators).
func writeRecordJSON(builder *strings.Builder, record TrustRecord, depth int) {
	indent := strings.Repeat("  ", depth)
	fieldIndent := strings.Repeat("  ", depth+1)
	builder.WriteString("{\n")
	first := true
	writeField := func(name string, value any) {
		if !first {
			builder.WriteString(",\n")
		}
		first = false
		builder.WriteString(fieldIndent)
		writeJSONString(builder, name)
		builder.WriteString(": ")
		writeCanonicalJSON(builder, value)
	}
	writeField("workspaceIdentity", record.WorkspaceIdentity)
	writeField("hookDeclarationDigest", record.HookDeclarationDigest)
	writeField("digestAlgorithm", record.DigestAlgorithm)
	writeField("decision", record.Decision)
	writeField("grantedAt", record.GrantedAt)
	if record.LastUsedAt != "" {
		writeField("lastUsedAt", record.LastUsedAt)
	}
	if record.BundleDigestAtGrant != "" {
		writeField("bundleDigestAtGrant", record.BundleDigestAtGrant)
	}
	writeField("eventAtGrant", record.EventAtGrant)
	writeField("displayCommandAtGrant", record.DisplayCommandAtGrant)
	writeField("sourcePathAtGrant", record.SourcePathAtGrant)
	if record.SourceDiscoveryOrderAtGrant != nil {
		writeField("sourceDiscoveryOrderAtGrant", *record.SourceDiscoveryOrderAtGrant)
	}
	if record.MatcherAtGrant != nil {
		writeField("matcherAtGrant", *record.MatcherAtGrant)
	}
	if record.MatcherIndexAtGrant != nil {
		writeField("matcherIndexAtGrant", *record.MatcherIndexAtGrant)
	}
	if record.HookIndexAtGrant != nil {
		writeField("hookIndexAtGrant", *record.HookIndexAtGrant)
	}
	if record.AppVersionAtGrant != "" {
		writeField("appVersionAtGrant", record.AppVersionAtGrant)
	}
	builder.WriteString("\n")
	builder.WriteString(indent)
	builder.WriteString("}")
}

// TrustStorePath resolves the store path under a storage root.
func TrustStorePath(storageRoot string) string {
	return filepath.Join(storageRoot, "security", TrustStoreFileName)
}

// ReadTrustStoreDig... — read + filter by workspace identity, returning the
// corrupt flag separately (ENOENT is NOT corrupt: it is a legitimate empty
// store; unreadable or invalid IS corrupt and every consumer must fail
// closed, not partially trust).
func ReadTrustedDigests(storageRoot, workspaceIdentity string) (digests map[string]bool, corrupt bool, err error) {
	content, err := os.ReadFile(TrustStorePath(storageRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bool{}, false, nil
		}
		return map[string]bool{}, true, nil
	}
	parsed := ParseTrustStoreContent(string(content))
	if parsed.Status != "ok" {
		return map[string]bool{}, true, nil
	}
	out := map[string]bool{}
	for _, record := range parsed.File.Records {
		if record.WorkspaceIdentity == workspaceIdentity {
			out[record.HookDeclarationDigest] = true
		}
	}
	return out, false, nil
}
