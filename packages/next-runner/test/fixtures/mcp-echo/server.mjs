#!/usr/bin/env node
// Minimal MCP stdio server used by the runner tests and the `mcp` E2E suite.
//
// Speaks newline-delimited JSON-RPC 2.0 over stdio (the MCP stdio transport) and
// exposes one tool, `echo`, which returns its `text` argument. No dependencies:
// the fixture must start on a bare checkout.
//
//   initialize → instructions (serverInfo name "nex-mcp-echo")
//   tools/list → [{ name: "echo", inputSchema: { text } }]
//   tools/call echo {text} → content [{type:"text", text}]
//
// Set MCP_ECHO_PREFIX to prepend a marker to every echoed text (proves the E2E
// talked to this process). RESOURCE_URI marks a resource-capable server: it
// advertises resources and lists one at that URI (the multi-runner E2E does the
// same through a server it starts itself).

import { createInterface } from "node:readline";

const PREFIX = process.env.MCP_ECHO_PREFIX ?? "";
const RESOURCE_URI = process.env.RESOURCE_URI ?? "";
const PROTOCOL_VERSION = "2025-06-18";

const send = (message) => {
  process.stdout.write(`${JSON.stringify(message)}\n`);
};
const reply = (id, result) => send({ jsonrpc: "2.0", id, result });
const fail = (id, code, message) => send({ jsonrpc: "2.0", id, error: { code, message } });
const log = (message) => process.stderr.write(`[mcp-echo] ${message}\n`);

const TOOLS = [
  {
    name: "echo",
    description: "Echo the given text back.",
    inputSchema: {
      type: "object",
      properties: { text: { type: "string", description: "Text to echo" } },
      required: ["text"],
    },
  },
];

const handlers = {
  initialize(params) {
    return {
      protocolVersion: params?.protocolVersion ?? PROTOCOL_VERSION,
      capabilities: {
        tools: { listChanged: false },
        ...(RESOURCE_URI ? { resources: { listChanged: false } } : {}),
      },
      serverInfo: { name: "nex-mcp-echo", version: "1.0.0" },
      // The first prompt's `mcp_servers` section lists this line.
      instructions: "Use the echo tool to repeat text.",
    };
  },
  "notifications/initialized"() {
    return undefined;
  },
  ping() {
    return {};
  },
  "tools/list"() {
    return { tools: TOOLS };
  },
  "resources/list"() {
    return RESOURCE_URI
      ? {
          resources: [
            {
              uri: RESOURCE_URI,
              name: "echo-note",
              description: "A note the echo server serves.",
              mimeType: "text/plain",
            },
          ],
        }
      : { resources: [] };
  },
  "resources/read"(params) {
    if (!RESOURCE_URI || params?.uri !== RESOURCE_URI) {
      throw Object.assign(new Error("resource not found"), { code: -32602 });
    }
    return {
      contents: [{ uri: RESOURCE_URI, mimeType: "text/plain", text: PREFIX || "echo note" }],
    };
  },
  "tools/call"(params) {
    if (params?.name !== "echo") {
      throw Object.assign(new Error(`unknown tool ${String(params?.name)}`), { code: -32602 });
    }
    const text = typeof params?.arguments?.text === "string" ? params.arguments.text : "";
    return { content: [{ type: "text", text: `${PREFIX}${text}` }] };
  },
  "resources/list"() {
    return { resources: [] };
  },
  "prompts/list"() {
    return { prompts: [] };
  },
};

log(`started (prefix ${JSON.stringify(PREFIX)})`);
const lines = createInterface({ input: process.stdin });
lines.on("line", (line) => {
  const trimmed = line.trim();
  if (!trimmed) return;
  let message;
  try {
    message = JSON.parse(trimmed);
  } catch {
    log(`dropping malformed line: ${trimmed.slice(0, 120)}`);
    return;
  }
  const { id, method, params } = message;
  const isNotification = id === undefined || id === null;
  const handler = handlers[method];
  if (!handler) {
    if (!isNotification) fail(id, -32601, `method not found: ${String(method)}`);
    return;
  }
  try {
    const result = handler(params);
    if (isNotification || result === undefined) return;
    reply(id, result);
  } catch (error) {
    if (isNotification) return;
    fail(id, error?.code ?? -32603, error?.message ?? String(error));
  }
});
lines.on("close", () => process.exit(0));
