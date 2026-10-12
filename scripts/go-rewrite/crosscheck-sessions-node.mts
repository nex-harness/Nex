// crosscheck-sessions-node.mts — the Node half of crosscheck-sessions.mjs.
//
//   tsx crosscheck-sessions-node.mts write <db>   # write the text-turn via the real SqliteSessionStore
//   tsx crosscheck-sessions-node.mts read <db>     # dump messages+parts as JSON
//   tsx crosscheck-sessions-node.mts session <db>  # dump the session row as JSON
//
// The write path uses the same repository calls as gen-store-vectors.mts, with
// the same fixed ids and clock, so the rows are deterministic.
import { DatabaseSync } from "node:sqlite";
import { createSqliteSessionStore } from "../../apps/nex-cli/packages/adapters/src/storage/session-store/sqlite-session-store.ts";
import * as sessionRepository from "../../apps/nex-cli/packages/adapters/src/storage/session-store/repositories/sessions.ts";

const NOW = 2_000_000_000_500;
Date.now = () => NOW;

const SESSION = "sess_00000000-0000-4000-8000-000000000001";
const TURN = "turn_00000000-0000-4000-8000-000000000002";
const TRACE = "trace-0000-0000-0000-000000000000";
const USER_MSG = "msg_test_user_1";
const ASSISTANT_MSG = "msg_test_assistant_1";
const DIR = "/work/proj";
const PROJECT = "proj_work-proj";
const T = {
  sessionCreate: NOW - 20_000,
  userMessage: NOW - 18_000,
  modelStart: NOW - 17_000,
  assistantComplete: NOW - 15_000,
};

const [, , action, dbPath] = process.argv;

const store = createSqliteSessionStore({ dbPath });

if (action === "write") {
  await store.createSession({
    id: SESSION,
    projectID: PROJECT,
    taskType: "interactive",
    traceID: TRACE,
    slug: SESSION,
    directory: DIR,
    path: DIR,
    title: "Say hello",
    titleSource: "first_input",
    version: "1.2.3",
    permission: { mode: "build" },
    time: { created: T.sessionCreate, updated: T.sessionCreate },
  });
  await store.saveSessionEntry({
    id: `${SESSION}:runtime-model-selection`,
    sessionID: SESSION,
    type: "runtime/model_selection",
    touchSession: false,
    time: { created: T.sessionCreate, updated: T.sessionCreate },
    data: { providerId: "mock", modelId: "mock-model", options: { reasoningLevel: "none" } },
  });
  await store.saveSessionEntry({
    id: `${SESSION}:runtime-execution-state`,
    sessionID: SESSION,
    type: "runtime/execution_state",
    touchSession: false,
    time: { created: T.sessionCreate, updated: T.sessionCreate },
    data: { mode: "build", planEnabled: false },
  });
  await store.saveMessage({
    id: USER_MSG,
    sessionID: SESSION,
    role: "user",
    time: { created: T.userMessage },
    agent: "nex-agent",
    modelSelection: {
      providerId: "mock",
      modelId: "mock-model",
      options: { reasoningLevel: "none" },
    },
    contextSnapshot: {
      envInfo: {
        cwd: DIR,
        platform: "linux",
        shell: "zsh",
        osVersion: "linux 6.8.0 x64",
        nodeVersion: "v24.0.0",
        isGitRepository: false,
        gitStatus: "not_repo",
      },
    },
    semantics: {
      origin: "real_user",
      kind: "user_prompt",
      uiVisibility: "visible",
      providerVisibility: "visible",
      transcriptVisibility: "visible",
    },
    anchor: { turnId: TURN, origin: "realUser", sourceCommandId: "cmd-0001" },
    tools: { Read: true, Write: true },
  });
  await store.savePart({
    id: "part_test_user_text_1",
    sessionID: SESSION,
    messageID: USER_MSG,
    type: "text",
    text: "Say hello",
    time: { start: T.userMessage, end: T.userMessage },
  });
  await store.saveMessage({
    id: ASSISTANT_MSG,
    sessionID: SESSION,
    role: "assistant",
    parentID: USER_MSG,
    modelId: "mock-model",
    providerId: "mock",
    mode: "build",
    planEnabled: false,
    agent: "nex-agent",
    path: { cwd: DIR, root: DIR },
    time: { created: T.modelStart, completed: T.assistantComplete },
    cost: 0,
    tokens: { total: 120, input: 100, output: 20, reasoning: 0, cache: { read: 0, write: 0 } },
    finish: "stop",
    semantics: {
      origin: "agent_runtime",
      kind: "assistant_response",
      uiVisibility: "visible",
      providerVisibility: "visible",
      transcriptVisibility: "visible",
    },
    anchor: { turnId: TURN },
  });
  for (const part of [
    {
      id: "part_test_step_start_1",
      sessionID: SESSION,
      messageID: ASSISTANT_MSG,
      type: "step-start",
    },
    {
      id: "part_test_step_text_1",
      sessionID: SESSION,
      messageID: ASSISTANT_MSG,
      type: "text",
      text: "Hello from the mock model.",
      time: { start: T.modelStart, end: T.assistantComplete },
    },
    {
      id: "part_test_step_finish_1",
      sessionID: SESSION,
      messageID: ASSISTANT_MSG,
      type: "step-finish",
      reason: "stop",
      cost: 0,
      tokens: { total: 120, input: 100, output: 20, reasoning: 0, cache: { read: 0, write: 0 } },
    },
  ]) {
    await store.savePart(part);
  }
  store.close();
  console.log("ok");
  process.exit(0);
}

// Read side: open with a fresh connection (the writer already closed the file).
// We dump the raw stored columns (not the decoded objects) so the comparison is
// against exactly what is on disk, matching the Go reader.
const db = new DatabaseSync(dbPath);
if (action === "read") {
  const rows = db
    .prepare(
      "select id, sequence, data from message where session_id = ? order by sequence is null, sequence, time_created, rowid",
    )
    .all(SESSION);
  const partStmt = db.prepare(
    "select id, sequence, data from part where message_id = ? order by sequence is null, sequence, time_created, id",
  );
  const out = rows.map((m) => ({
    id: m.id,
    sequence: m.sequence ?? null,
    data: JSON.parse(m.data),
    parts: partStmt
      .all(m.id)
      .map((p) => ({ id: p.id, sequence: p.sequence ?? null, data: JSON.parse(p.data) })),
  }));
  db.close();
  process.stdout.write(JSON.stringify(out));
  process.exit(0);
}

if (action === "session") {
  const session = sessionRepository.getSession(db, SESSION);
  db.close();
  process.stdout.write(JSON.stringify(session));
  process.exit(0);
}

console.error(`unknown action: ${action}`);
process.exit(2);
