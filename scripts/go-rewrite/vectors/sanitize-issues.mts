// sanitize-issues.mts prints the TS redaction vector used by
// go/internal/services/conversationshare/errors_test.go
// (TestSanitizedIssuesMatchesTheTSVector). Run: npx tsx scripts/go-rewrite/vectors/sanitize-issues.mts

import { sanitizeConversationShareIssues } from "../../../packages/services/src/conversation-share/conversationShare.js";
const issues = [
  {
    code: "artifact_type_not_allowed",
    scope: "artifact",
    artifactDisplayName: "/etc/passwd",
    artifactType: "text",
    extension: "txt",
    mimeType: "text/plain",
    allowedFormats: ["/bad/path"],
    allowedArtifacts: [{ type: "/weird", extensions: [".x"], mimeTypes: ["a/b"] }],
  },
  {
    code: "artifact_changed",
    scope: "artifact",
    rowId: 3,
    turnOrdinal: 2,
    productTurnId: "pt-1",
    artifactDisplayName: "ok.txt",
    artifactType: "text",
    extension: "txt",
    mimeType: "text/plain",
    actual: 5,
    limit: 4,
    retryAfterMs: 100,
    phase: "downloading",
    availability: "changed",
  },
  { code: "rows_limit", scope: "conversation", actual: 999, limit: 500 },
  { code: "unknown_code", scope: "turn", mimeType: "not a mime", extension: "x".repeat(200) },
];
console.log(JSON.stringify(sanitizeConversationShareIssues(issues)));
