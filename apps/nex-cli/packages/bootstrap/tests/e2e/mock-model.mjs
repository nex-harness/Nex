// Scripted OpenAI-compatible chat-completions server for the ToolSearch + Codemode e2e.
// It plays a fixed three-step turn (ToolSearch -> Codemode -> final answer) and logs, per
// request, which tools were declared and how many tool results were already in history, so a
// run can prove: deferred schemas absent first, present after ToolSearch, and no intermediate
// MCP result bodies in the model context.
//
// Usage: see README.md in this directory.
import http from "node:http";
import { appendFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const LOG = process.env.MOCK_MODEL_LOG ?? join(tmpdir(), "nex-mock-model-requests.jsonl");
const PORT = Number(process.env.MOCK_MODEL_PORT ?? 18999);
writeFileSync(LOG, "");
const CODE = `
const name = Object.keys(tools).find((n) => n.endsWith("read_issue"));
const issues = await Promise.all([1, 2, 3].map((n) => tools[name]({ number: n })));
return issues.map((i) => JSON.parse(i.content[0].text).title).join(", ");
`;

function chunk(delta, finish) {
  return `data: ${JSON.stringify({
    id: "chatcmpl-mock", object: "chat.completion.chunk", created: 1, model: "mock",
    choices: [{ index: 0, delta, finish_reason: finish ?? null }],
  })}\n\n`;
}
function toolCall(id, name, args) {
  return chunk({ role: "assistant", tool_calls: [{ index: 0, id, type: "function", function: { name, arguments: JSON.stringify(args) } }] })
    + chunk({}, "tool_calls");
}
function text(t) { return chunk({ role: "assistant", content: t }) + chunk({}, "stop"); }

http.createServer((req, res) => {
  let body = "";
  req.on("data", (d) => (body += d));
  req.on("end", () => {
    if (!req.url.includes("/chat/completions")) { res.writeHead(404).end(); return; }
    const json = JSON.parse(body);
    const tools = (json.tools ?? []).map((t) => t.function?.name ?? t.name);
    const toolResults = (json.messages ?? []).filter((m) => m.role === "tool");
    appendFileSync(LOG, JSON.stringify({ tools, toolResultCount: toolResults.length, stream: json.stream === true,
      toolResultPreview: toolResults.map((m) => String(typeof m.content === "string" ? m.content : JSON.stringify(m.content)).slice(0, 400)) }) + "\n");
    let out;
    if (tools.length === 0) out = text("ok");
    else if (toolResults.length === 0) out = toolCall("call_search", "ToolSearch", { query: "read github issue" });
    else if (toolResults.length === 1) out = toolCall("call_code", "Codemode", { code: CODE });
    else out = text("DONE: " + toolResults.at(-1).content);
    if (json.stream) {
      res.writeHead(200, { "content-type": "text/event-stream" });
      res.end(out + "data: [DONE]\n\n");
    } else {
      res.writeHead(200, { "content-type": "application/json" });
      res.end(JSON.stringify({ id: "x", object: "chat.completion", choices: [{ index: 0, message: { role: "assistant", content: "ok" }, finish_reason: "stop" }] }));
    }
  });
}).listen(PORT, "127.0.0.1", () => console.log(`mock model on ${PORT}, log ${LOG}`));
