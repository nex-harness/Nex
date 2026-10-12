// 场景驱动的 OpenAI chat-completions 流式 mock，供录制器和 Go 对照测试共用。
// 与 apps/nex-cli/packages/bootstrap/test/e2e/mock-model.mjs 的区别：后者是写死的三步脚本；
// 这里的每个请求按"场景脚本"逐轮回放，并把每个入站请求体写进 JSONL，供断言和对照。
//
// 场景 = 一个 steps 数组，第 N 个模型请求取 steps[N]（越界取最后一个，便于无限轮次）。
// step 形态：
//   { text: "..." }                                  只回文本
//   { toolCalls: [{ id, name, args }], text?: "" }   回工具调用（可带前置文本）
//   { error: { status: 429, body?: "...", headers?: {} } }   返回 HTTP 错误
//   { dropAfterChunks: N }                           发 N 个 chunk 后直接断开连接
//   { delayMs: N, ...其它 }                          响应前等待
import http from "node:http";
import { appendFileSync, writeFileSync } from "node:fs";

const FIXED_CREATED = 1_700_000_000; // 固定时间戳，保证两次录制逐字节一致

function sseChunk(delta, finish, extra = {}) {
  return `data: ${JSON.stringify({
    id: "chatcmpl-mock",
    object: "chat.completion.chunk",
    created: FIXED_CREATED,
    model: "mock-model",
    choices: [{ index: 0, delta, finish_reason: finish ?? null }],
    ...extra,
  })}\n\n`;
}

function renderStep(step) {
  const chunks = [];
  if (step.text) {
    // 把文本切成固定大小的块，让"流式增量"路径被真实走到。
    const size = step.chunkSize ?? 8;
    for (let i = 0; i < step.text.length; i += size) {
      chunks.push(
        sseChunk({
          ...(i === 0 ? { role: "assistant" } : {}),
          content: step.text.slice(i, i + size),
        }),
      );
    }
  }
  (step.toolCalls ?? []).forEach((call, index) => {
    const args = typeof call.args === "string" ? call.args : JSON.stringify(call.args ?? {});
    // 工具参数拆成两段，覆盖"增量 JSON 拼装"。
    const mid = Math.max(1, Math.floor(args.length / 2));
    chunks.push(
      sseChunk({
        role: "assistant",
        tool_calls: [
          {
            index,
            id: call.id,
            type: "function",
            function: { name: call.name, arguments: args.slice(0, mid) },
          },
        ],
      }),
    );
    chunks.push(sseChunk({ tool_calls: [{ index, function: { arguments: args.slice(mid) } }] }));
  });
  const usage = {
    prompt_tokens: step.promptTokens ?? 100,
    completion_tokens: step.completionTokens ?? 20,
    total_tokens: (step.promptTokens ?? 100) + (step.completionTokens ?? 20),
  };
  chunks.push(sseChunk({}, step.toolCalls?.length ? "tool_calls" : "stop", { usage }));
  chunks.push("data: [DONE]\n\n");
  return chunks;
}

function nonStreamBody(step) {
  const message = { role: "assistant", content: step.text ?? null };
  if (step.toolCalls?.length) {
    message.tool_calls = step.toolCalls.map((c) => ({
      id: c.id,
      type: "function",
      function: {
        name: c.name,
        arguments: typeof c.args === "string" ? c.args : JSON.stringify(c.args ?? {}),
      },
    }));
  }
  return {
    id: "chatcmpl-mock",
    object: "chat.completion",
    created: FIXED_CREATED,
    model: "mock-model",
    choices: [{ index: 0, message, finish_reason: step.toolCalls?.length ? "tool_calls" : "stop" }],
    usage: { prompt_tokens: 100, completion_tokens: 20, total_tokens: 120 },
  };
}

export function startMockModel({
  steps,
  port = 0,
  logFile,
  host = "127.0.0.1",
  bodyDir,
  onRequest,
}) {
  if (logFile) writeFileSync(logFile, "");
  let requestIndex = 0;
  const server = http.createServer((req, res) => {
    const parts = [];
    req.on("data", (d) => parts.push(d));
    req.on("end", async () => {
      if (!req.url.includes("/chat/completions")) {
        res.writeHead(404).end("{}");
        return;
      }
      let json = {};
      try {
        json = JSON.parse(Buffer.concat(parts).toString("utf8"));
      } catch {
        /* 记录原样 */
      }
      const index = requestIndex++;
      if (onRequest) onRequest(json);
      if (bodyDir)
        writeFileSync(
          `${bodyDir}/request-${index}.json`,
          JSON.stringify({ url: req.url, headers: req.headers, body: json }, null, 1),
        );
      const step = steps[Math.min(index, steps.length - 1)];
      if (logFile) {
        const toolResults = (json.messages ?? []).filter((m) => m.role === "tool");
        appendFileSync(
          logFile,
          JSON.stringify({
            index,
            stream: json.stream === true,
            model: json.model,
            tools: (json.tools ?? []).map((t) => t.function?.name ?? t.name),
            messageRoles: (json.messages ?? []).map((m) => m.role),
            toolResultCount: toolResults.length,
            lastUserText:
              [...(json.messages ?? [])].reverse().find((m) => m.role === "user")?.content ?? null,
            systemPromptBytes: Buffer.byteLength(
              JSON.stringify((json.messages ?? []).filter((m) => m.role === "system")),
            ),
          }) + "\n",
        );
      }
      if (step.delayMs) await new Promise((r) => setTimeout(r, step.delayMs));
      if (step.error) {
        res.writeHead(step.error.status, {
          "content-type": "application/json",
          ...(step.error.headers ?? {}),
        });
        res.end(
          step.error.body ??
            JSON.stringify({ error: { message: `mock error ${step.error.status}`, type: "mock" } }),
        );
        return;
      }
      if (json.stream !== true) {
        res.writeHead(200, { "content-type": "application/json" });
        res.end(JSON.stringify(nonStreamBody(step)));
        return;
      }
      res.writeHead(200, { "content-type": "text/event-stream", "cache-control": "no-cache" });
      const chunks = renderStep(step);
      const limit = step.dropAfterChunks ?? chunks.length;
      for (let i = 0; i < Math.min(limit, chunks.length); i++) {
        res.write(chunks[i]);
        await new Promise((r) => setImmediate(r));
      }
      if (step.dropAfterChunks !== undefined) {
        res.destroy();
        return;
      }
      res.end();
    });
  });
  return new Promise((resolve) =>
    server.listen(port, host, () =>
      resolve({
        port: server.address().port,
        requestCount: () => requestIndex,
        close: () =>
          new Promise((r) => {
            server.closeAllConnections?.();
            server.close(r);
          }),
      }),
    ),
  );
}
