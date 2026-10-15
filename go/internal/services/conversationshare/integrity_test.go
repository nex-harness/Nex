// integrity_test.go (step 3.8): the canonical-JSON digest the server
// recomputes with Node must agree byte-for-byte, or every share is reported
// as tampered. The vectors below were produced by running the TS
// canonicalizeValue/sha256ConversationShareJson over the same values.
package conversationshare

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func mustDigest(t *testing.T, value any) string {
	t.Helper()
	digest, err := sha256CanonicalJSON(value)
	if err != nil {
		t.Fatalf("sha256CanonicalJSON(%#v): %v", value, err)
	}
	return digest
}

func TestCanonicalJSONDigestMatchesNode(t *testing.T) {
	// (name, value, digest produced by Node's crypto + JSON.stringify rules)
	cases := []struct {
		name   string
		value  any
		digest string
	}{
		{"empty array", []any{}, "4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945"},
		{"sorted keys", []any{map[string]any{"b": 1.0, "a": 2.0}}, "82c9656ed6aa58d0ca5d00081451bfd33f9edd2a45f27c647781c8783759541d"},
		{"nested arrays", []any{map[string]any{"z": []any{1.0, 2.0, map[string]any{"y": "x"}}}}, "950cd0bd571add4af951c422fc6c80cbb42a3825ce2d570b9cdcf711c1e98738"},
		{"non-ascii string", []any{"héllo"}, "9bee78645407d0bdd0bc83467db179f92757ffcf36116b68dfa3a0aaeecd977e"},
		{"integral number", []any{1.0}, "080a9ed428559ef602668b4c00f114f1a11c3f6b02a435f0bdc154578e4d7f22"},
		{"big integral number", []any{1e21}, "5f5f297c3b2ec0b2793ea5cfe3f242ad4bd3aa438734268b6c9b7251a643d86c"},
		{"fraction", []any{0.1}, "06a6535c3ff5c9da828e977bc53df452117cd1579a24be9595a0b154d601d75c"},
		{"negative zero", []any{float64(int64(0))}, "d0bca111f8628137adc4c16f123496dcdd1d590d06cb5d9acd68b39fe656fb97"},
		{"empty key first", map[string]any{"": "", "a": "b"}, "b0653e830cc02c3ee3c9180ff9308ca7419e1529fe1680bb7898c645564507ac"},
		{"projection row", []any{map[string]any{"rowId": 1.0, "productTurnId": "t", "createdAt": 1.7e12}}, "e3d77d1e6db5a41a8cdf3cb74ee6539725d77cdac8478080489f5d7bf6683a98"},
		{"deep object", map[string]any{"nested": map[string]any{"deep": map[string]any{"deeper": map[string]any{"x": "y"}}}}, "fd25f7d7eefd275f7c306deb898ca58a400e52089a596a5672138f319c2efea4"},
		{"null member", []any{map[string]any{"a": nil}}, "827af7cb40290dba0c2ba9ef1cdfab2674c7fb44c4353e37aa2b6ecd32c87df2"},
		{"control chars", map[string]any{"k": "line\nbreak\ttab"}, "d9a39927895ae4e2172792b20486c3c5335518d12a8536d2b7fb2f72fa1b9d4f"},
		{"repeated rows", []any{map[string]any{"a": 1.0}, map[string]any{"a": 1.0}}, "dd65a2478e63520253ec34e3f19549dcd74b27a96fcc78656ef22de22da2a929"},
		{"ragged arrays", []any{map[string]any{"a": []any{[]any{1.0}, []any{2.0, []any{3.0}}}}}, "31ce1b86c30ce83bbfa9088c5fc76222b4c1d1bc36bea59c3534892cd765a961"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := mustDigest(t, testCase.value); got != testCase.digest {
				t.Fatalf("digest mismatch\n got %s\nwant %s", got, testCase.digest)
			}
		})
	}
}

func TestCanonicalizeRejectsUnrepresentableValues(t *testing.T) {
	if _, err := sha256CanonicalJSON([]any{nil, "ok"}); err != nil {
		t.Fatalf("null member must be representable: %v", err)
	}
	// NaN/Inf reach Go through raw float math where JSON.stringify would throw.
	if _, err := sha256CanonicalJSON(math.Inf(1)); err == nil {
		t.Fatal("expected an error for +Inf")
	}
	if _, err := sha256CanonicalJSON(math.NaN()); err == nil {
		t.Fatal("expected an error for NaN")
	}
	// A Go value with no JSON spelling must be rejected, not silently skipped.
	if _, err := sha256CanonicalJSON(struct{ A int }{1}); err == nil {
		t.Fatal("expected an error for an unsupported Go value")
	}
}

