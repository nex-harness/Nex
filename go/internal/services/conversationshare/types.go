// types.go ports the wire-facing types of packages/shared/src/conversation-share.ts
// that the service consumes: capabilities (with narrowing), descriptors, the
// preparation/confirm/upload records, preview and continuation.
//
// Inbound shapes are permissive (Go structs ignore unknown fields, which is
// the non-strict zod behaviour); outbound shapes are built by this package so
// strictness is enforced at construction.
package conversationshare

import (
	"encoding/json"
	"fmt"
	"strings"
)

// SchemaVersion ports CONVERSATION_SHARE_SCHEMA_VERSION.
const SchemaVersion = 1

// Access mode values.
const (
	AccessPrivate        = "private"
	AccessPublicReadonly = "public_readonly"
	AccessPublicImport   = "public_importable"
)

// artifactTypes is the conversationArtifactTypeSchema enum.
var artifactTypes = map[string]bool{
	"pdf": true, "pptx": true, "docx": true, "xlsx": true,
	"image": true, "html": true, "md": true, "text": true,
}

// Capabilities ports ConversationShareCapabilities (the narrowed form).
type Capabilities struct {
	SchemaVersion         int               `json:"schema_version"`
	TtlMs                 int64             `json:"ttl_ms"`
	MaxRows               int               `json:"max_rows"`
	MaxPayloadBytes       int64             `json:"max_payload_bytes"`
	MaxArtifactCount      int               `json:"max_artifact_count"`
	MaxArtifactBytes      int64             `json:"max_artifact_bytes"`
	MaxTotalArtifactBytes int64             `json:"max_total_artifact_bytes"`
	AccessModes           []string          `json:"access_modes"`
	AllowedArtifacts      []AllowedArtifact `json:"allowed_artifacts"`
}

// capabilitiesWire ports ConversationShareCapabilitiesWire (loose).
type capabilitiesWire struct {
	SchemaVersion         int      `json:"schema_version"`
	TtlMs                 int64    `json:"ttl_ms"`
	MaxRows               int      `json:"max_rows"`
	MaxPayloadBytes       int64    `json:"max_payload_bytes"`
	MaxArtifactCount      int      `json:"max_artifact_count"`
	MaxArtifactBytes      int64    `json:"max_artifact_bytes"`
	MaxTotalArtifactBytes int64    `json:"max_total_artifact_bytes"`
	AccessModes           []string `json:"access_modes"`
	AllowedArtifacts      []struct {
		Type       string   `json:"type"`
		Extensions []string `json:"extensions"`
		MimeTypes  []string `json:"mime_types"`
	} `json:"allowed_artifacts"`
}

