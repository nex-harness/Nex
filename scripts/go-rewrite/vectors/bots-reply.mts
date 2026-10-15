// bots-reply.mts prints the golden outputs asserted by
// go/internal/services/bots/formatter_golden_test.go.
//
// It drives the TypeScript replyFormatter.ts directly, so the Go port's expected
// strings come from the implementation being ported and not from a hand-typed
// copy that could drift with the Go.
//
// Run from the repository root:
//   node --import tsx scripts/go-rewrite/vectors/bots-reply.mts
import {
  formatBotToolCallSummaryLine,
  formatBotToolCallReply,
  formatBotPermissionRequestSummary,
  extractBotAssistantResponseMessages,
  formatBotAssistantReplyBlocks,
} from "../../../packages/services/src/bots/replyFormatter.js";
const out: any = {};
out.toolLineEdit = formatBotToolCallSummaryLine(
  {
    toolId: "t1",
    kind: "edit",
    title: "Edit file",
    input: { path: "/repo/src/a.ts", old_string: "a\nb", new_string: "a\nc\nd" },
    status: "completed",
  },
  { workspacePath: "/repo" },
);
out.toolLineEditZh = formatBotToolCallSummaryLine(
  {
    toolId: "t1",
    kind: "edit",
    title: "Edit file",
    input: { path: "/repo/src/a.ts", old_string: "a\nb", new_string: "a\nc\nd" },
    status: "completed",
  },
  { workspacePath: "/repo", locale: "zh-CN" },
);
out.toolLineFailed = formatBotToolCallSummaryLine(
  {
    toolId: "t2",
    kind: "bash",
    title: "Run",
    input: { command: "pnpm typecheck" },
    status: "failed",
    error: "  exit   code 2  ",
  },
  { locale: "en-US" },
);
out.toolLinePending = formatBotToolCallSummaryLine({ toolId: "t3", status: "pending" }, {});
out.toolLineInProgress = formatBotToolCallSummaryLine(
  { toolId: "t4", kind: "read", title: "Read", status: "in_progress" },
  { locale: "en-US" },
);
out.toolLineDenied = formatBotToolCallSummaryLine(
  { toolId: "t5", status: "denied" },
  { locale: "en-US" },
);
out.toolReply = formatBotToolCallReply(
  {
    toolId: "t1",
    kind: "edit",
    title: "Edit file",
    input: { path: "/repo/src/a.ts", old_string: "a", new_string: "a\nb" },
    status: "completed",
  },
  { locale: "en-US" },
);
out.permCommand = formatBotPermissionRequestSummary(
  {
    title: "Run command",
    description: "",
    kind: "exec",
    raw: { input: { command: "rm -rf /tmp/x", args: ["-rf", "/tmp/x"] } },
  },
  { locale: "en-US" },
);
out.permEditUpdate = formatBotPermissionRequestSummary(
  {
    title: "Edit src/a.ts",
    description: "",
    kind: "edit",
    raw: { changes: { "/repo/src/a.ts": { type: "update" } } },
  },
  { workspacePath: "/repo", locale: "en-US" },
);
out.permEditCreate = formatBotPermissionRequestSummary(
  {
    title: "Write src/b.ts",
    description: "",
    kind: "edit",
    raw: { changes: { "/repo/src/b.ts": { type: "add" } } },
  },
  { workspacePath: "/repo", locale: "en-US" },
);
out.permGeneric = formatBotPermissionRequestSummary(
  { title: "Something happened", description: "", kind: "other", raw: {} },
  { locale: "zh-CN" },
);
out.long = extractBotAssistantResponseMessages("x".repeat(4000), true).messages.map(
  (m) => m.length,
);
out.longNonForce = extractBotAssistantResponseMessages("abc\r\ndef", false);
out.blocks = formatBotAssistantReplyBlocks(
  [
    { type: "content", content: "hello" },
    {
      type: "tool-call",
      toolCall: {
        toolId: "t1",
        kind: "edit",
        title: "Edit",
        input: { path: "/repo/a.ts", old_string: "a", new_string: "a\nb" },
        status: "completed",
      },
    },
    {
      type: "change-summary",
      changeSummary: {
        fileCount: 2,
        files: [
          { path: "/repo/a.ts", added: 3, removed: 1 },
          { path: "/repo/b.ts", added: 2, removed: 0 },
        ],
      },
    },
  ],
  { locale: "en-US", workspacePath: "/repo" },
);
out.blocksZh = formatBotAssistantReplyBlocks(
  [
    {
      type: "change-summary",
      changeSummary: { fileCount: 1, files: [{ path: "/repo/a.ts", added: 1, removed: 2 }] },
    },
  ],
  { locale: "zh-CN" },
);
console.log(JSON.stringify(out, null, 2));
