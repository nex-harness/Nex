import { describe, expect, it } from "vitest";
import {
  normalizeWebRemoteControlWorkspaceName,
  resolveUnrecognizedWebRemoteControlRequest,
} from "../src/web-remote-relay/index.js";
import {
  parseWebRemoteControlAppPayload,
  webRemoteControlRendererActionRequestSchema,
  webRemoteControlRendererActionResultSchema,
} from "../src/validation.js";

describe("手机置顶/归档/新建项目 payload", () => {
  it.each([
    {
      nex_type: "task-pin-request",
      requestId: "r1",
      taskId: "t1",
      workspacePath: "/w",
      workspaceIdentity: "remote:ssh:h:/w",
      pinned: true,
    },
    {
      nex_type: "task-pin-response",
      requestId: "r1",
      taskId: "t1",
      success: true,
      pinned: false,
    },
    { nex_type: "task-pin-response", requestId: "r1", taskId: "t1", success: false, error: "x" },
    { nex_type: "task-archive-request", requestId: "r2", taskId: "t1", workspacePath: "/w" },
    {
      nex_type: "task-archive-request",
      requestId: "r2",
      taskId: "t1",
      workspacePath: "/w",
      archived: false,
    },
    {
      nex_type: "task-archive-response",
      requestId: "r2",
      taskId: "t1",
      success: true,
      archived: false,
    },
    {
      nex_type: "task-archive-response",
      requestId: "r2",
      taskId: "t1",
      success: true,
      archived: true,
    },
    { nex_type: "workspace-create-request", requestId: "r3", name: "demo" },
    {
      nex_type: "workspace-create-response",
      requestId: "r3",
      success: true,
      workspacePath: "/Users/u/NexProject/demo",
      workspaceKey: "/Users/u/NexProject/demo",
      projectId: "p1",
      hostId: "local",
    },
    { nex_type: "workspace-create-response", requestId: "r3", success: false, error: "bad" },
  ])("解析 $nex_type", (payload) => {
    expect(parseWebRemoteControlAppPayload(payload)).toEqual(payload);
  });

  it("拒绝缺字段或成功应答带错的 payload", () => {
    expect(
      parseWebRemoteControlAppPayload({
        nex_type: "task-pin-request",
        requestId: "r",
        taskId: "t",
      }),
    ).toBeNull();
    expect(
      parseWebRemoteControlAppPayload({
        nex_type: "task-archive-response",
        requestId: "r",
        taskId: "t",
        success: true,
      }),
    ).toBeNull();
  });

  it("Renderer IPC 按 kind 区分请求，结果区分成功/失败", () => {
    expect(
      webRemoteControlRendererActionRequestSchema.safeParse({
        requestId: "a",
        kind: "workspace-create",
        name: "demo",
      }).success,
    ).toBe(true);
    expect(
      webRemoteControlRendererActionRequestSchema.safeParse({ requestId: "a", kind: "unknown" })
        .success,
    ).toBe(false);
    expect(
      webRemoteControlRendererActionResultSchema.safeParse({ requestId: "a", success: false })
        .success,
    ).toBe(false);
  });
});

describe("normalizeWebRemoteControlWorkspaceName", () => {
  it("去首尾空白", () => {
    expect(normalizeWebRemoteControlWorkspaceName("  demo ")).toBe("demo");
  });

  it.each(["", "   ", "a/b", "a\\b", ".", ".."])("拒绝非法名称 %j", (name) => {
    expect(() => normalizeWebRemoteControlWorkspaceName(name)).toThrow();
  });
});

describe("resolveUnrecognizedWebRemoteControlRequest", () => {
  it("只对带 requestId 的 -request 类型回错", () => {
    expect(
      resolveUnrecognizedWebRemoteControlRequest({ nex_type: "future-request", requestId: "r" }),
    ).toEqual({ requestId: "r", nexType: "future-request" });
  });

  it.each([
    null,
    "text",
    { nex_type: "future-response", requestId: "r" },
    { nex_type: "future-request" },
    { nex_type: "future-request", requestId: "  " },
    { nex_type: 1, requestId: "r" },
  ])("忽略 %j", (value) => {
    expect(resolveUnrecognizedWebRemoteControlRequest(value)).toBeNull();
  });
});
