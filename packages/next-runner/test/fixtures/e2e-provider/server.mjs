#!/usr/bin/env node
// Deterministic faux provider for the next/ E2E suites.
//
// Every suite needs one model whose answers are scripted and that never leaves
// the process. This server hosts pi-ai's own faux provider and exposes it over a
// minimal HTTP API; the runner's fallback models.json points a provider at it
// through the bundled `e2e-fetch` extension
// (packages/next-runner/src/extensions/e2eFauxProxy.ts), which rewrites each
// chat request to GET /e2e/faux here.
//
//   GET    /state                 -> {ok, callCount}
//   POST   /configure {responses} -> append scripted answers
//   DELETE /state                 -> reset the script and the counter
//
// A `responses` entry is one faux assistant message:
//   { "text": "..." }
//   { "toolCall": { "name": "read", "arguments": { "path": "notes.txt" } } }
//   { "text": "...", "toolCall": { ... } }
//
// The faux message is re-encoded as pi's OpenAI-completions SSE stream so the
// proxied request gets a normal provider answer back.
//
// Environment: E2E_FAUX_PORT (default 18080).

import http from "node:http";
import {
  createFauxCore,
  fauxAssistantMessage,
  fauxToolCall,
  fauxText,
} from "@earendil-works/pi-ai/providers/faux";

const port = Number(process.env.E2E_FAUX_PORT ?? 18080);
// The faux core gives this scripted provider a private API id; requests are
// replayed through it so tool-call streaming matches pi exactly.
const faux = createFauxCore({
  api: "e2e-faux",
  provider: "faux",
  models: [{ id: "faux-1", name: "Faux 1" }],
});
/** Scripted answers, replayed in order and then cycled. */
let script = [];

function toMessage(step) {
  if (Array.isArray(step?.toolCalls)) {
    const blocks = typeof step.text === "string" ? [fauxText(step.text)] : [];
    for (const call of step.toolCalls) {
      blocks.push(fauxToolCall(call.name, call.arguments ?? {}));
    }
    return fauxAssistantMessage(blocks, { stopReason: "toolCalls" });
  }
  if (step?.toolCall && typeof step.text === "string") {
    return fauxAssistantMessage([
      fauxText(step.text),
      fauxToolCall(step.toolCall.name, step.toolCall.arguments ?? {}),
    ]);
  }
  if (step?.toolCall) {
    return fauxAssistantMessage(fauxToolCall(step.toolCall.name, step.toolCall.arguments ?? {}), {
      stopReason: "toolCalls",
    });
  }
  return fauxAssistantMessage(typeof step?.text === "string" ? step.text : "");
}

/** Built-in defaults used when the script runs out (keeps long suites stable). */
const FALLBACK = [
  // The parser reads a tool-call name and its JSON arguments strictly; "read"
  // and "bash" are the pi tool names the runner maps to Read / Bash cards.
  {
    text: "Listing the directory and reading notes.txt.",
    toolCalls: [
      { name: "bash", arguments: { command: "ls -la" } },
      { name: "read", arguments: { path: "notes.txt" } },
    ],
  },
  { text: "hello from demo" },
];

/** The newest user prompt as text (the runner sends text blocks). */
function promptText(message) {
  const content = message?.content;
  if (typeof content === "string") return content;
  return Array.isArray(content) ? content.map((block) => block?.text ?? "").join(" ") : "";
}

/**
 * Prompts that name exactly one tool action get it as a tool call, so the
 * cards a suite asserts appear deterministically instead of depending on a
 * turn's position in the script:
 *  - permissions: "Use the write tool to create <file> containing exactly <x>"
 *    → the approval card;
 *  - conversation: "run exactly this command: sleep <n>" → a turn that stays
 *    busy while the suite queues more input.
 */
