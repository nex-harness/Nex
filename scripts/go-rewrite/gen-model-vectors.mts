/* eslint-disable max-lines -- 向量生成脚本：一条脚本驱动一组真实 TS 类并顺序产出全部向量，拆开会让用例与其期望值分居两个文件。 */
// 用真实的 Vercel AI SDK + @ai-sdk/openai-compatible 生成 Go 侧 model client 的对照向量。
//
// 生成三类向量：
//   1. sse.json    —— 自编 SSE 字节流经真实 SDK（OpenAICompatibleChatLanguageModel.doStream）
//                     解析后得到的 closed event set（含 text/reasoning/tool_call/usage/finish/error）。
//   2. assembler.json —— 上述 SDK 事件喂给真实 StreamingToolCallAssembler 后的可见事件序列。
//   3. failure.json —— 真实 classifyModelFailure（>40 个输入组合）的分类结果。
//
// Go 侧必须逐字段复现。SSE 原始字节同时写到 go/testdata/sse/*.txt，供 Go 测试直接重放。
import { mkdirSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { OpenAICompatibleChatLanguageModel } from "@ai-sdk/openai-compatible";
import { StreamingToolCallAssembler } from "../../apps/nex-cli/packages/adapters/src/model/streaming-tool-call-assembler.ts";
import { classifyModelFailure } from "../../apps/nex-cli/packages/adapters/src/model/failure-classifier.ts";
import { APICallError } from "ai";

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO = resolve(HERE, "../..");
const VECTORS = join(REPO, "go/testdata/vectors");
const SSE_DIR = join(REPO, "go/testdata/sse");
mkdirSync(VECTORS, { recursive: true });
mkdirSync(SSE_DIR, { recursive: true });

// ---------------------------------------------------------------------------
// 1. SSE fixtures. Each is authored as raw bytes; we also record the split
//    points so the Go test can feed the same bytes in one shot (split-at-every-
//    byte is covered by Go's own fuzz/table test).
// ---------------------------------------------------------------------------

interface Fixture {
  name: string;
  /** Raw SSE text exactly as the mock provider would write it. */
  text: string;
  /** Optional: byte offset where utf-8 splitting matters. */
  notes?: string;
}

const chunk = (delta: object, finish: string | null = null, extra: object = {}) =>
  `data: ${JSON.stringify({
    id: "chatcmpl-mock",
    object: "chat.completion.chunk",
    created: 1_700_000_000,
    model: "mock-model",
    choices: [{ index: 0, delta, finish_reason: finish }],
    ...extra,
  })}\n\n`;

const fixtures: Fixture[] = [
  {
    name: "text-basic",
    text:
      chunk({ role: "assistant", content: "Hel" }) +
      chunk({ content: "lo " }) +
      chunk({ content: "world" }) +
      chunk({}, "stop", { usage: { prompt_tokens: 10, completion_tokens: 3, total_tokens: 13 } }) +
      "data: [DONE]\n\n",
  },
  {
    name: "tool-call-split-args",
    text:
      chunk({
        role: "assistant",
        tool_calls: [
          {
            index: 0,
            id: "call_1",
            type: "function",
            function: { name: "Read", arguments: '{"file_p' },
          },
        ],
      }) +
      chunk({ tool_calls: [{ index: 0, function: { arguments: 'ath":"/tmp/x.txt"}' } }] }) +
      chunk({}, "tool_calls", {
        usage: { prompt_tokens: 20, completion_tokens: 15, total_tokens: 35 },
      }) +
      "data: [DONE]\n\n",
  },
  {
    name: "tool-call-id-name-later",
    text:
      chunk({ role: "assistant", tool_calls: [{ index: 0, function: { arguments: '{"a":' } }] }) +
      chunk({
        tool_calls: [
          {
            index: 0,
            id: "call_late",
            type: "function",
            function: { name: "Write", arguments: "1}" },
          },
        ],
      }) +
      chunk({}, "tool_calls") +
      "data: [DONE]\n\n",
  },
  {
    name: "tool-call-two-indices",
    text:
      chunk({
        role: "assistant",
        tool_calls: [
          { index: 0, id: "c0", type: "function", function: { name: "Read", arguments: "{}" } },
          { index: 1, id: "c1", type: "function", function: { name: "Glob", arguments: '{"p' } },
        ],
      }) +
      chunk({ tool_calls: [{ index: 1, function: { arguments: 'attern":"*.go"}' } }] }) +
      chunk({}, "tool_calls") +
      "data: [DONE]\n\n",
  },
  {
    name: "usage-only-last-chunk",
    text:
      chunk({ role: "assistant", content: "hi" }) +
      chunk({}) +
      chunk({}, "stop", {
        usage: { prompt_tokens: 100, completion_tokens: 20, total_tokens: 120 },
      }) +
      "data: [DONE]\n\n",
  },
  {
    name: "reasoning-then-text",
    text:
      chunk({ role: "assistant", reasoning_content: "think" }) +
      chunk({ reasoning_content: "ing..." }) +
      chunk({ content: "answer" }) +
      chunk({}, "stop", { usage: { prompt_tokens: 5, completion_tokens: 5, total_tokens: 10 } }) +
      "data: [DONE]\n\n",
  },
  {
    name: "utf8-split",
    // "你好" / "🙂" split across data chunks; bytes are fed whole here, the Go
    // test splits at byte boundaries.
    text:
      chunk({ role: "assistant", content: "你" }) +
      chunk({ content: "好🙂" }) +
      chunk({}, "stop", { usage: { prompt_tokens: 1, completion_tokens: 3, total_tokens: 4 } }) +
      "data: [DONE]\n\n",
  },
  {
    name: "no-done",
    // stream ends without [DONE]; the SDK still flushes on EOF.
    text:
      chunk({ role: "assistant", content: "no done marker" }) +
      chunk({}, "stop", { usage: { prompt_tokens: 2, completion_tokens: 3, total_tokens: 5 } }),
  },
  {
    name: "empty-completion",
    text: chunk({ role: "assistant", content: "" }) + chunk({}, "stop"),
  },
  {
    name: "crlf-and-comments",
    text:
      [
        ": keep-alive comment",
        chunk({ role: "assistant", content: "crlf" }).replace(/\n\n$/, "\r\n\r\n"),
        ": another comment",
        chunk({}, "stop", {
          usage: { prompt_tokens: 4, completion_tokens: 1, total_tokens: 5 },
        }).replace(/\n\n$/, "\r\n\r\n"),
        "data: [DONE]\r\n\r\n",
      ].join("\n") + "\n",
  },
  {
    name: "multiline-data",
    // Two `data:` lines inside one event join with "\n"; the joined text is not
    // valid JSON so the SDK reports a JSON parse error.
    text: 'data: {"choices":\ndata: []}\n\n',
  },
  {
    name: "finish-length",
    text:
      chunk({ role: "assistant", content: "truncated" }) +
      chunk({}, "length", { usage: { prompt_tokens: 9, completion_tokens: 9, total_tokens: 18 } }) +
      "data: [DONE]\n\n",
  },
  {
    name: "content-filter",
    text:
      chunk({ role: "assistant", content: "x" }) + chunk({}, "content_filter") + "data: [DONE]\n\n",
  },
  {
    name: "unknown-finish-reason",
    text:
      chunk({ role: "assistant", content: "x" }) + chunk({}, "weird_reason") + "data: [DONE]\n\n",
  },
  {
    name: "function-call-finish",
    text:
      chunk({ role: "assistant", content: "x" }) + chunk({}, "function_call") + "data: [DONE]\n\n",
  },
  {
    name: "leading-bom",
    text:
      "\ufeff" +
      chunk({ role: "assistant", content: "bom" }) +
      chunk({}, "stop") +
      "data: [DONE]\n\n",
  },
  {
    name: "invalid-json",
    text: "data: {not json}\n\n",
  },
  {
    name: "sse-error-object",
    // provider error delivered inside a 200 payload
    text: 'data: {"error":{"message":"rate limited","type":"rate_limit_error","code":"1302"}}\n\n',
  },
  {
    name: "missing-function-name",
    // tool call whose name never arrives -> flush throws
    text:
      chunk({
        role: "assistant",
        tool_calls: [{ index: 0, id: "call_x", type: "function", function: { arguments: "{}" } }],
      }) +
      chunk({}, "tool_calls") +
      "data: [DONE]\n\n",
  },
  {
    name: "empty-args-then-name",
    // name arrives in the same delta as the first args; args empty means no
    // tool-input-delta is emitted for the initial buffered arguments.
    text:
      chunk({
        role: "assistant",
        tool_calls: [
          { index: 0, id: "c", type: "function", function: { name: "Read", arguments: "" } },
        ],
      }) +
      chunk({ tool_calls: [{ index: 0, function: { arguments: '{"a":1}' } }] }) +
      chunk({}, "tool_calls") +
      "data: [DONE]\n\n",
  },
];

interface DecodedEvent {
  type: string;
  id?: string;
  delta?: string;
  text?: string;
  toolName?: string;
  input?: unknown;
  toolCallId?: string;
  finishReason?: string;
  usage?: unknown;
  error?: string;
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
        usage: part.usage?.raw ?? null,
      };
    case "error":
      return { type: "error", error: part.error };
    default:
      return { type: "raw", text: part.type };
  }
}

