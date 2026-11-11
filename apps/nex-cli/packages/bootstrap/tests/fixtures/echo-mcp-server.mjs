// 真实 stdio MCP server：给 ToolSearch / Codemode 集成测试提供可被 adapter 连接的工具。
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import { z } from "zod";

const server = new McpServer({ name: "fixture", version: "1.0.0" });
server.tool("read_issue", "Read a GitHub issue by number", { number: z.number() }, async ({ number }) => ({
  content: [{ type: "text", text: JSON.stringify({ number, title: `Issue ${number}`, body: "x".repeat(2000) }) }],
}));
server.tool("post_message", "Post a chat message to a channel", { text: z.string() }, async ({ text }) => ({
  content: [{ type: "text", text: `posted:${text}` }],
}));
await server.connect(new StdioServerTransport());
