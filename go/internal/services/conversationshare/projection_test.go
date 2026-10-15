// projection_test.go (step 3.8): the public projection is the privacy core of
// the share path. Every identity must be re-minted, every row rebuilt from an
// allow-list, and local-only fields must be structurally unable to appear.
package conversationshare

import (
	"encoding/json"
	"strings"
	"testing"
)

// artifactRow builds an artifact row as a tool would have recorded it.
func artifactRow(productTurnID, turnID, versionID string, rowID int64) row {
	return row{
		"rowId": float64(rowID), "kind": "artifact", "productTurnId": productTurnID,
		"turnId": turnID, "entityId": versionID + "-entity", "createdAt": float64(1700000002000),
		"createdAtSeq": float64(3), "artifactVersionId": versionID,
		"logicalArtifactKey": versionID, "displayName": "report.pdf", "artifactType": "pdf",
		"mimeType": "application/pdf", "sizeBytes": float64(1024),
		"sha256": "ab12", "ref": "/Users/alice/work/report.pdf", "state": "current",
	}
}

func TestProjectionRemintsEveryIdentity(t *testing.T) {
	rows := append(completedTurn("pt-1", 1), artifactRow("pt-1", "pt-1-turn", "av-1", 3))
	selected, productTurnIDs, err := selectRows(rows, Selection{Kind: "all"})
	if err != nil {
		t.Fatalf("selectRows: %v", err)
	}
	projection, err := BuildPublicProjection(selected, productTurnIDs, nil)
	if err != nil {
		t.Fatalf("BuildPublicProjection: %v", err)
	}
	if len(projection.SelectedProductTurnIDs) != 1 || projection.SelectedProductTurnIDs[0] != "share-product-turn-1" {
		t.Fatalf("selected product turns not re-minted: %v", projection.SelectedProductTurnIDs)
	}
	encoded := rowsToString(t, projection.Rows)
	for _, leaked := range []string{"pt-1", "pt-1-turn", "pt-1-entity", "av-1", "/Users/alice"} {
		if strings.Contains(encoded, leaked) {
			t.Fatalf("local identity %q leaked into the projection:\n%s", leaked, encoded)
		}
	}
	for _, expected := range []string{
		"share-product-turn-1", "share-turn-1", "share-entity-1", "share-artifact-1",
		"nex-artifact://share/share-artifact-1",
	} {
		if !strings.Contains(encoded, expected) {
			t.Fatalf("expected %q in the projection:\n%s", expected, encoded)
		}
	}
	if len(projection.Artifacts) != 1 {
		t.Fatalf("artifacts = %d", len(projection.Artifacts))
	}
	descriptor := projection.Artifacts[0].Descriptor
	if descriptor.ArtifactID != "share-artifact-1" || descriptor.Sha256 != "ab12" ||
		descriptor.SizeBytes != 1024 || descriptor.Extension != "pdf" {
		t.Fatalf("descriptor = %#v", descriptor)
	}
	// SourceRef stays local (it is the local read key, never serialized).
	if projection.Artifacts[0].SourceRef != "/Users/alice/work/report.pdf" {
		t.Fatalf("source ref changed: %q", projection.Artifacts[0].SourceRef)
	}
}

func TestProjectionDropsRowsOutsideTheV1Set(t *testing.T) {
	rows := append(completedTurn("pt-1", 1),
		row{"rowId": float64(3), "kind": "subagent", "productTurnId": "pt-1", "turnId": "pt-1-turn",
			"createdAt": float64(1), "createdAtSeq": float64(2), "status": "completed"},
		row{"rowId": float64(4), "kind": "hookInvocation", "productTurnId": "pt-1", "turnId": "pt-1-turn",
			"createdAt": float64(1), "createdAtSeq": float64(3)},
	)
	selected, productTurnIDs, err := selectRows(rows, Selection{Kind: "all"})
	if err != nil {
		t.Fatalf("selectRows: %v", err)
	}
	projection, err := BuildPublicProjection(selected, productTurnIDs, nil)
	if err != nil {
		t.Fatalf("subagent/hookInvocation rows must be filtered, not rejected: %v", err)
	}
	if len(projection.Rows) != 2 {
		t.Fatalf("rows = %d, want 2 (the two retained kinds)", len(projection.Rows))
	}
}

func TestProjectionRejectsAnUnknownRowKind(t *testing.T) {
	rows := append(completedTurn("pt-1", 1),
		row{"rowId": float64(3), "kind": "brandNewKind", "productTurnId": "pt-1",
			"turnId": "pt-1-turn", "createdAt": float64(1), "createdAtSeq": float64(2)})
	selected, productTurnIDs, _ := selectRows(rows, Selection{Kind: "all"})
	if _, err := BuildPublicProjection(selected, productTurnIDs, nil); err == nil {
		t.Fatal("an unknown row kind must not silently pass into the projection")
	}
}