// narrowCapabilities ports narrowConversationShareCapabilities: 丢弃本端
// 产不出的结果物类型与选不出的访问模式，并把被丢弃的取值回报给调用方。
func narrowCapabilities(wire capabilitiesWire) (Capabilities, []string, []string) {
	capabilities := Capabilities{
		SchemaVersion: wire.SchemaVersion, TtlMs: wire.TtlMs,
		MaxRows: wire.MaxRows, MaxPayloadBytes: wire.MaxPayloadBytes,
		MaxArtifactCount: wire.MaxArtifactCount, MaxArtifactBytes: wire.MaxArtifactBytes,
		MaxTotalArtifactBytes: wire.MaxTotalArtifactBytes,
	}
	var unsupportedArtifactTypes, unsupportedAccessModes []string
	for _, entry := range wire.AllowedArtifacts {
		if artifactTypes[entry.Type] {
			capabilities.AllowedArtifacts = append(capabilities.AllowedArtifacts, AllowedArtifact{
				Type: entry.Type, Extensions: entry.Extensions, MimeTypes: entry.MimeTypes,
			})
		} else if !containsString(unsupportedArtifactTypes, entry.Type) {
			unsupportedArtifactTypes = append(unsupportedArtifactTypes, entry.Type)
		}
	}
	for _, mode := range wire.AccessModes {
		if mode == AccessPrivate || mode == AccessPublicReadonly || mode == AccessPublicImport {
			capabilities.AccessModes = append(capabilities.AccessModes, mode)
		} else if !containsString(unsupportedAccessModes, mode) {
			unsupportedAccessModes = append(unsupportedAccessModes, mode)
		}
	}
	return capabilities, unsupportedArtifactTypes, unsupportedAccessModes
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// ArtifactDescriptor ports ConversationShareArtifactDescriptor (the outbound
// strict shape; marshalled with exact snake_case keys).
type ArtifactDescriptor struct {
	ArtifactID          string `json:"artifact_id"`
	LogicalArtifactKey  string `json:"logical_artifact_key"`
	ProducerProductTurn string `json:"producer_product_turn_id"`
	ArtifactVersion     int    `json:"artifact_version"`
	State               string `json:"state"`
	Ref                 string `json:"ref"`
	ArtifactType        string `json:"artifact_type"`
	DisplayName         string `json:"display_name"`
	OriginalPath        string `json:"original_path,omitempty"`
	Extension           string `json:"extension"`
	MimeType            string `json:"mime_type"`
	SizeBytes           int64  `json:"size_bytes"`
	Sha256              string `json:"sha256"`
}

// ShareRecord ports ConversationShareRecord.
type ShareRecord struct {
	ShareCode  string `json:"share_code"`
	ShareURL   string `json:"share_url"`
	AccessMode string `json:"access_mode"`
	ExpiresAt  int64  `json:"expires_at"`
}

// Preparation ports ConversationSharePreparation (discriminated on status).
type Preparation struct {
	Status        string       `json:"status"` // preparing | confirmed
	PreparationID string       `json:"preparation_id"`
	AccessMode    string       `json:"access_mode"`
	ExpiresAt     int64        `json:"expires_at"`
	Share         *ShareRecord `json:"share,omitempty"`
}

// ArtifactUpload ports ConversationShareArtifactUpload.
type ArtifactUpload struct {
	ArtifactID   string `json:"artifact_id"`
	SizeBytes    int64  `json:"size_bytes"`
	Sha256       string `json:"sha256"`
	Status       string `json:"status"`
	SafetyStatus string `json:"safety_status"`
}

// PreparationRequest ports ConversationSharePreparationRequest.
type PreparationRequest struct {
	ClientRequestID string `json:"client_request_id"`
	Title           string `json:"title"`
	SchemaVersion   int    `json:"schema_version"`
	AccessMode      string `json:"access_mode"`
	PayloadSha256   string `json:"payload_sha256"`
	ArtifactCount   int    `json:"artifact_count"`
}

// ConfirmRequest ports ConversationShareConfirmRequest. Rows stays raw JSON
// (the projection is a JSON tree; the digest is over exactly what is sent).
type ConfirmRequest struct {
	SelectedProductTurnIDs []string `json:"selected_product_turn_ids"`
	Projection             struct {
		Rows []json.RawMessage `json:"rows"`
	} `json:"projection"`
	Integrity struct {
		ProjectionSha256  string `json:"projection_sha256"`
		ArtifactSetSha256 string `json:"artifact_set_sha256"`
	} `json:"integrity"`
	DisclosureConfirmation struct {
		Version                       int   `json:"version"`
		AcceptedAt                    int64 `json:"accepted_at"`
		AcknowledgedNoSecretDetection bool  `json:"acknowledged_no_secret_detection"`
	} `json:"disclosure_confirmation"`
}

// continuationArtifact ports conversationShareContinuationArtifactSchema.
type continuationArtifact struct {
	ArtifactID           string `json:"artifact_id"`
	LogicalArtifactKey   string `json:"logical_artifact_key"`
	ProducerProductTurn  string `json:"producer_product_turn_id"`
	ArtifactVersion      int    `json:"artifact_version"`
	State                string `json:"state"`
	Ref                  string `json:"ref"`
	ArtifactType         string `json:"artifact_type"`
	DisplayName          string `json:"display_name"`
	Extension            string `json:"extension"`
	MimeType             string `json:"mime_type"`
	SizeBytes            int64  `json:"size_bytes"`
	Sha256               string `json:"sha256"`
	DownloadURL          string `json:"download_url"`
	DownloadURLExpiresAt int64  `json:"download_url_expires_at"`
}

// Continuation ports ConversationShareContinuation. RawRows keeps the exact
// server bytes for the integrity check and the on-disk readonly copy.
type Continuation struct {
	SchemaVersion        int                    `json:"schema_version"`
	ImportGrantID        string                 `json:"import_grant_id"`
	ImportGrantExpiresAt int64                  `json:"import_grant_expires_at"`
	Share                ContinuationShareMeta  `json:"share"`
	Rows                 []json.RawMessage      `json:"rows"`
	Artifacts            []continuationArtifact `json:"artifacts"`
	Integrity            struct {
		ProjectionSha256  string `json:"projection_sha256"`
		ArtifactSetSha256 string `json:"artifact_set_sha256"`
	} `json:"integrity"`
}

// ContinuationShareMeta extends the public metadata with share_id.
type ContinuationShareMeta struct {
	ShareID    string `json:"share_id"`
	Title      string `json:"title"`
	AccessMode string `json:"access_mode"`
	CreatedAt  int64  `json:"created_at"`
	ExpiresAt  int64  `json:"expires_at"`
}

// buildConfirmRequest ports buildConversationShareConfirmRequest: sorts the
// artifacts by artifact_id and fills the two digests.
func buildConfirmRequest(selectedProductTurnIDs []string, rows []json.RawMessage, artifacts []ArtifactDescriptor, disclosureAcceptedAt int64) (*ConfirmRequest, error) {
	sorted := make([]ArtifactDescriptor, len(artifacts))
	copy(sorted, artifacts)
	sortArtifactsByArtifactID(sorted)
	projectionDigest, err := sha256OfRawRows(rows)
	if err != nil {
		return nil, err
	}
	artifactSetDigest, err := sha256OfDescriptors(sorted)
	if err != nil {
		return nil, err
	}
	request := &ConfirmRequest{SelectedProductTurnIDs: selectedProductTurnIDs}
	request.Projection.Rows = rows
	request.Integrity.ProjectionSha256 = projectionDigest
	request.Integrity.ArtifactSetSha256 = artifactSetDigest
	request.DisclosureConfirmation.Version = 1
	request.DisclosureConfirmation.AcceptedAt = disclosureAcceptedAt
	request.DisclosureConfirmation.AcknowledgedNoSecretDetection = true
	return request, nil
}

func sortArtifactsByArtifactID(artifacts []ArtifactDescriptor) {
	for i := 1; i < len(artifacts); i++ {
		for j := i; j > 0 && artifacts[j-1].ArtifactID > artifacts[j].ArtifactID; j-- {
			artifacts[j-1], artifacts[j] = artifacts[j], artifacts[j-1]
		}
	}
}

// sha256OfRawRows hashes the projection rows the way the TS does: the array
// of raw (already-serialized) row values, canonicalized.
func sha256OfRawRows(rows []json.RawMessage) (string, error) {
	values := make([]any, len(rows))
	for index, row := range rows {
		var value any
		if err := json.Unmarshal(row, &value); err != nil {
			return "", fmt.Errorf("projection row is not valid JSON: %w", err)
		}
		values[index] = value
	}
	return sha256CanonicalJSON(values)
}

// sha256OfDescriptors hashes the descriptor set (artifact_version etc. as
// JSON trees).
func sha256OfDescriptors(artifacts []ArtifactDescriptor) (string, error) {
	values := make([]any, len(artifacts))
	for index, artifact := range artifacts {
		encoded, err := json.Marshal(artifact)
		if err != nil {
			return "", err
		}
		var value any
		if err := json.Unmarshal(encoded, &value); err != nil {
			return "", err
		}
		values[index] = value
	}
	return sha256CanonicalJSON(values)
}

// fileExtension ports fileExtension in conversationShareService.ts.
func fileExtension(fileName string) (string, bool) {
	base := fileName
	if index := strings.LastIndexAny(base, `/\`); index >= 0 {
		base = base[index+1:]
	}
	dot := strings.LastIndex(base, ".")
	if dot <= 0 || dot == len(base)-1 {
		return "", false
	}
	return strings.ToLower(base[dot+1:]), true
}

// normalizedMimeType ports normalizedMimeType.
func normalizedMimeType(mime string) string {
	if index := strings.Index(mime, ";"); index >= 0 {
		mime = mime[:index]
	}
	trimmed := strings.TrimSpace(mime)
	if trimmed == "" {
		return "application/octet-stream"
	}
	return strings.ToLower(trimmed)
}