func TestVerifyIntegrityIgnoresServerSignedURLFields(t *testing.T) {
	rows := []json.RawMessage{json.RawMessage(`{"rowId":1,"kind":"assistantText"}`)}
	projectionDigest := mustDigest(t, rawRowsValue(rows))
	// Server-signed fields (ARTIFACT_URL_KEYS) are attached on read and must
	// not take part in the descriptor-set digest.
	artifacts := []any{
		map[string]any{"artifact_id": "share-artifact-2", "display_name": "b.pdf", "download_url": "https://example.invalid/b", "url_expires_at": 1.0},
		map[string]any{"artifact_id": "share-artifact-1", "display_name": "a.pdf", "download_url": "https://example.invalid/a"},
	}
	artifactDigest := mustDigest(t, []any{
		map[string]any{"artifact_id": "share-artifact-1", "display_name": "a.pdf"},
		map[string]any{"artifact_id": "share-artifact-2", "display_name": "b.pdf"},
	})
	integrity := struct{ Projection, ArtifactSet string }{projectionDigest, artifactDigest}
	if !verifyIntegrity(rawRowsValue(rows), artifacts, integrity) {
		t.Fatal("integrity check must pass when only signed URL fields differ")
	}
	integrity.ArtifactSet = "deadbeef"
	if verifyIntegrity(rawRowsValue(rows), artifacts, integrity) {
		t.Fatal("integrity check must fail on a tampered artifact digest")
	}
}

func TestVerifyIntegrityRejectsEditedRows(t *testing.T) {
	rows := []json.RawMessage{json.RawMessage(`{"rowId":1,"kind":"assistantText"}`)}
	integrity := struct{ Projection, ArtifactSet string }{
		mustDigest(t, rawRowsValue(rows)),
		mustDigest(t, []any{}),
	}
	edited := []json.RawMessage{json.RawMessage(`{"rowId":1,"kind":"assistantText","text":"injected"}`)}
	if verifyIntegrity(rawRowsValue(edited), []any{}, integrity) {
		t.Fatal("integrity check must fail when a row gains a field")
	}
}

func TestBuildConfirmRequestSortsArtifactsAndFillsDigests(t *testing.T) {
	rows := []json.RawMessage{
		json.RawMessage(`{"rowId":2}`),
		json.RawMessage(`{"rowId":1}`),
	}
	artifacts := []ArtifactDescriptor{
		{ArtifactID: "share-artifact-2", DisplayName: "b.pdf", Sha256: "bb", SizeBytes: 2},
		{ArtifactID: "share-artifact-1", DisplayName: "a.pdf", Sha256: "aa", SizeBytes: 1},
	}
	request, err := buildConfirmRequest([]string{"share-product-turn-1"}, rows, artifacts, 1700000000000)
	if err != nil {
		t.Fatalf("buildConfirmRequest: %v", err)
	}
	// 原数组不得被排序副作用改写（调用方随后还要用）。
	if artifacts[0].ArtifactID != "share-artifact-2" {
		t.Fatalf("input descriptors were mutated: %s", artifacts[0].ArtifactID)
	}
	if request.DisclosureConfirmation.Version != 1 ||
		request.DisclosureConfirmation.AcceptedAt != 1700000000000 ||
		!request.DisclosureConfirmation.AcknowledgedNoSecretDetection {
		t.Fatalf("disclosure confirmation not filled: %#v", request.DisclosureConfirmation)
	}
	// 摘要按行序（不被排序）与 artifact_id 排序后的集合计算。
	wantProjection := mustDigest(t, rawRowsValue(rows))
	if request.Integrity.ProjectionSha256 != wantProjection {
		t.Fatalf("projection digest %s != %s", request.Integrity.ProjectionSha256, wantProjection)
	}
	sorted := []ArtifactDescriptor{artifacts[1], artifacts[0]}
	wantArtifactSet, err := sha256OfDescriptors(sorted)
	if err != nil {
		t.Fatalf("sha256OfDescriptors: %v", err)
	}
	if request.Integrity.ArtifactSetSha256 != wantArtifactSet {
		t.Fatalf("artifact set digest %s != %s", request.Integrity.ArtifactSetSha256, wantArtifactSet)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal confirm request: %v", err)
	}
	if !strings.Contains(string(encoded), `"projection_sha256"`) ||
		!strings.Contains(string(encoded), `"acknowledged_no_secret_detection":true`) {
		t.Fatalf("confirm request wire shape is wrong: %s", encoded)
	}
}

func TestSha256OfRawRowsRejectsInvalidRow(t *testing.T) {
	if _, err := sha256OfRawRows([]json.RawMessage{json.RawMessage(`{`)}); err == nil {
		t.Fatal("expected an error for a malformed projection row")
	}
}