func TestProjectionRejectsNonTerminalRows(t *testing.T) {
	cases := []struct {
		name string
		row  row
		kind string
	}{
		{"running turn", row{"kind": "turnHeader", "state": "running"}, KindInvalidConversation},
		{"streaming text", row{"kind": "assistantText", "state": "streaming"}, KindInvalidConversation},
		{"running tool", row{"kind": "toolCall", "status": "running"}, KindInvalidConversation},
		{"running subagent", row{"kind": "subagent", "status": "running"}, KindInvalidConversation},
		{"inline tool image", row{
			"kind":    "toolCall",
			"status":  "completed",
			"display": map[string]any{"kind": "node_repl_images"},
		}, KindUnsafeStructure},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			rows := append(completedTurn("pt-1", 1),
				row{
					"rowId": float64(3), "kind": testCase.row["kind"], "productTurnId": "pt-1",
					"turnId": "pt-1-turn", "createdAt": float64(1), "createdAtSeq": float64(2),
				})
			for key, value := range testCase.row {
				if key == "kind" {
					continue
				}
				rows[2][key] = value
			}
			selected, productTurnIDs, _ := selectRows(rows, Selection{Kind: "all"})
			_, err := BuildPublicProjection(selected, productTurnIDs, nil)
			typed, ok := isServiceError(err)
			if !ok {
				t.Fatalf("error = %v", err)
			}
			if typed.Kind() != testCase.kind {
				t.Fatalf("kind = %q, want %q (%v)", typed.Kind(), testCase.kind, err)
			}
		})
	}
}

func TestProjectionRejectsInsecureStrings(t *testing.T) {
	for _, value := range []string{"file:///Users/alice/x", "data:image/png;base64,AAA", "DATA:text/plain,x"} {
		rows := append(completedTurn("pt-1", 1),
			row{
				"rowId": float64(3), "kind": "toolCall", "productTurnId": "pt-1", "turnId": "pt-1-turn",
				"entityId": "tc-1", "toolCallId": "tc-1", "createdAt": float64(1),
				"createdAtSeq": float64(2), "status": "completed",
				"output": map[string]any{"text": value},
			})
		selected, productTurnIDs, _ := selectRows(rows, Selection{Kind: "all"})
		_, err := BuildPublicProjection(selected, productTurnIDs, nil)
		typed, ok := isServiceError(err)
		if !ok || typed.Kind() != KindUnsafeStructure {
			t.Fatalf("value %q: error = %v", value, err)
		}
	}
}

func TestProjectionRejectsBareArtifactReferences(t *testing.T) {
	rows := append(completedTurn("pt-1", 1),
		row{
			"rowId": float64(3), "kind": "toolCall", "productTurnId": "pt-1", "turnId": "pt-1-turn",
			"entityId": "tc-1", "toolCallId": "tc-1", "createdAt": float64(1),
			"createdAtSeq": float64(2), "status": "completed",
			"output": map[string]any{"text": "nex-artifact://share/x"},
		})
	selected, productTurnIDs, _ := selectRows(rows, Selection{Kind: "all"})
	_, err := BuildPublicProjection(selected, productTurnIDs, nil)
	typed, ok := isServiceError(err)
	if !ok || typed.Kind() != KindArtifactProtocolNotReady {
		t.Fatalf("error = %v", err)
	}
}

func TestProjectionKeepsArtifactRefOutOfTheURLSweep(t *testing.T) {
	// artifact.ref is a LOCAL path; the sweep must skip it (deleting it would
	// be wrong too — it is re-minted). Use a row whose ref would otherwise trip.
	rows := append(completedTurn("pt-1", 1), artifactRow("pt-1", "pt-1-turn", "av-1", 3))
	selected, productTurnIDs, _ := selectRows(rows, Selection{Kind: "all"})
	if _, err := BuildPublicProjection(selected, productTurnIDs, nil); err != nil {
		t.Fatalf("a plain artifact ref must not trip the URL sweep: %v", err)
	}
}

