/* eslint-disable max-lines -- 向量生成脚本：用真实的 @anthropic-ai/sdk 驱动同一批 SSE 字节，
// 顺序产出全部用例与其期望事件，拆开会让用例与期望值分居两个文件。 */
//
// 产出三类产物：
//   1. go/testdata/sse/anthropic/*.txt —— 自编 SSE 原始字节，供 Go 测试直接重放。
//   2. go/testdata/vectors/model-anthropic.json —— 每条 fixture 的 modelclient 事件序列
//      （由真实 SDK 的 MessageStream 累积出的 finalMessage 推导）与分片解码结果。
//   3. 每个用例的原始 SDK 事件（raw），Go 侧不必再跑一次 TS 就能对照 type 分支。
//
// 依赖真实包而不是手写期望值，是为了让 Go 分支与 SDK 的 type 语义保持一致；
// 事件映射（MessageStreamEvent -> modelclient.Event）由真实 finalMessage 推导，
// Go 侧 decoder.go 必须逐字段复现。
import { mkdirSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import Anthropic from "@anthropic-ai/sdk";

const require = createRequire(import.meta.url);
void require; // 保持 createRequire 可用；SDK 以 default export 引入。

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO = resolve(HERE, "../..");
const VECTORS = join(REPO, "go/testdata/vectors");
const SSE_DIR = join(REPO, "go/testdata/sse/anthropic");
mkdirSync(VECTORS, { recursive: true });
mkdirSync(SSE_DIR, { recursive: true });

// ---------------------------------------------------------------------------
// 1. Fixture 原始字节。每条都带 `event:` 头，除了 data-only 那条 —— 代理可能省略
//    `event:`（spike R4），那是必须覆盖的分支。
// ---------------------------------------------------------------------------

interface Fixture {
  name: string;
  /** 真实 provider 会写出的原始 SSE 字节。 */
  text: string;
  /** 用例意图，写进向量文件便于 Go 侧报错时对照。 */
  notes: string;
}

const ev = (event: string, data: object): string =>
  `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`;
const dataOnly = (data: object): string => `data: ${JSON.stringify(data)}\n\n`;

const messageStart = (usage: object = { input_tokens: 25, output_tokens: 1 }) =>
  ev("message_start", {
    type: "message_start",
    message: {
      id: "msg_fixture",
      type: "message",
      role: "assistant",
      model: "claude-mock",
      content: [],
      stop_reason: null,
      stop_sequence: null,
      usage,
    },
  });

const textBlock = (text: string) => [
  ev("content_block_start", {
    type: "content_block_start",
    index: 0,
    content_block: { type: "text", text: "" },
  }),
  ...Array.from(text).map((_, i) =>
    // 单字符切分：text_delta 的最小粒度。
    ev("content_block_delta", {
      type: "content_block_delta",
      index: 0,
      delta: { type: "text_delta", text: Array.from(text)[i] },
    }),
  ),
  ev("content_block_stop", { type: "content_block_stop", index: 0 }),
];

const toolBlock = (index: number, id: string, name: string, argChunks: string[]) => [
  ev("content_block_start", {
    type: "content_block_start",
    index,
    content_block: { type: "tool_use", id, name, input: {} },
  }),
  ...argChunks.map((partial) =>
    ev("content_block_delta", {
      type: "content_block_delta",
      index,
      delta: { type: "input_json_delta", partial_json: partial },
    }),
  ),
  ev("content_block_stop", { type: "content_block_stop", index }),
];

const messageDelta = (stopReason: string, usage: object) =>
  ev("message_delta", {
    type: "message_delta",
    delta: { stop_reason: stopReason, stop_sequence: null },
    usage,
  });

const messageStop = () => ev("message_stop", { type: "message_stop" });

const fixtures: Fixture[] = [
  {
    name: "text-only",
    notes: "单文本块，三个 text_delta，end_turn，usage 只在 message_delta 里出现。",
    text:
      messageStart({ input_tokens: 25, output_tokens: 1 }) +
      textBlock("Hi there").join("") +
      messageDelta("end_turn", { output_tokens: 5 }) +
      messageStop(),
  },
  {
    name: "thinking-then-text",
    notes: "thinking 块（含 signature_delta）后接文本块，max_tokens 截断。",
    text:
      messageStart({ input_tokens: 30, output_tokens: 1 }) +
      [
        ev("content_block_start", {
          type: "content_block_start",
          index: 0,
          content_block: { type: "thinking", thinking: "" },
        }),
        ev("content_block_delta", {
          type: "content_block_delta",
          index: 0,
          delta: { type: "thinking_delta", thinking: "let me" },
        }),
        ev("content_block_delta", {
          type: "content_block_delta",
          index: 0,
          delta: { type: "thinking_delta", thinking: " think" },
        }),
        ev("content_block_delta", {
          type: "content_block_delta",
          index: 0,
          delta: { type: "signature_delta", signature: "SIGABC" },
        }),
        ev("content_block_stop", { type: "content_block_stop", index: 0 }),
        ev("content_block_start", {
          type: "content_block_start",
          index: 1,
          content_block: { type: "text", text: "" },
        }),
        ev("content_block_delta", {
          type: "content_block_delta",
          index: 1,
          delta: { type: "text_delta", text: "Answer" },
        }),
        ev("content_block_stop", { type: "content_block_stop", index: 1 }),
      ].join("") +
      messageDelta("max_tokens", { output_tokens: 120 }) +
      messageStop(),
  },
  {
    name: "tool-call-split-args",
    notes: "单个 tool_use，参数拆成 12 个 input_json_delta（含空 delta）。",
    text:
      messageStart({ input_tokens: 20, output_tokens: 1 }) +
      toolBlock(0, "toolu_1", "Read", [
        "",
        "{",
        '"file',
        "_path",
        '":',
        '"/tmp',
        "/a b",
        '.txt"',
        "}",
        "",
        "",
        "",
      ]).join("") +
      messageDelta("tool_use", { output_tokens: 42 }) +
      messageStop(),
  },
  {
    name: "tool-call-multiple",
    notes: "两个 tool_use 块，索引不连续（0 与 2），中间夹一个文本块。",
    text:
      messageStart({ input_tokens: 40, output_tokens: 1 }) +
      toolBlock(0, "toolu_a", "Read", ['{"file_path":"/tmp/x"}']).join("") +
      ev("content_block_start", {
        type: "content_block_start",
        index: 1,
        content_block: { type: "text", text: "" },
      }) +
      ev("content_block_delta", {
        type: "content_block_delta",
        index: 1,
        delta: { type: "text_delta", text: "checking" },
      }) +
      ev("content_block_stop", { type: "content_block_stop", index: 1 }) +
      toolBlock(2, "toolu_b", "Write", ['{"content":"', "你好", '"}']).join("") +
      messageDelta("tool_use", { output_tokens: 88 }) +
      messageStop(),
  },
  {
    name: "usage-in-message-delta",
    notes:
      "message_start 给出 input_tokens，message_delta 给出累计 output_tokens；message_delta 同时带 input_tokens 时取后者。",
    text:
      messageStart({ input_tokens: 111, output_tokens: 1 }) +
      textBlock("x").join("") +
      messageDelta("end_turn", { input_tokens: 111, output_tokens: 7 }) +
      messageStop(),
  },
  {
    name: "error-event",
    notes: "HTTP 200 内联 error 事件（overloaded_error），message_stop 不会到达。",
    text:
      messageStart({ input_tokens: 25, output_tokens: 1 }) +
      ev("error", {
        type: "error",
        error: { type: "overloaded_error", message: "Overloaded" },
      }),
  },
  {
    name: "ping-and-comments",
    notes: "注释行 + 多个 ping 事件必须被忽略，不产生任何 modelclient 事件。",
    text:
      ": keep-alive\n" +
      messageStart({ input_tokens: 5, output_tokens: 1 }) +
      ev("ping", { type: "ping" }) +
      ev("ping", { type: "ping" }) +
      ": another comment\n" +
      textBlock("ok").join("") +
      ev("ping", { type: "ping" }) +
      messageDelta("end_turn", { output_tokens: 3 }) +
      messageStop(),
  },
  {
    name: "missing-event-header",
    notes:
      "spike R4：代理省略全部 `event:` 头。SDK 的 Stream.Next 会静默丢弃，本仓库按 JSON `type` 分支因此必须能解码。",
    text:
      dataOnly({
        type: "message_start",
        message: {
          id: "msg_noevent",
          type: "message",
          role: "assistant",
          model: "claude-mock",
          content: [],
          stop_reason: null,
          stop_sequence: null,
          usage: { input_tokens: 9, output_tokens: 1 },
        },
      }) +
      dataOnly({
        type: "content_block_start",
        index: 0,
        content_block: { type: "text", text: "" },
      }) +
      dataOnly({
        type: "content_block_delta",
        index: 0,
        delta: { type: "text_delta", text: "no event header" },
      }) +
      dataOnly({ type: "content_block_stop", index: 0 }) +
      dataOnly({
        type: "message_delta",
        delta: { stop_reason: "end_turn", stop_sequence: null },
        usage: { output_tokens: 6 },
      }) +
      dataOnly({ type: "message_stop" }),
  },
  {
    name: "utf8-split",
    notes: "多字节 UTF-8 出现在 delta 与工具参数里；Go 测试会在任意字节边界切分。",
    text:
      messageStart({ input_tokens: 12, output_tokens: 1 }) +
      ev("content_block_start", {
        type: "content_block_start",
        index: 0,
        content_block: { type: "text", text: "" },
      }) +
      ev("content_block_delta", {
        type: "content_block_delta",
        index: 0,
        delta: { type: "text_delta", text: "你" },
      }) +
      ev("content_block_delta", {
        type: "content_block_delta",
        index: 0,
        delta: { type: "text_delta", text: "好🙂" },
      }) +
      ev("content_block_stop", { type: "content_block_stop", index: 0 }) +
      toolBlock(1, "toolu_utf8", "Write", ['{"content":"', "héllo 🌏", '"}']).join("") +
      messageDelta("tool_use", { output_tokens: 30 }) +
      messageStop(),
  },
  {
    name: "crlf",
    notes: "全 CRLF 行尾 + CRLF 注释；事件字节与 LF 版本等价。",
    text: (
      messageStart({ input_tokens: 15, output_tokens: 1 }) +
      textBlock("crlf").join("") +
      messageDelta("end_turn", { output_tokens: 4 }) +
      messageStop()
    ).replaceAll("\n", "\r\n"),
  },
];

// ---------------------------------------------------------------------------
// 2. 用真实 @anthropic-ai/sdk 驱动这些字节，收集原始事件与累积后的 message。
//    TS 侧的期望值由 finalMessage（真实累积器）推导，Go 侧必须复现同一序列。
// ---------------------------------------------------------------------------

interface RawEvent {
  type: string;
  index?: number;
  contentBlockType?: string;
  toolId?: string;
  toolName?: string;
  deltaType?: string;
  text?: string;
  partialJson?: string;
  thinking?: string;
  signature?: string;
  stopReason?: string | null;
  inputTokens?: number;
  outputTokens?: number;
  errorType?: string;
  errorMessage?: string;
  messageId?: string;
}

interface RecordedEvent {
  type: string;
  id?: string;
  text?: string;
  toolName?: string;
  finishReason?: string;
  input?: unknown;
}

/** stop_reason -> modelclient 归一化 finish reason，对应 modelclient 的常量。 */
function safeParse(value: unknown): any {
  if (value === null || value === undefined) return null;
  if (typeof value === "object") return value;
  try {
    return JSON.parse(String(value));
  } catch {
    return null;
  }
}

function normalizeStopReason(stopReason: string | null | undefined): string | null {
  switch (stopReason) {
    case null:
    case undefined:
      return null;
    case "end_turn":
    case "stop_sequence":
    case "pause_turn":
      return "stop";
    case "max_tokens":
    case "model_context_window_exceeded":
      return "length";
    case "refusal":
      return "content-filter";
    case "tool_use":
      return "tool-calls";
    default:
      return "other";
  }
}

/**
 * 把真实 SDK 产出的原始事件流折叠成 modelclient 的事件序列。映射规则与
 * @ai-sdk/anthropic 的 doStream 一致（见 anthropic-messages-language-model.ts）：
 * text 块 -> text-delta、thinking 块 -> reasoning-delta、tool_use -> tool-input-delta。
 * 用它（而不是手写期望值）是为了让 Go 分支与真实 SDK 的 type 语义保持一致。
 */
function foldToModelClientEvents(rawEvents: RawEvent[]): RecordedEvent[] {
  const out: RecordedEvent[] = [];
  const blockToolId = new Map<number, string>();
  let stopReason: string | null = null;
  for (const ev of rawEvents) {
    switch (ev.type) {
      case "message_start":
        break;
      case "content_block_start":
        if (ev.contentBlockType === "tool_use" && ev.toolId) {
          out.push({ type: "tool_input_start", id: ev.toolId, toolName: ev.toolName ?? "" });
          if (ev.index !== undefined) blockToolId.set(ev.index, ev.toolId);
        }
        break;
      case "content_block_delta":
        if (ev.deltaType === "text_delta" && ev.text !== undefined) {
          out.push({ type: "text_delta", text: ev.text });
        } else if (ev.deltaType === "thinking_delta" && ev.thinking !== undefined) {
          out.push({ type: "reasoning_delta", text: ev.thinking });
        } else if (ev.deltaType === "input_json_delta") {
          // ai-sdk/anthropic 跳过空 delta（code-execution 首字符替换的处理）。
          if (ev.partialJson === "") break;
          out.push({
            type: "tool_input_delta",
            id: ev.index !== undefined ? blockToolId.get(ev.index) : undefined,
            text: ev.partialJson,
          });
        }
        break;
      case "content_block_stop":
        if (ev.index !== undefined && blockToolId.has(ev.index)) {
          out.push({ type: "tool_input_end", id: blockToolId.get(ev.index) });
          blockToolId.delete(ev.index);
        }
        break;
      case "message_delta":
        if (ev.stopReason) stopReason = normalizeStopReason(ev.stopReason);
        break;
      case "message_stop":
        break;
      case "error":
        out.push({ type: "error", text: `${ev.errorType}: ${ev.errorMessage}` });
        break;
      default:
        break;
    }
  }
  if (stopReason) out.push({ type: "finish", finishReason: stopReason });
  return out;
}

function toRawEvent(ev: any): RawEvent {
  const base: RawEvent = { type: ev.type };
  if (ev.type === "message_start") {
    base.messageId = ev.message?.id;
    base.inputTokens = ev.message?.usage?.input_tokens;
    base.outputTokens = ev.message?.usage?.output_tokens;
    return base;
  }
  if (ev.type === "content_block_start") {
    base.index = ev.index;
    base.contentBlockType = ev.content_block?.type;
    base.toolId = ev.content_block?.id;
    base.toolName = ev.content_block?.name;
    return base;
  }
  if (ev.type === "content_block_delta") {
    base.index = ev.index;
    base.deltaType = ev.delta?.type;
    base.text = ev.delta?.text;
    base.thinking = ev.delta?.thinking;
    base.partialJson = ev.delta?.partial_json;
    base.signature = ev.delta?.signature;
    return base;
  }
  if (ev.type === "content_block_stop") {
    base.index = ev.index;
    return base;
  }
  if (ev.type === "message_delta") {
    base.stopReason = ev.delta?.stop_reason ?? null;
    base.inputTokens = ev.usage?.input_tokens;
    base.outputTokens = ev.usage?.output_tokens;
    return base;
  }
  if (ev.type === "error") {
    base.errorType = ev.error?.type;
    base.errorMessage = ev.error?.message;
    return base;
  }
  return base;
}

/**
 * 把 fixture 自身每个 SSE data body 按 JSON `type` 折叠成 modelclient 事件序列。
 * 这与 Go decoder.go 的分支一一对应（spike R4：分支必须读 `type`，不是 `event:`）。
 * 对带 `event:` 头的 fixture，本函数的结果必须与真实 SDK 折叠出的
 * modelClientEvents 相同（见 assertSdkAgrees），否则视为映射写错。
 */
function foldBodiesByType(text: string): RecordedEvent[] {
  const out: RecordedEvent[] = [];
  let stopReason: string | null = null;
  // 每个 block 的 kind 由 content_block_start 决定，delta 是位置化的。
  const blockToolId = new Map<number, string>();
  for (const block of text.split("\n")) {
    if (!block.startsWith("data: ")) continue;
    let body: any;
    try {
      body = JSON.parse(block.slice(6));
    } catch {
      continue;
    }
    if (typeof body?.type !== "string") continue;
    switch (body.type) {
      case "message_start":
        break;
      case "content_block_start": {
        if (body.content_block?.type === "tool_use") {
          out.push({
            type: "tool_input_start",
            id: body.content_block.id,
            toolName: body.content_block.name,
          });
          blockToolId.set(body.index, body.content_block.id);
        }
        break;
      }
      case "content_block_delta": {
        const delta = body.delta ?? {};
        if (delta.type === "text_delta") {
          out.push({ type: "text_delta", text: delta.text });
        } else if (delta.type === "thinking_delta") {
          out.push({ type: "reasoning_delta", text: delta.thinking });
        } else if (delta.type === "input_json_delta") {
          if (delta.partial_json === "") break;
          out.push({
            type: "tool_input_delta",
            id: blockToolId.get(body.index),
            text: delta.partial_json,
          });
        }
        break;
      }
      case "content_block_stop": {
        if (blockToolId.has(body.index)) {
          out.push({ type: "tool_input_end", id: blockToolId.get(body.index) });
          blockToolId.delete(body.index);
        }
        break;
      }
      case "message_delta":
        if (body.delta?.stop_reason) stopReason = normalizeStopReason(body.delta.stop_reason);
        break;
      case "message_stop":
        break;
      case "error":
        out.push({ type: "error", text: `${body.error?.type}: ${body.error?.message}` });
        break;
      default:
        break;
    }
  }
  if (stopReason) out.push({ type: "finish", finishReason: stopReason });
  return out;
}

function clientWithFetch(fixtureText: string, chunkSize?: number) {
  const bytes = new TextEncoder().encode(fixtureText);
  const makeResponse = () => {
    if (!chunkSize) {
      return new Response(fixtureText, {
        status: 200,
        headers: { "content-type": "text/event-stream" },
      });
    }
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
    return new Response(stream, {
      status: 200,
      headers: { "content-type": "text/event-stream" },
    });
  };
  return new Anthropic({
    apiKey: "sk-ant-vector-fixture",
    maxRetries: 0,
    fetch: async () => makeResponse(),
  } as any);
}

async function recordFixture(fixture: Fixture) {
  const rawEvents: RawEvent[] = [];
  let streamError: { name: string; message: string; type: string } | null = null;
  // `client.messages.create({stream:true})` returns the raw event stream; the
  // cumulative usage/stop_reason semantics live on `finalMessage()`.
  const client = clientWithFetch(fixture.text);
  try {
    const message = await client.messages.create({
      model: "claude-mock",
      max_tokens: 256,
      stream: true,
      messages: [{ role: "user", content: "hi" }],
    });
    for await (const ev of message as any) {
      rawEvents.push(toRawEvent(ev));
    }
  } catch (error: any) {
    // The TS SDK aborts the stream on an `error` event (src/core/streaming.ts).
    // We keep that behaviour as a recorded fact and derive the error event from
    // the parsed error body, so the Go decoder is checked against a real SDK run.
    const parsed = safeParse(error?.error ?? error?.body ?? null);
    const inner = parsed?.error ?? parsed;
    rawEvents.push({
      type: "error",
      errorType: inner?.type ?? null,
      errorMessage: inner?.message ?? String(error?.message ?? error),
    });
    streamError = {
      name: error?.name ?? "APIError",
      message: inner?.message ?? String(error?.message ?? error),
      type: inner?.type ?? "api_error",
    };
  }

  let usage = { inputTokens: null as number | null, outputTokens: null as number | null };
  let stopReason: string | null = null;
  let sdkDroppedEvents = false;
  if (!streamError) {
    if (rawEvents.length === 0) {
      // spike R4 的直接证据：TS SDK 的 Stream.Next 只对白名单里的 `event:` 名字
      // 产出事件，没有 `event:` 头就整条流为空。Go 侧按 JSON `type` 分支，因此
      // 这条 fixture 的 Go 结果必须比 SDK 更多，而不是相同。
      sdkDroppedEvents = true;
    } else {
      const final = await client.messages
        .stream({
          model: "claude-mock",
          max_tokens: 256,
          messages: [{ role: "user", content: "hi" }],
        })
        .finalMessage();
      usage = {
        inputTokens: final.usage?.input_tokens ?? null,
        outputTokens: final.usage?.output_tokens ?? null,
      };
      stopReason = normalizeStopReason(final.stop_reason ?? null);
    }
  }

  // 分片解码：同一批字节按 1/2/3 字节喂入，事件序列必须与整读一致。
  const splits: Record<string, RawEvent[]> = {};
  for (const size of [1, 2, 3]) {
    const splitClient = clientWithFetch(fixture.text, size);
    const events: RawEvent[] = [];
    try {
      const msg = await splitClient.messages.create({
        model: "claude-mock",
        max_tokens: 256,
        stream: true,
        messages: [{ role: "user", content: "hi" }],
      });
      for await (const ev of msg as any) {
        events.push(toRawEvent(ev));
      }
    } catch {
      // 与整读同样在 error 事件处中止；Go 侧对分片不做额外断言。
    }
    splits[String(size)] = events;
  }

  const byType = foldBodiesByType(fixture.text);
  const fromSdk = foldToModelClientEvents(rawEvents);
  // R4 的对照：带 `event:` 头时 SDK 折叠结果必须与按 JSON `type` 折叠结果一致。
  // 一旦不一致，说明本脚本的映射写错了，而不是 SDK 有问题。
  if (!sdkDroppedEvents) {
    const a = JSON.stringify(fromSdk);
    const b = JSON.stringify(byType);
    if (a !== b) {
      throw new Error(`fixture ${fixture.name}: SDK fold != by-type fold\n${a}\n${b}`);
    }
  }

  return {
    name: fixture.name,
    notes: fixture.notes,
    text: fixture.text,
    rawEvents,
    modelClientEvents: byType,
    usage,
    stopReason,
    streamError,
    sdkDroppedEvents,
    split: splits,
  };
}

const vectors = [];
for (const fixture of fixtures) {
  vectors.push(await recordFixture(fixture));
  writeFileSync(join(SSE_DIR, `${fixture.name}.txt`), fixture.text);
}

writeFileSync(join(VECTORS, "model-anthropic.json"), JSON.stringify(vectors, null, 1) + "\n");
console.log(
  `anthropic vectors: ${vectors.length} fixtures, ${vectors.reduce((n, v) => n + v.rawEvents.length, 0)} raw events, ${vectors.reduce((n, v) => n + v.modelClientEvents.length, 0)} modelclient events`,
);