interface SseVector {
  name: string;
  text: string;
  events?: DecodedEvent[];
  /** set when the SDK rejects the stream */
  terminalError?: { name: string; message: string };
  /** decoded with chunk sizes 1, 2 and 3 bytes to pin byte-boundary behavior */
  split?: { [chunkSize: string]: { events: DecodedEvent[]; error?: string } };
}

async function decodeWithChunks(
  text: string,
  chunkSize: number,
): Promise<{ events: DecodedEvent[]; error?: string }> {
  // Build a ReadableStream that emits fixed-size chunks; this exercises the
  // real SDK pipeline's pending-buffer logic at each boundary.
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
  const model = new OpenAICompatibleChatLanguageModel("mock-model", {
    provider: "mock",
    url: () => "http://127.0.0.1/v1/chat/completions",
    headers: () => ({}),
    fetch: fetchImpl as any,
  });
  try {
    const res: any = await model.doStream({
      prompt: [{ role: "user", content: [{ type: "text", text: "hi" }] }],
    });
    const events: DecodedEvent[] = [{ type: "start" }];
    for await (const part of res.stream as any) {
      const ev = partToEvent(part);
      if (ev) events.push(ev);
    }
    return { events };
  } catch (error: any) {
    return { events: [{ type: "start" }], error: String(error?.message ?? error) };
  }
}