function scriptedTool(prompt) {
  // permissions: `create <file> containing exactly <word>`
  const write = /create ([\w.-]+) containing exactly (\w+)/iu.exec(prompt ?? "");
  if (write) {
    return {
      text: "",
      toolCall: { name: "write", arguments: { path: write[1], content: write[2] } },
    };
  }
  // file-changes: the prompt doubles as the transcript's user text, so a later
  // turn's prompt still contains the previous "create the file …" sentence. A
  // create-file prompt therefore always writes first line + the asked edit (the
  // re-run turn writes only the first line), and answers with the turn's own
  // "When done, reply with exactly X" marker.
  const file =
    /create the file ([\w.-]+) with exactly this content: ([^.]+)\.(?:\s*Then use the edit tool to replace "([^"]+)" with "([^"]+)")?/u.exec(
      prompt ?? "",
    );
  const plainReply = /with exactly rerun line [\w.-]+\. Use no tools/u.test(prompt ?? "");
  if (plainReply) {
    // The edited prompt inherits the first turn's "create the file …" text; a
    // plain-reply turn must not answer it with a write (only the first line is
    // asserted as restored, so a rewrite would mask the rewind).
    return {
      text: /with exactly (rerun line [\w.-]+)\. Use no tools/u.exec(prompt ?? "")?.[1] ?? "ok",
    };
  }
  const done = /When done, reply with exactly ([\w.-]+?)\.?\s*$/u.exec((prompt ?? "").trim());
  if (file && !plainReply) {
    return {
      text: done ? done[1] : "",
      toolCalls: [
        { name: "write", arguments: { path: file[1], content: file[2].trim() } },
        ...(file[3]
          ? [
              {
                name: "edit",
                arguments: { path: file[1], edits: [{ oldText: file[3], newText: file[4] }] },
              },
            ]
          : []),
      ],
    };
  }
  const bash = /command:\s*(sleep\s+\d+)/iu.exec(prompt ?? "");
  if (bash)
    return { text: "Sleeping.", toolCall: { name: "bash", arguments: { command: bash[1] } } };
  // attachments: read the attached text file with the read tool
  if (/attached text file/iu.test(prompt ?? "")) {
    return { text: "attached-secret-2048" };
  }
  return null;
}

/**
 * "Reply with exactly X" as the whole instruction: the answer is X, independent
 * of the script. A prompt that adds other instructions (a follow-up edit such as
 * "… Use no tools.") is not matched, so the turn keeps its other behaviour.
 */
function exactReply(prompt) {
  // The persisted prompt ends with its (empty) attachment block, so the sentence
  // is matched at the start; a follow-up instruction after it means "not exact".
  const match = /^Reply with exactly ([^\s.]+)(\.?)(.*)$/su.exec((prompt ?? "").trim());
  if (!match) return null;
  const rest = (match[3] ?? "").replace(/\[[^\]]*\]/gu, "").trim();
  return rest === "" ? { text: match[1] } : null;
}
/** A short prompt-derived title for title generation (see `answer`). */
function titleOf(prompt) {
  const firstLine = (prompt ?? "").split("\n")[0] ?? "";
  const cleaned = firstLine
    // An "exactly X" marker is a reply the conversation suite asserts on; a title
    // containing it would stay in the sidebar/header after an edit rewinds the
    // turn. Markers the suites find *rows* by (task names) are kept.
    .replace(/Reply with exactly:?\s*([^\s.]+)/giu, (_, marker) =>
      /(?:^|[^a-z])(?:pin-me|archive-me|renamed)-/u.test(marker) ||
      /^(?:pin|archive)-/u.test(marker)
        ? marker
        : "",
    )
    .replace(/\.\s.*$/u, "")
    .trim();
  return cleaned.slice(0, 60) || "session";
}

/**
 * Script cursor, one position per *turn* (a turn is the first request that
 * carries more than the prompt, i.e. the tool result round-trip does not
 * consume a position). The faux core advances its own `callCount` only per
 * answered message, so it cannot drive a script by itself.
 *
 * Why per-turn rather than per-request:
 *  - title generation (no `tools`) replays the session's first model on the
 *    first prompt; answering it from the script would use up the tool step;
 *  - a suite's single-prompt turns each get the next scripted answer in order,
 *    so the runner's own extra requests never shift a later chat.
 */
let scriptIndex = 0;

function answer(payload) {
  const steps = script.length > 0 ? script : FALLBACK;
  const messages = Array.isArray(payload.messages) ? payload.messages : [];
  // Title generation: the prompt itself, without consuming the script, so the
  // first chat turn still gets the scripted tool step. A title is persisted and
  // shown in the sidebar and header; a reply answer would keep the session's
  // title at a fixed scripted line (and, after an edit that rewinds a turn,
  // could keep "the replaced reply" visible there).
  if (!Array.isArray(payload.tools) || payload.tools.length === 0) {
    return { text: titleOf(promptText(messages[messages.map((m) => m.role).lastIndexOf("user")])) };
  }
  // A fresh prompt has no assistant/tool message after its last user message.
  const lastUser = messages.map((message) => message.role).lastIndexOf("user");
  // pi's own steer/follow-up messages are `role: "user"`, so only assistant or
  // tool messages after the newest prompt mark a tool round-trip.
  const continuation = messages
    .slice(lastUser + 1)
    .some((message) => message.role === "assistant" || message.role === "tool");
  if (!continuation) {
    const prompt = promptText(messages[lastUser]);
    const tool = scriptedTool(prompt);
    if (tool) return tool;
    const exact = exactReply(prompt);
    if (exact) return exact;
    const step = steps[scriptIndex % steps.length];
    scriptIndex += 1;
    return step;
  }
  // The closing sentence: the last text-only entry, or a fixed line.
  const textOnly = steps.find(
    (step) => typeof step?.text === "string" && !step.toolCall && !step.toolCalls,
  );
  return textOnly ?? { text: "hello from demo" };
}