func TestSelectRowsRejectsAmbiguousSelections(t *testing.T) {
	rows := completedTurn("pt-1", 1)
	cases := []struct {
		name      string
		selection Selection
	}{
		{"empty product turns", Selection{Kind: "productTurns"}},
		{"empty row anchors", Selection{Kind: "rowAnchors"}},
		{"unknown row", Selection{Kind: "rowAnchors", RowIDs: []int64{99}}},
		{"unknown product turn", Selection{Kind: "productTurns", ProductTurnIDs: []string{"nope"}}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, _, err := selectRows(rows, testCase.selection)
			typed, ok := isServiceError(err)
			if !ok || typed.Kind() != KindInvalidSelection {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestSelectRowsOrdersProductTurnsByHeaderOrder(t *testing.T) {
	rows := append(completedTurn("pt-2", 1), completedTurn("pt-1", 3)...)
	// Header order is pt-2 then pt-1; the selection set order must follow it.
	_, productTurnIDs, err := selectRows(rows, Selection{Kind: "all"})
	if err != nil {
		t.Fatalf("selectRows: %v", err)
	}
	if len(productTurnIDs) != 2 || productTurnIDs[0] != "pt-2" || productTurnIDs[1] != "pt-1" {
		t.Fatalf("product turn order = %v", productTurnIDs)
	}
}

func TestSelectRowsKeepsRowsByTurnIdentity(t *testing.T) {
	rows := completedTurn("pt-1", 1)
	// A legacy row without productTurnId but with the selected turnId.
	rows = append(rows, row{
		"rowId": float64(3), "kind": "assistantText", "turnId": "pt-1-turn",
		"createdAt": float64(1), "createdAtSeq": float64(2), "text": "legacy",
	})
	selected, _, err := selectRows(rows, Selection{Kind: "all"})
	if err != nil {
		t.Fatalf("selectRows: %v", err)
	}
	if len(selected) != 3 {
		t.Fatalf("selected %d rows, want 3 (the turn-identity fallback must keep the legacy row)", len(selected))
	}
}

func TestTurnOrdinalsAreAssignedOverAllHeaders(t *testing.T) {
	rows := append(completedTurn("pt-1", 1), completedTurn("pt-2", 3)...)
	ordinals := turnOrdinalByProductTurn(rows)
	if ordinals["pt-1"] != 1 || ordinals["pt-2"] != 2 {
		t.Fatalf("ordinals = %#v", ordinals)
	}
	// A duplicate header for the same turn keeps the first ordinal.
	duplicated := append(rows, completedTurn("pt-1", 5)...)
	if got := turnOrdinalByProductTurn(duplicated); got["pt-1"] != 1 || got["pt-2"] != 2 {
		t.Fatalf("duplicated header changed the ordinals: %#v", got)
	}
}

func TestFileExtensionAndMimeNormalization(t *testing.T) {
	cases := []struct {
		name, want string
		ok         bool
	}{
		{"report.pdf", "pdf", true},
		{"/a/b/report.PDF", "pdf", true},
		{"report.tar.gz", "gz", true},
		{"dir.d/report", "", false},
		{"noextension", "", false},
		{"trailingdot.", "", false},
	}
	for _, testCase := range cases {
		got, ok := fileExtension(testCase.name)
		if got != testCase.want || ok != testCase.ok {
			t.Errorf("fileExtension(%q) = %q/%v, want %q/%v", testCase.name, got, ok, testCase.want, testCase.ok)
		}
	}
	mimes := map[string]string{
		"APPLICATION/PDF":               "application/pdf",
		"application/pdf; charset=utf8": "application/pdf",
		"":                              "application/octet-stream",
		"   ":                           "application/octet-stream",
	}
	for input, want := range mimes {
		if got := normalizedMimeType(input); got != want {
			t.Errorf("normalizedMimeType(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNarrowCapabilitiesDropsUnsupportedValues(t *testing.T) {
	wire := capabilitiesWire{
		SchemaVersion: 1, TtlMs: 1000, MaxRows: 10, MaxPayloadBytes: 20,
		MaxArtifactCount: 3, MaxArtifactBytes: 30, MaxTotalArtifactBytes: 40,
		AccessModes: []string{AccessPrivate, "public_everyone", AccessPublicImport, AccessPrivate},
		AllowedArtifacts: []struct {
			Type       string   `json:"type"`
			Extensions []string `json:"extensions"`
			MimeTypes  []string `json:"mime_types"`
		}{
			{Type: "pdf", Extensions: []string{"pdf"}, MimeTypes: []string{"application/pdf"}},
			{Type: "archive", Extensions: []string{"zip"}, MimeTypes: []string{"application/zip"}},
			{Type: "archive", Extensions: []string{"7z"}, MimeTypes: []string{"application/x-7z"}},
		},
	}
	capabilities, unsupportedTypes, unsupportedModes := narrowCapabilities(wire)
	if len(capabilities.AllowedArtifacts) != 1 || capabilities.AllowedArtifacts[0].Type != "pdf" {
		t.Fatalf("allowed artifacts = %#v", capabilities.AllowedArtifacts)
	}
	if len(unsupportedTypes) != 1 || unsupportedTypes[0] != "archive" {
		t.Fatalf("unsupported types = %v (must be de-duplicated)", unsupportedTypes)
	}
	if len(unsupportedModes) != 1 || unsupportedModes[0] != "public_everyone" {
		t.Fatalf("unsupported modes = %v (must be de-duplicated)", unsupportedModes)
	}
	if len(capabilities.AccessModes) != 3 {
		t.Fatalf("a duplicate access mode must be kept once per wire entry: %#v", capabilities.AccessModes)
	}
}

func TestWorkspaceKeyPrefersIdentityOverPath(t *testing.T) {
	if got := workspaceKeyOf("/w/a", "ident-1"); got != "ident-1" {
		t.Fatalf("identity key = %q", got)
	}
	if got := workspaceKeyOf("/w/a", "   "); got != "/w/a" {
		t.Fatalf("blank identity must fall back to the path: %q", got)
	}
	if got := workspaceKeyOf("  ", ""); got != "__default_conversation_workspace__" {
		t.Fatalf("empty key = %q", got)
	}
}

func rowsToString(t *testing.T, rows []json.RawMessage) string {
	t.Helper()
	var builder strings.Builder
	for _, current := range rows {
		builder.Write(current)
		builder.WriteString("\n")
	}
	return builder.String()
}
