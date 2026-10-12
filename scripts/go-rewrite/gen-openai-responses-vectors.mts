/* eslint-disable max-lines -- 向量生成脚本：一条脚本驱动真实 @ai-sdk/openai 的 Responses 模型，产出 Go 侧 OpenAI Responses adapter 的全部对照向量。 */
// 用真实的 @ai-sdk/openai（createOpenAI(...).responses，即 OpenAIResponsesLanguageModel.doStream）
// 对自编的 Responses SSE 字节流解码，产出 Go 侧 internal/adapters/model/openairesponses
// 必须逐事件复现的 closed event set。
//
// 与 gen-model-vectors.mts 的差别（也正是本脚本存在的理由）：
//   - 那条链路是 chat-completions（@ai-sdk/openai-compatible），base URL 为 /v1/chat/completions；
//   - 这条链路是 Responses（@ai-sdk/openai 的 .responses），base URL 为 /v1/responses，
//     事件名（response.output_text.delta / response.reasoning_summary_text.delta /
//     response.output_item.added / response.function_call_arguments.delta / ...）完全不同，
//     block id 语义也不同（Responses 用 provider 自己的 item id，chat-completions 固定 txt-0）。
//
// SSE 原始字节写到 go/testdata/sse/openai-responses/*.txt，供 Go 测试直接重放；
// 事件向量写到 go/testdata/vectors/model-openai-responses.json。
import { mkdirSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { createOpenAI } from "@ai-sdk/openai";

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO = resolve(HERE, "../..");
const VECTORS = join(REPO, "go/testdata/vectors");
const SSE_DIR = join(REPO, "go/testdata/sse/openai-responses");
mkdirSync(VECTORS, { recursive: true });
mkdirSync(SSE_DIR, { recursive: true });

// ---------------------------------------------------------------------------
// 1. SSE fixtures. Authored as raw bytes exactly as a Responses endpoint (or the
//    proxy forwarding it) writes them. The `error: null` echo (R1) and the
//    missing-`event:`-header (R4) cases are deliberate regressions guards.
// ---------------------------------------------------------------------------

interface Fixture {
  name: string;
  /** Raw SSE text exactly as the provider would write it. */
  text: string;
  /** Why this fixture exists; carried into the vector so the Go test can log it. */
  notes?: string;
}

/** `event:` + `data:` pair, the shape the real Responses API emits. */
const ev = (type: string, body: Record<string, unknown>) =>
  `event: ${type}\ndata: ${JSON.stringify({ type, ...body })}\n\n`;

/** `data:` only, no `event:` line — some proxies drop it (R4). */
const dataOnly = (type: string, body: Record<string, unknown>) =>
  `data: ${JSON.stringify({ type, ...body })}\n\n`;

/** `data:` plus an explicit top-level `"error": null` echo (R1). */
const evWithNullError = (type: string, body: Record<string, unknown>) =>
  `event: ${type}\ndata: ${JSON.stringify({ type, error: null, ...body })}\n\n`;

const seq = (n: number) => ({ sequence_number: n });

const textDelta = (n: number, itemId: string, delta: string) =>
  ev("response.output_text.delta", {
    ...seq(n),
    item_id: itemId,
    output_index: 0,
    content_index: 0,
    delta,
  });

const reasoningDelta = (n: number, itemId: string, delta: string, summaryIndex = 0) =>
  ev("response.reasoning_summary_text.delta", {
    ...seq(n),
    item_id: itemId,
    output_index: 0,
    summary_index: summaryIndex,
    delta,
  });

const messageAdded = (n: number, itemId: string) =>
  ev("response.output_item.added", {
    ...seq(n),
    output_index: 0,
    item: {
      id: itemId,
      type: "message",
      status: "in_progress",
      role: "assistant",
      content: [],
    },
  });

const messageDone = (n: number, itemId: string, text: string) =>
  ev("response.output_item.done", {
    ...seq(n),
    output_index: 0,
    item: {
      id: itemId,
      type: "message",
      status: "completed",
      role: "assistant",
      content: [{ type: "output_text", text, annotations: [] }],
    },
  });

const completed = (n: number, usage: Record<string, unknown>) =>
  ev("response.completed", {
    ...seq(n),
    response: {
      id: "resp_mock",
      object: "response",
      created_at: 1_700_000_000,
      status: "completed",
      model: "deepseek-flash",
      output: [],
      error: null,
      incomplete_details: null,
      usage: {
        input_tokens: usage.input,
        output_tokens: usage.output,
        total_tokens: usage.input + usage.output,
        input_tokens_details: { cached_tokens: usage.cached ?? 0 },
        output_tokens_details: { reasoning_tokens: usage.reasoning ?? 0 },
      },
    },
  });

const fixtures: Fixture[] = [
  {
    name: "text-only",
    text:
      messageAdded(1, "msg_1") +
      textDelta(2, "msg_1", "Hel") +
      textDelta(3, "msg_1", "lo ") +
      textDelta(4, "msg_1", "world") +
      messageDone(5, "msg_1", "Hello world") +
      completed(6, { input: 10, output: 3 }) +
      "data: [DONE]\n\n",
  },
  {
    name: "reasoning-then-text",
    text:
      ev("response.output_item.added", {
        ...seq(1),
        output_index: 0,
        item: { id: "rs_1", type: "reasoning", summary: [] },
      }) +
      ev("response.reasoning_summary_part.added", {
        ...seq(2),
        item_id: "rs_1",
        output_index: 0,
        summary_index: 0,
        part: { type: "summary_text", text: "" },
      }) +
      reasoningDelta(3, "rs_1", "think") +
      reasoningDelta(4, "rs_1", "ing...") +
      ev("response.output_item.done", {
        ...seq(5),
        output_index: 0,
        item: {
          id: "rs_1",
          type: "reasoning",
          summary: [{ type: "summary_text", text: "thinking..." }],
        },
      }) +
      messageAdded(6, "msg_1") +
      textDelta(7, "msg_1", "answer") +
      messageDone(8, "msg_1", "answer") +
      completed(9, { input: 5, output: 7, reasoning: 3, cached: 2 }) +
      "data: [DONE]\n\n",
  },
  {
    name: "tool-call-split-args",
    text:
      ev("response.output_item.added", {
        ...seq(1),
        output_index: 0,
        item: {
          id: "fc_1",
          type: "function_call",
          status: "in_progress",
          call_id: "call_1",
          name: "Read",
          arguments: "",
        },
      }) +
      ev("response.function_call_arguments.delta", {
        ...seq(2),
        item_id: "fc_1",
        output_index: 0,
        delta: '{"file_p',
      }) +
      ev("response.function_call_arguments.delta", {
        ...seq(3),
        item_id: "fc_1",
        output_index: 0,
        delta: 'ath":"/tmp/x.txt"}',
      }) +
      ev("response.function_call_arguments.done", {
        ...seq(4),
        item_id: "fc_1",
        output_index: 0,
        arguments: '{"file_path":"/tmp/x.txt"}',
      }) +
      ev("response.output_item.done", {
        ...seq(5),
        output_index: 0,
        item: {
          id: "fc_1",
          type: "function_call",
          status: "completed",
          call_id: "call_1",
          name: "Read",
          arguments: '{"file_path":"/tmp/x.txt"}',
        },
      }) +
      completed(6, { input: 20, output: 15 }) +
      "data: [DONE]\n\n",
  },
  {
    name: "tool-call-two",
    text:
      ev("response.output_item.added", {
        ...seq(1),
        output_index: 0,
        item: {
          id: "fc_0",
          type: "function_call",
          status: "in_progress",
          call_id: "c0",
          name: "Read",
          arguments: "",
        },
      }) +
      ev("response.output_item.added", {
        ...seq(2),
        output_index: 1,
        item: {
          id: "fc_1",
          type: "function_call",
          status: "in_progress",
          call_id: "c1",
          name: "Glob",
          arguments: "",
        },
      }) +
      ev("response.function_call_arguments.delta", {
        ...seq(3),
        item_id: "fc_0",
        output_index: 0,
        delta: "{}",
      }) +
      ev("response.function_call_arguments.delta", {
        ...seq(4),
        item_id: "fc_1",
        output_index: 1,
        delta: '{"p',
      }) +
      ev("response.function_call_arguments.delta", {
        ...seq(5),
        item_id: "fc_1",
        output_index: 1,
        delta: 'attern":"*.go"}',
      }) +
      ev("response.output_item.done", {
        ...seq(6),
        output_index: 0,
        item: {
          id: "fc_0",
          type: "function_call",
          status: "completed",
          call_id: "c0",
          name: "Read",
          arguments: "{}",
        },
      }) +
      ev("response.output_item.done", {
        ...seq(7),
        output_index: 1,
        item: {
          id: "fc_1",
          type: "function_call",
          status: "completed",
          call_id: "c1",
          name: "Glob",
          arguments: '{"pattern":"*.go"}',
        },
      }) +
      completed(8, { input: 30, output: 22 }) +
      "data: [DONE]\n\n",
  },
  {
    name: "tool-call-then-text",
    text:
      messageAdded(1, "msg_1") +
      textDelta(2, "msg_1", "Let me look.") +
      messageDone(3, "msg_1", "Let me look.") +
      ev("response.output_item.added", {
        ...seq(4),
        output_index: 1,
        item: {
          id: "fc_1",
          type: "function_call",
          status: "in_progress",
          call_id: "call_1",
          name: "Bash",
          arguments: "",
        },
      }) +
      ev("response.function_call_arguments.delta", {
        ...seq(5),
        item_id: "fc_1",
        output_index: 1,
        delta: '{"command":"ls"}',
      }) +
      ev("response.output_item.done", {
        ...seq(6),
        output_index: 1,
        item: {
          id: "fc_1",
          type: "function_call",
          status: "completed",
          call_id: "call_1",
          name: "Bash",
          arguments: '{"command":"ls"}',
        },
      }) +
      completed(7, { input: 40, output: 18 }) +
      "data: [DONE]\n\n",
  },
  {
    name: "usage-with-details",
    text:
      messageAdded(1, "msg_1") +
      textDelta(2, "msg_1", "hi") +
      messageDone(3, "msg_1", "hi") +
      completed(4, { input: 100, output: 20, reasoning: 8, cached: 30 }) +
      "data: [DONE]\n\n",
  },
  {
    name: "error-mid-stream",
    text:
      messageAdded(1, "msg_1") +
      textDelta(2, "msg_1", "partial") +
      ev("error", {
        ...seq(3),
        error: {
          type: "rate_limit_error",
          code: "rate_limit_exceeded",
          message: "slow down",
          param: null,
        },
      }) +
      "data: [DONE]\n\n",
  },
  {
    name: "error-null-echo",
    // R1: openai-go's ssestream aborts the moment gjson sees a top-level "error"
    // key, including an explicit null. Every successful Responses payload has
    // response.error = null, and proxies that echo the whole object emit it at the
    // top level too. This fixture must decode identically to text-only.
    notes: 'R1: top-level "error": null must not abort the stream',
    text:
      messageAdded(1, "msg_1") +
      evWithNullError("response.output_text.delta", {
        ...seq(2),
        item_id: "msg_1",
        output_index: 0,
        content_index: 0,
        delta: "Hel",
      }) +
      evWithNullError("response.output_text.delta", {
        ...seq(3),
        item_id: "msg_1",
        output_index: 0,
        content_index: 0,
        delta: "lo",
      }) +
      completed(4, { input: 6, output: 2 }) +
      "data: [DONE]\n\n",
  },
  {
    name: "no-event-header",
    // R4: some proxies forward only `data:` lines. The decoder must branch on the
    // JSON `type` field, never on the SSE `event:` name.
    notes: "R4: no `event:` lines at all",
    text:
      dataOnly("response.output_item.added", {
        ...seq(1),
        output_index: 0,
        item: {
          id: "msg_1",
          type: "message",
          status: "in_progress",
          role: "assistant",
          content: [],
        },
      }) +
      dataOnly("response.output_text.delta", {
        ...seq(2),
        item_id: "msg_1",
        output_index: 0,
        content_index: 0,
        delta: "no header",
      }) +
      dataOnly("response.output_item.done", {
        ...seq(3),
        output_index: 0,
        item: {
          id: "msg_1",
          type: "message",
          status: "completed",
          role: "assistant",
          content: [{ type: "output_text", text: "no header", annotations: [] }],
        },
      }) +
      dataOnly("response.completed", {
        ...seq(4),
        response: {
          id: "resp_mock",
          object: "response",
          created_at: 1_700_000_000,
          status: "completed",
          model: "deepseek-flash",
          output: [],
          error: null,
          incomplete_details: null,
          usage: {
            input_tokens: 4,
            output_tokens: 2,
            total_tokens: 6,
            input_tokens_details: { cached_tokens: 0 },
            output_tokens_details: { reasoning_tokens: 0 },
          },
        },
      }) +
      "data: [DONE]\n\n",
  },
  {
    name: "utf8-split",
    // "你好" / "🙂" across delta boundaries; the Go test splits at every byte
    // offset, so the multi-byte sequences land inside a chunk too.
    text:
      messageAdded(1, "msg_1") +
      textDelta(2, "msg_1", "你") +
      textDelta(3, "msg_1", "好") +
      textDelta(4, "msg_1", "🙂") +
      messageDone(5, "msg_1", "你好🙂") +
      completed(6, { input: 1, output: 3 }) +
      "data: [DONE]\n\n",
  },
  {
    name: "no-done",
    // Stream ends without [DONE]; the SDK still flushes on EOF.
    notes: "termination without the [DONE] sentinel",
    text:
      messageAdded(1, "msg_1") +
      textDelta(2, "msg_1", "no done marker") +
      messageDone(3, "msg_1", "no done marker") +
      completed(4, { input: 2, output: 3 }),
  },
  {
    name: "response-incomplete",
    text:
      messageAdded(1, "msg_1") +
      textDelta(2, "msg_1", "truncated") +
      ev("response.incomplete", {
        ...seq(3),
        response: {
          id: "resp_mock",
          object: "response",
          created_at: 1_700_000_000,
          status: "incomplete",
          model: "deepseek-flash",
          output: [],
          error: null,
          incomplete_details: { reason: "max_output_tokens" },
          usage: {
            input_tokens: 9,
            output_tokens: 9,
            total_tokens: 18,
            input_tokens_details: { cached_tokens: 0 },
            output_tokens_details: { reasoning_tokens: 0 },
          },
        },
      }) +
      "data: [DONE]\n\n",
  },
  {
    name: "response-failed",
    text:
      messageAdded(1, "msg_1") +
      ev("response.failed", {
        ...seq(2),
        response: {
          id: "resp_mock",
          object: "response",
          created_at: 1_700_000_000,
          status: "failed",
          model: "deepseek-flash",
          output: [],
          error: { code: "rate_limit_exceeded", message: "slow down" },
          incomplete_details: null,
          usage: null,
        },
      }) +
      "data: [DONE]\n\n",
  },
  {
    name: "crlf-and-comments",
    text:
      [
        ": keep-alive",
        textDelta(1, "msg_1", "crlf").replace(/\n\n$/, "\r\n\r\n"),
        ": another",
        messageDone(2, "msg_1", "crlf").replace(/\n\n$/, "\r\n\r\n"),
        completed(3, { input: 4, output: 1 }).replace(/\n\n$/, "\r\n\r\n"),
        "data: [DONE]\r\n\r\n",
      ].join("\n") + "\n",
  },
];

// ---------------------------------------------------------------------------
// 2. Decode with the real provider. The stream parts are serialised into the
//    same closed event vocabulary internal/modelclient uses, so the Go test can
//    compare event-by-event rather than type-by-type.
// ---------------------------------------------------------------------------

interface DecodedEvent {
  type: string;
  id?: string;
  /** text_delta / reasoning_delta / tool_input_delta */
  delta?: string;
  text?: string;
  toolName?: string;
  toolCallId?: string;
  input?: unknown;
  finishReason?: string;
  rawFinishReason?: string;
  usage?: unknown;
  error?: string;
}

interface SseVector {
  name: string;
  notes?: string;
  text: string;
  events?: DecodedEvent[];
  terminalError?: { name: string; message: string };
  /** decoded with chunk sizes 1, 2 and 3 bytes to pin byte-boundary behavior */
  split?: { [chunkSize: string]: { events: DecodedEvent[]; error?: string } };
}

function partToEvent(part: any): DecodedEvent | undefined {
  switch (part.type) {
    case "stream-start":
    case "response-metadata":
      return undefined;
    case "text-start":
      return { type: "text_start", id: part.id };
    case "text-delta":
      return { type: "text_delta", id: part.id, text: part.delta };
    case "text-end":
      return { type: "text_end", id: part.id };
    case "reasoning-start":
      return { type: "reasoning_start", id: part.id };
    case "reasoning-delta":
      return { type: "reasoning_delta", id: part.id, text: part.delta };
    case "reasoning-end":
      return { type: "reasoning_end", id: part.id };
    case "tool-input-start":
      return { type: "tool_input_start", id: part.id, toolName: part.toolName };
    case "tool-input-delta":
      return { type: "tool_input_delta", id: part.id, delta: part.delta };
    case "tool-input-end":
      return { type: "tool_input_end", id: part.id };
    case "tool-call":
      return {
        type: "tool_call",
        toolCallId: part.toolCallId,
        toolName: part.toolName,
        input: part.input,
      };
    case "finish":
      return {
        type: "finish",
        finishReason: part.finishReason?.unified,
        rawFinishReason: part.finishReason?.raw ?? null,
        usage: part.usage?.raw ?? null,
      };
    case "error":
      return {
        type: "error",
        error:
          typeof part.error === "string"
            ? part.error
            : String(part.error?.message ?? JSON.stringify(part.error)),
      };
    default:
      return { type: "raw", text: part.type };
  }
}

async function decodeWithChunks(
  text: string,
  chunkSize: number,
): Promise<{ events: DecodedEvent[]; error?: string }> {
  const encoder = new TextEncoder();
  const bytes = encoder.encode(text);
  let offset = 0;
  const stream = new ReadableStream<Uint8Array>({
    pull(controller) {
      if (offset >= bytes.length) {
        controller.close();
        return;
      }
      const end = Math.min(offset + chunkSize, bytes.length);
      controller.enqueue(bytes.slice(offset, end));
      offset = end;
    },
  });
  const fetchImpl = async () =>
    new Response(stream, { status: 200, headers: { "content-type": "text/event-stream" } });
  const model = createOpenAI({
    apiKey: "test",
    baseURL: "http://127.0.0.1/v1",
    fetch: fetchImpl as any,
  }).responses("deepseek-flash");
  try {
    const res: any = await model.doStream({
      prompt: [{ role: "user", content: [{ type: "text", text: "hi" }] }],
    });
    const events: DecodedEvent[] = [];
    for await (const part of res.stream as any) {
      const evv = partToEvent(part);
      if (evv) events.push(evv);
    }
    return { events };
  } catch (error: any) {
    return { events: [], error: String(error?.message ?? error) };
  }
}

async function decodeFixture(fixture: Fixture): Promise<SseVector> {
  const fetchImpl = async () =>
    new Response(fixture.text, { status: 200, headers: { "content-type": "text/event-stream" } });
  const model = createOpenAI({
    apiKey: "test",
    baseURL: "http://127.0.0.1/v1",
    fetch: fetchImpl as any,
  }).responses("deepseek-flash");

  const vector: SseVector = { name: fixture.name, notes: fixture.notes, text: fixture.text };
  try {
    const res: any = await model.doStream({
      prompt: [{ role: "user", content: [{ type: "text", text: "hi" }] }],
    });
    const events: DecodedEvent[] = [];
    for await (const part of res.stream as any) {
      const e = partToEvent(part);
      if (e) events.push(e);
    }
    vector.events = events;
  } catch (error: any) {
    vector.terminalError = {
      name: error?.name ?? "Error",
      message: String(error?.message ?? error),
    };
  }

  const split: SseVector["split"] = {};
  for (const size of [1, 2, 3]) {
    split[String(size)] = await decodeWithChunks(fixture.text, size);
  }
  vector.split = split;
  return vector;
}

const sseVectors: SseVector[] = [];
for (const fixture of fixtures) {
  sseVectors.push(await decodeFixture(fixture));
  writeFileSync(join(SSE_DIR, `${fixture.name}.txt`), fixture.text);
}

// ---------------------------------------------------------------------------
// 3. Request-shape vectors: the exact JSON body the real provider serialises
//    for a Responses request, so the Go transport's request body is compared
//    against a real SDK rather than against a hand-written expectation.
// ---------------------------------------------------------------------------

import { readFileSync } from "node:fs";
const TOOLS_JSON = join(REPO, "go/internal/tooldefs/tools.json");

/**
 * `tools.json` is stored in chat-completions shape ({"type":"function","function":{...}}).
 * The Responses API wants the flat shape. The provider does the flattening itself
 * (convert-to-openai-responses-input / openai-responses-prepare-tools), so we
 * capture its output rather than replicating the transform in the script.
 *
 * The SDK reads the schema from `inputSchema`, not `parameters` — feeding it
 * `parameters` results in a tool with no schema on the wire. Map onto the SDK
 * name here so the recorded request carries the real schemas.
 */
const flatTools = (JSON.parse(readFileSync(TOOLS_JSON, "utf8")) as any[]).map((entry) => ({
  type: "function",
  name: entry.function.name,
  description: entry.function.description,
  inputSchema: entry.function.parameters,
  strict: entry.function.strict ?? null,
}));

async function captureRequestBody(): Promise<Record<string, unknown>> {
  let captured: Record<string, unknown> | undefined;
  const fetchImpl = async (_url: any, init: any) => {
    captured = JSON.parse(String(init?.body ?? "{}"));
    return new Response(
      `event: response.completed\ndata: ${JSON.stringify({
        type: "response.completed",
        sequence_number: 1,
        response: {
          id: "resp_mock",
          object: "response",
          created_at: 1_700_000_000,
          status: "completed",
          model: "deepseek-flash",
          output: [],
          error: null,
          incomplete_details: null,
          usage: { input_tokens: 1, output_tokens: 1, total_tokens: 2 },
        },
      })}\n\ndata: [DONE]\n\n`,
      { status: 200, headers: { "content-type": "text/event-stream" } },
    );
  };
  const model = createOpenAI({
    apiKey: "test",
    baseURL: "http://127.0.0.1/v1",
    fetch: fetchImpl as any,
  }).responses("deepseek-flash");
  const res: any = await model.doStream({
    prompt: [
      { role: "system", content: [{ type: "text", text: "you are nex" }] },
      { role: "user", content: [{ type: "text", text: "hi" }] },
      {
        role: "assistant",
        content: [
          { type: "text", text: "" },
          {
            type: "tool-call",
            toolCallId: "call_1",
            toolName: "Read",
            input: '{"filePath":"/tmp/x"}',
          },
        ],
      },
      {
        role: "tool",
        content: [
          {
            type: "tool-result",
            toolCallId: "call_1",
            toolName: "Read",
            output: { type: "text", value: "file body" },
          },
        ],
      },
    ],
    tools: flatTools,
    toolChoice: { type: "auto" },
    maxOutputTokens: 4096,
  } as any);
  for await (const _ of res.stream as any) {
    void _;
  }
  return captured ?? {};
}

const requestBody = await captureRequestBody();

// ---------------------------------------------------------------------------
// Write.
// ---------------------------------------------------------------------------
writeFileSync(
  join(VECTORS, "model-openai-responses.json"),
  JSON.stringify({ sse: sseVectors, request: requestBody }, null, 1) + "\n",
);
console.log(
  `openai-responses sse: ${sseVectors.length} (${sseVectors.filter((v) => v.terminalError).length} terminal errors); request body keys: ${Object.keys(requestBody).sort().join(",")}`,
);