/** Encodes one assistant message as the SSE body pi's openai-completions parser reads. */
function encodeSSE(message) {
  const chunk = (delta, finish = null) =>
    `data: ${JSON.stringify({
      id: "chatcmpl-e2e",
      object: "chat.completion.chunk",
      created: 0,
      model: "faux-1",
      choices: [{ index: 0, delta, finish_reason: finish }],
    })}\n\n`;
  let body = chunk({ role: "assistant" });
  let toolIndex = 0;
  for (const block of message.content) {
    if (block.type === "text") {
      body += chunk({ content: block.text });
    } else if (block.type === "thinking") {
      // pi's openai-completions parser reads `reasoning_content`.
      body += chunk({ reasoning_content: block.thinking });
    } else if (block.type === "toolCall") {
      body += chunk({
        tool_calls: [
          {
            index: toolIndex,
            id: block.id,
            type: "function",
            function: { name: block.name, arguments: JSON.stringify(block.arguments ?? {}) },
          },
        ],
      });
      toolIndex += 1;
    }
  }
  const finish = message.content.some((block) => block.type === "toolCall") ? "tool_calls" : "stop";
  body += chunk({}, finish);
  return body + "data: [DONE]\n\n";
}

function readBody(request) {
  return new Promise((resolve) => {
    let body = "";
    request.on("data", (chunk) => {
      body += chunk;
    });
    request.on("end", () => {
      try {
        resolve(JSON.parse(body || "{}"));
      } catch {
        resolve({});
      }
    });
  });
}

const server = http.createServer(async (request, response) => {
  const url = request.url ?? "";
  if (request.method === "GET" && url === "/state") {
    response.writeHead(200, { "content-type": "application/json" });
    response.end(JSON.stringify({ ok: true, callCount: faux.state.callCount }));
    return;
  }
  if (request.method === "DELETE" && url === "/state") {
    script = [];
    scriptIndex = 0;
    faux.setResponses([]);
    faux.state.callCount = 0;
    response.writeHead(200, { "content-type": "application/json" });
    response.end(JSON.stringify({ ok: true }));
    return;
  }
  if (request.method === "POST" && url === "/configure") {
    // Optional ?reset=1 starts the script from its first entry.
    const body = await readBody(request);
    const steps = Array.isArray(body.responses) ? body.responses : [];
    script = steps;
    scriptIndex = 0;
    faux.setResponses([]);
    response.writeHead(200, { "content-type": "application/json" });
    response.end(JSON.stringify({ ok: true, responses: steps.length }));
    return;
  }
  if (request.method === "POST" && url === "/v1/chat/completions") {
    // One proxied chat request: replay the next scripted answer through the faux
    // core and re-encode its event stream for pi's chat-completions parser.
    const payload = await readBody(request);
    const model = faux.getModel("faux-1");
    let message;
    try {
      faux.setResponses([toMessage(answer(payload))]);
      const stream = faux.stream(model, { messages: [] }, {});
      const events = [];
      for await (const event of stream) events.push(event);
      const last = events.at(-1);
      message = last?.message ?? last?.partial;
      if (!message || last?.type === "error") {
        throw new Error(message?.errorMessage ?? "faux stream produced no message");
      }
    } catch (error) {
      response.writeHead(500, { "content-type": "application/json" });
      response.end(JSON.stringify({ error: { message: String(error) } }));
      return;
    }
    response.writeHead(200, {
      "content-type": "text/event-stream",
      "cache-control": "no-cache",
      connection: "keep-alive",
    });
    response.end(encodeSSE(message));
    return;
  }
  response.writeHead(404, { "content-type": "application/json" });
  response.end(JSON.stringify({ error: { message: `unexpected ${request.method} ${url}` } }));
});

server.listen(port, "127.0.0.1", () => {
  process.stderr.write(`[e2e-faux-provider] listening on http://127.0.0.1:${port}\n`);
});