async function decodeFixture(fixture: Fixture): Promise<SseVector> {
  const fetchImpl = async () =>
    new Response(fixture.text, { status: 200, headers: { "content-type": "text/event-stream" } });
  const model = new OpenAICompatibleChatLanguageModel("mock-model", {
    provider: "mock",
    url: () => "http://127.0.0.1/v1/chat/completions",
    headers: () => ({}),
    fetch: fetchImpl as any,
  });
  const vector: SseVector = { name: fixture.name, text: fixture.text };
  try {
    const res: any = await model.doStream({
      prompt: [{ role: "user", content: [{ type: "text", text: "hi" }] }],
    });
    const events: DecodedEvent[] = [{ type: "start" }];
    for await (const part of res.stream as any) {
      const ev = partToEvent(part);
      if (ev) events.push(ev);
    }
    vector.events = events;
  } catch (error: any) {
    vector.terminalError = {
      name: error?.name ?? "Error",
      message: String(error?.message ?? error),
    };
  }
  // Split vectors: decode the same bytes at 1/2/3-byte chunk sizes.
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
// 2. Assembler vectors: feed decoded SDK events through the real assembler.
// ---------------------------------------------------------------------------

function makeAssemblerEvent(ev: DecodedEvent): any {
  switch (ev.type) {
    case "text_start":
      return { type: "text_start", id: ev.id };
    case "text_delta":
      return { type: "text_delta", id: ev.id, text: ev.text };
    case "text_end":
      return { type: "text_end", id: ev.id };
    case "reasoning_start":
      return { type: "reasoning_start", id: ev.id };
    case "reasoning_delta":
      return { type: "reasoning_delta", id: ev.id, text: ev.text };
    case "reasoning_end":
      return { type: "reasoning_end", id: ev.id };
    case "tool_input_start":
      return { type: "tool_input_start", id: ev.id, toolName: ev.toolName };
    case "tool_input_delta":
      return { type: "tool_input_delta", id: ev.id, delta: ev.delta };
    case "tool_input_end":
      return { type: "tool_input_end", id: ev.id };
    case "tool_call":
      return {
        type: "tool_call",
        toolCall: { id: ev.toolCallId, name: ev.toolName, input: ev.input },
      };
    case "finish":
      return { type: "finish", finishReason: ev.finishReason, usage: ev.usage };
    default:
      return { type: ev.type };
  }
}

function serializeAssemblerEvent(ev: any): DecodedEvent {
  switch (ev.type) {
    case "tool_input_start":
      return { type: "tool_input_start", id: ev.id, toolName: ev.toolName };
    case "tool_input_delta":
      return { type: "tool_input_delta", id: ev.id, delta: ev.delta };
    case "tool_input_end":
      return { type: "tool_input_end", id: ev.id };
    case "tool_call":
      return {
        type: "tool_call",
        toolCallId: ev.toolCall.id,
        toolName: ev.toolCall.name,
        input: ev.toolCall.input,
      };
    default:
      return ev;
  }
}

const assemblerVectors = sseVectors
  .filter((v) => v.events && v.events.length > 0)
  .map((v) => {
    const assembler = new StreamingToolCallAssembler({});
    const out: DecodedEvent[] = [];
    for (const decoded of v.events!) {
      const events = assembler.handle(makeAssemblerEvent(decoded));
      for (const ev of events) out.push(serializeAssemblerEvent(ev));
    }
    return {
      name: v.name,
      input: v.events,
      output: out,
      snapshot: assembler.snapshotNormalizedToolCalls(),
    };
  });

// ---------------------------------------------------------------------------
// 3. Failure classifier vectors. Build errors that mirror what the transport
//    layer produces (APICallError with statusCode/responseBody/responseHeaders,
//    plus provider business errors and network/timeout codes).
// ---------------------------------------------------------------------------

interface FailureInput {
  name: string;
  /** descriptor consumed by the Go test to build the same error */
  error:
    | {
        kind: "apiCall";
        statusCode?: number;
        code?: string;
        message?: string;
        responseBody?: string;
        responseHeaders?: Record<string, string>;
        isRetryable?: boolean;
        data?: unknown;
      }
    | {
        kind: "plain";
        name?: string;
        code?: string;
        message?: string;
        responseHeaders?: Record<string, string>;
        isRetryable?: boolean;
      }
    | {
        kind: "cause";
        outer: {
          kind: "apiCall";
          statusCode?: number;
          message?: string;
          responseHeaders?: Record<string, string>;
        };
        code?: string;
        name?: string;
        message?: string;
      }
    | { kind: "idleTimeout" };
  abortSignal?: boolean;
}

function buildError(input: FailureInput["error"]): unknown {
  switch (input.kind) {
    case "apiCall":
      return new APICallError({
        message: input.message ?? "api call failed",
        url: "http://127.0.0.1/v1/chat/completions",
        requestBodyValues: {},
        statusCode: input.statusCode,
        responseBody: input.responseBody,
        responseHeaders: input.responseHeaders,
        isRetryable: input.isRetryable,
        data: input.data,
      });
    case "plain": {
      const e: any = new Error(input.message ?? "plain error");
      if (input.name) e.name = input.name;
      if (input.code) e.code = input.code;
      if (input.responseHeaders) e.responseHeaders = input.responseHeaders;
      if (input.isRetryable !== undefined) e.isRetryable = input.isRetryable;
      return e;
    }
    case "cause": {
      const inner = new APICallError({
        message: input.outer.message ?? "inner",
        url: "http://127.0.0.1/v1/chat/completions",
        requestBodyValues: {},
        statusCode: input.outer.statusCode,
        responseHeaders: input.outer.responseHeaders,
      });
      const e: any = new Error(input.message ?? "outer");
      if (input.name) e.name = input.name;
      if (input.code) e.code = input.code;
      e.cause = inner;
      return e;
    }
    case "idleTimeout": {
      const e: any = new Error("Model stream stalled: no event received for 600000ms.");
      e.name = "ModelStreamIdleTimeoutError";
      e.code = "MODEL_STREAM_IDLE_TIMEOUT";
      e.idleMs = 600000;
      e.timeoutMs = 600000;
      return e;
    }
  }
}

const jsonBody = (obj: unknown) => JSON.stringify(obj);

const failureInputs: FailureInput[] = [
  {
    name: "429-rate-limited",
    error: {
      kind: "apiCall",
      statusCode: 429,
      responseBody: jsonBody({ error: { message: "slow down" } }),
    },
  },
  {
    name: "429-retry-after-seconds",
    error: { kind: "apiCall", statusCode: 429, responseHeaders: { "retry-after": "2" } },
  },
  {
    name: "429-retry-after-ms",
    error: { kind: "apiCall", statusCode: 429, responseHeaders: { "retry-after-ms": "1500" } },
  },
  {
    name: "429-x-should-retry-false",
    error: {
      kind: "apiCall",
      statusCode: 429,
      responseHeaders: { "retry-after": "30", "x-should-retry": "false" },
    },
  },
  { name: "500-server-error", error: { kind: "apiCall", statusCode: 500 } },
  { name: "502-bad-gateway", error: { kind: "apiCall", statusCode: 502 } },
  { name: "503-unavailable", error: { kind: "apiCall", statusCode: 503 } },
  { name: "529-overloaded", error: { kind: "apiCall", statusCode: 529 } },
  { name: "401-unauthorized", error: { kind: "apiCall", statusCode: 401 } },
  { name: "403-forbidden", error: { kind: "apiCall", statusCode: 403 } },
  { name: "404-not-found", error: { kind: "apiCall", statusCode: 404 } },
  { name: "400-bad-request", error: { kind: "apiCall", statusCode: 400 } },
  { name: "422-unprocessable", error: { kind: "apiCall", statusCode: 422 } },
  {
    name: "400-context-code",
    error: {
      kind: "apiCall",
      statusCode: 400,
      responseBody: jsonBody({ error: { code: "context_length_exceeded", message: "too long" } }),
    },
  },
  {
    name: "400-context-message",
    error: {
      kind: "apiCall",
      statusCode: 400,
      responseBody: jsonBody({
        error: {
          message:
            "This model's maximum context length is 128000 tokens, however you requested 200000",
        },
      }),
    },
  },
  {
    name: "400-prompt-too-long",
    error: {
      kind: "apiCall",
      statusCode: 400,
      responseBody: jsonBody({ error: { message: "prompt is too long" } }),
    },
  },
  {
    name: "403-business-3007",
    error: {
      kind: "apiCall",
      statusCode: 403,
      responseBody: jsonBody({ error: { code: "3007", message: "plan required" } }),
    },
  },
  {
    name: "200-business-code-body",
    error: {
      kind: "apiCall",
      statusCode: 200,
      responseBody: jsonBody({ providerCode: "3002", message: "concurrency" }),
    },
  },
  {
    name: "429-terminal-business-1304",
    error: {
      kind: "apiCall",
      statusCode: 429,
      responseBody: jsonBody({ code: "1304", message: "quota" }),
    },
  },
  {
    name: "429-retryable-business-1302",
    error: {
      kind: "apiCall",
      statusCode: 429,
      responseBody: jsonBody({ code: "1302", message: "busy" }),
    },
  },
  {
    name: "400-context-business-1261",
    error: {
      kind: "apiCall",
      statusCode: 400,
      responseBody: jsonBody({ code: "1261", message: "context" }),
    },
  },
  {
    name: "500-business-500",
    error: { kind: "apiCall", statusCode: 500, responseBody: jsonBody({ code: "500" }) },
  },
  {
    name: "404-business-3006",
    error: { kind: "apiCall", statusCode: 404, responseBody: jsonBody({ code: "3006" }) },
  },
  {
    name: "403-business-1006",
    error: { kind: "apiCall", statusCode: 403, responseBody: jsonBody({ code: "1006" }) },
  },
  {
    name: "insufficient-quota",
    error: {
      kind: "apiCall",
      statusCode: 429,
      data: { error: { code: "insufficient_quota", message: "out of quota" } },
    },
  },
  {
    name: "organization-spend-limit",
    error: {
      kind: "apiCall",
      statusCode: 429,
      data: { error: { code: "organization_spend_limit_exceeded", type: "insufficient_quota" } },
    },
  },
  {
    name: "overloaded-error-type",
    error: {
      kind: "apiCall",
      statusCode: 500,
      data: { error: { type: "overloaded_error", message: "overloaded" } },
    },
  },
  {
    name: "rate-limit-error-type",
    error: {
      kind: "apiCall",
      statusCode: 429,
      data: { error: { type: "rate_limit_error", code: "rate_limit_reached_error" } },
    },
  },
  {
    name: "network-econnreset",
    error: { kind: "plain", code: "ECONNRESET", message: "socket hang up" },
  },
  {
    name: "network-econnrefused",
    error: { kind: "plain", code: "ECONNREFUSED", message: "connect refused" },
  },
  {
    name: "network-und-err-socket",
    error: { kind: "plain", code: "UND_ERR_SOCKET", message: "socket" },
  },
  { name: "timeout-etimedout", error: { kind: "plain", code: "ETIMEDOUT", message: "timeout" } },
  {
    name: "timeout-und-headers",
    error: { kind: "plain", code: "UND_ERR_HEADERS_TIMEOUT", message: "headers timeout" },
  },
  { name: "408-status", error: { kind: "apiCall", statusCode: 408 } },
  { name: "abort-error", error: { kind: "plain", name: "AbortError", message: "aborted" } },
  { name: "abort-signal", error: { kind: "plain", message: "whatever" }, abortSignal: true },
  { name: "idle-timeout", error: { kind: "idleTimeout" } },
  { name: "tls-cert", error: { kind: "plain", code: "CERT_HAS_EXPIRED", message: "cert" } },
  {
    name: "tls-unable-verify",
    error: { kind: "plain", code: "UNABLE_TO_VERIFY_LEAF_SIGNATURE", message: "tls" },
  },
  {
    name: "proxy-econn",
    error: { kind: "plain", code: "ERR_PROXY_CONNECTION_FAILED", message: "proxy" },
  },
  {
    name: "cause-network",
    error: { kind: "cause", outer: { kind: "apiCall", statusCode: 502 }, code: "ECONNRESET" },
  },
  { name: "retryable-flag-true", error: { kind: "apiCall", statusCode: 418, isRetryable: true } },
  { name: "retryable-flag-false", error: { kind: "apiCall", statusCode: 418, isRetryable: false } },
  { name: "unknown-plain", error: { kind: "plain", message: "something odd" } },
  {
    name: "invalid-response-code",
    error: { kind: "plain", code: "invalid_model_response", message: "bad response" },
  },
  {
    name: "invalid-request-code",
    error: { kind: "plain", code: "invalid_model_request", message: "bad request" },
  },
  {
    name: "401-business-1006-body",
    error: { kind: "apiCall", statusCode: 401, responseBody: jsonBody({ providerCode: "1006" }) },
  },
  {
    name: "400-invalid-request-code-body",
    error: { kind: "apiCall", statusCode: 400, responseBody: jsonBody({ error_code: "3001" }) },
  },
];

const failureVectors = failureInputs.map((input) => {
  const error = buildError(input.error);
  const signal = input.abortSignal ? { aborted: true } : undefined;
  // The real classifier takes an AbortSignal; emulate with a minimal stub.
  const abortSignal = signal ? ({ aborted: true } as AbortSignal) : undefined;
  const failure = classifyModelFailure(error, abortSignal);
  return {
    name: input.name,
    error: input.error,
    abortSignal: input.abortSignal ?? false,
    result: {
      code: failure.code,
      message: failure.message,
      reason: failure.reason,
      retryReason: failure.retryReason,
      retryable: failure.retryable,
      retryAfterMs: failure.retryAfterMs ?? null,
      statusCode: failure.statusCode ?? null,
    },
  };
});

// ---------------------------------------------------------------------------
// 4. Retry-delay vectors (delay curve without jitter, so deterministic).
// ---------------------------------------------------------------------------

const retryDelayVectors = [
  {
    attempt: 1,
    baseDelayMs: 2000,
    backoffFactor: 2,
    maxDelayMs: 60000,
    jitter: false,
    retryAfterMs: null,
  },
  {
    attempt: 2,
    baseDelayMs: 2000,
    backoffFactor: 2,
    maxDelayMs: 60000,
    jitter: false,
    retryAfterMs: null,
  },
  {
    attempt: 3,
    baseDelayMs: 2000,
    backoffFactor: 2,
    maxDelayMs: 60000,
    jitter: false,
    retryAfterMs: null,
  },
  {
    attempt: 10,
    baseDelayMs: 2000,
    backoffFactor: 2,
    maxDelayMs: 60000,
    jitter: false,
    retryAfterMs: null,
  },
  {
    attempt: 1,
    baseDelayMs: 2000,
    backoffFactor: 2,
    maxDelayMs: 60000,
    jitter: false,
    retryAfterMs: 0,
  },
  {
    attempt: 1,
    baseDelayMs: 2000,
    backoffFactor: 2,
    maxDelayMs: 60000,
    jitter: false,
    retryAfterMs: 1500,
  },
  {
    attempt: 1,
    baseDelayMs: 2000,
    backoffFactor: 2,
    maxDelayMs: 60000,
    jitter: false,
    retryAfterMs: 600000,
  },
  {
    attempt: 1,
    baseDelayMs: 2000,
    backoffFactor: 2,
    maxDelayMs: 60000,
    jitter: false,
    retryAfterMs: 300001,
  },
  {
    attempt: 1,
    baseDelayMs: 0,
    backoffFactor: 2,
    maxDelayMs: 60000,
    jitter: false,
    retryAfterMs: null,
  },
  {
    attempt: 1,
    baseDelayMs: 2000,
    backoffFactor: 2,
    maxDelayMs: 0,
    jitter: false,
    retryAfterMs: null,
  },
];

import { calculateRetryDelay } from "../../apps/nex-cli/packages/adapters/src/model/runner-retry.ts";
const retryDelayResults = retryDelayVectors.map((v) => ({
  ...v,
  delayMs: calculateRetryDelay(
    {
      maxAttempts: 11,
      baseDelayMs: v.baseDelayMs,
      backoffFactor: v.backoffFactor,
      maxDelayMs: v.maxDelayMs,
      jitter: v.jitter,
    },
    v.attempt,
    v.retryAfterMs ?? undefined,
  ),
}));

// ---------------------------------------------------------------------------
// 5. Retry-option resolution vectors.
// ---------------------------------------------------------------------------

import { resolveAiSdkModelRetryOptions } from "../../apps/nex-cli/packages/adapters/src/model/retry-policy.ts";
const retryOptionVectors = [
  { options: undefined, env: {} },
  { options: {}, env: {} },
  { options: { maxAttempts: 3 }, env: {} },
  { options: undefined, env: { NEX_MODEL_RETRY_MAX_RETRIES: "5" } },
  { options: undefined, env: { NEX_MODEL_RETRY_MAX_RETRIES: "0" } },
  {
    options: undefined,
    env: { NEX_MODEL_RETRY_BASE_DELAY_MS: "500", NEX_MODEL_RETRY_MAX_DELAY_MS: "4000" },
  },
  { options: undefined, env: { NEX_MODEL_RETRY_BACKOFF_FACTOR: "3" } },
  { options: { baseDelayMs: 100 }, env: { NEX_MODEL_RETRY_BASE_DELAY_MS: "500" } },
  { options: { jitter: false }, env: {} },
  { options: undefined, env: { NEX_MODEL_RETRY_MAX_RETRIES: "-1" } },
  { options: { backoffFactor: 0 }, env: {} },
  { options: { maxAttempts: 0 }, env: {} },
];
const retryOptionResults = retryOptionVectors.map((v) => ({
  ...v,
  resolved: resolveAiSdkModelRetryOptions(v.options as any, v.env as any),
}));

// ---------------------------------------------------------------------------
// 6. Idle-timeout vectors.
// ---------------------------------------------------------------------------
import { resolveModelStreamIdleTimeoutMs } from "../../apps/nex-cli/packages/adapters/src/model/stream-idle-timeout.ts";
const idleTimeoutVectors = [
  {},
  { baseTimeoutMs: 600000 },
  { baseTimeoutMs: 600000, retryNumber: 1 },
  { baseTimeoutMs: 600000, retryNumber: 3 },
  { baseTimeoutMs: 0 },
  { baseTimeoutMs: -5 },
  { retryNumber: 2 },
];
const idleTimeoutResults = idleTimeoutVectors.map((v) => ({
  ...v,
  resolved: resolveModelStreamIdleTimeoutMs(v as any),
}));

// ---------------------------------------------------------------------------
// Write.
// ---------------------------------------------------------------------------
writeFileSync(join(VECTORS, "model-sse.json"), JSON.stringify(sseVectors, null, 1) + "\n");
writeFileSync(
  join(VECTORS, "model-assembler.json"),
  JSON.stringify(assemblerVectors, null, 1) + "\n",
);
writeFileSync(join(VECTORS, "model-failure.json"), JSON.stringify(failureVectors, null, 1) + "\n");
writeFileSync(
  join(VECTORS, "model-retry.json"),
  JSON.stringify(
    { delays: retryDelayResults, options: retryOptionResults, idle: idleTimeoutResults },
    null,
    1,
  ) + "\n",
);
console.log(
  `sse: ${sseVectors.length} (${sseVectors.filter((v) => v.terminalError).length} terminal errors); assembler: ${assemblerVectors.length}; failure: ${failureVectors.length}; retry: ${retryDelayResults.length + retryOptionResults.length + idleTimeoutResults.length}`,
);
