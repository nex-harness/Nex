import type { McpServerConfig } from "@nex/contracts";
import {
  resetCapturedNexCuaBrokerCredentialsForTest,
  sanitizeNexRuntimeEnvInPlace,
  NEX_CUA_OFFICIAL_PLUGIN_ID,
  NEX_CUA_PLUGIN_AUTHORITY_ENV_KEY,
  NEX_PLUGIN_ID_ENV_KEY,
} from "@nex/shared";
import { describe, expect, it } from "vitest";
import {
  injectNexCuaBrokerMcpServers,
  omitMcpServers,
  resolveTrustedOfficialCuaServerNames,
  NEX_CUA_BROKER_SOCKET_ENV,
  NEX_CUA_BROKER_UNAVAILABLE_ENV,
} from "../src/mcp-config.js";

// 遗留 bearer token 的环境变量名。broker 已整体改为 identity 模式（socket + plugin
// authority，无口令），本进程既不产生也不消费它；但 sanitize 仍必须剔除它，因为用户机上
// 可能装着认 token 的旧 Helper（见 packages/shared/src/runtimeEnv.ts 同名注释）。
//
// 刻意写字面量而不是从 src/mcp-config.js import：那里已经不再导出这个常量，而 vitest 下
// 缺失的命名导出不报链接错误、只是 undefined，于是 `process.env[undefined]` 变成读
// `"undefined"` 键，`toBeUndefined()` 之类断言全部永真。2026-09-16 评审实测本文件因此
// 静默失效（2 条断言退化 + 3 条真失败），且子包 tsconfig 只 include src/**，typecheck 看不到。
const LEGACY_BROKER_TOKEN_ENV = "NEX_CUA_PERMISSION_BROKER_TOKEN";

describe("project MCP config", () => {
  // CUA 已不再声明任何 MCP server（.nex-plugin/plugin.json 去掉 mcpServers，执行面搬到
  // 共享 node_repl 宿主），因此官方 CUA server 不出现在任何默认连接集合里。
  // 旧 server 的剔除由 mcp-project-config-startup.test.ts 的启动/状态用例及下方 direct-CLI 用例覆盖；
  // 产品凭据只向可信 node_repl 注入，另有 sanitize 与 captured 优先级用例。
  it("does not trust a project override that copies the official CUA plugin identity", () => {
    const officialPluginConfig: McpServerConfig = {
      type: "stdio",
      command: "/Applications/Nex Computer Use.app/Contents/MacOS/server",
      env: {
        [NEX_PLUGIN_ID_ENV_KEY]: NEX_CUA_OFFICIAL_PLUGIN_ID,
      },
    };
    const projectSpoof: McpServerConfig = {
      type: "stdio",
      command: "/tmp/attacker-command",
      env: {
        [NEX_PLUGIN_ID_ENV_KEY]: NEX_CUA_OFFICIAL_PLUGIN_ID,
      },
    };
    const pluginServers = { "plugin:official-cua:computer-use": officialPluginConfig };
    const configuredServers = {
      ...pluginServers,
      "plugin:official-cua:computer-use": projectSpoof,
    };

    expect(resolveTrustedOfficialCuaServerNames(configuredServers, pluginServers)).toEqual(
      new Set(),
    );

    const injected = injectNexCuaBrokerMcpServers(
      configuredServers,
      "/tmp/product-broker.sock",
      "product-token",
      resolveTrustedOfficialCuaServerNames(configuredServers, pluginServers),
    );
    expect(injected["plugin:official-cua:computer-use"]).toBeUndefined();
  });

  it("trusts the unmodified in-memory official CUA plugin config", () => {
    const officialPluginConfig: McpServerConfig = {
      type: "stdio",
      command: "/Applications/Nex Computer Use.app/Contents/MacOS/server",
      env: {
        [NEX_PLUGIN_ID_ENV_KEY]: NEX_CUA_OFFICIAL_PLUGIN_ID,
      },
    };
    const pluginServers = { "plugin:official-cua:computer-use": officialPluginConfig };
    const configuredServers = { ...pluginServers };

    expect(resolveTrustedOfficialCuaServerNames(configuredServers, pluginServers)).toEqual(
      new Set(["plugin:official-cua:computer-use"]),
    );
  });
  it("does not inject product broker args into unrelated MCP servers that only mention nex-cua", () => {
    const servers: Record<string, McpServerConfig> = {
      search: {
        type: "stdio",
        command: "node",
        args: ["/tmp/not-nex-cua/server.js", "--label", "nex-cua-notes"],
      },
    };

    const injected = injectNexCuaBrokerMcpServers(servers, "/tmp/broker.sock", "tok");

    expect(injected).toBe(servers);
  });

  it("drops explicit nex-cua package specs instead of injecting broker args", () => {
    const servers: Record<string, McpServerConfig> = {
      custom: {
        type: "stdio",
        command: "uvx",
        args: [
          "--from",
          "nex-cua[macos]@0.1.8",
          "nex-cua",
          "--permission-mode",
          "product",
          "--backend=broker",
          "--",
          "--backend",
          "payload-value",
        ],
      },
    };

    const injected = injectNexCuaBrokerMcpServers(
      servers,
      "/tmp/broker.sock",
      "tok",
      new Set(["custom"]),
    );
    expect(injected.custom).toBeUndefined();
  });

  it("re-injects the product broker into shared node_repl after CLI env sanitization", () => {
    // 复现 Critical：CLI 入口 sanitize 会先从 process.env 删除 broker socket/token，若定向注入只读
    // process.env，全局 ~/.nex/cli/config.json 的 nex-cua 会 fail-open 绕过 broker，让
    // Python/uvx 成为 TCC 主体。修复后 sanitize 前的凭据被捕获到进程内私有存储并仍能定向注入。
    const socketPath = "/tmp/nex-cua-broker.sock";
    const token = "broker-token-abc";
    const previousSocket = process.env[NEX_CUA_BROKER_SOCKET_ENV];
    const previousToken = process.env[LEGACY_BROKER_TOKEN_ENV];
    const previousAuthority = process.env[NEX_CUA_PLUGIN_AUTHORITY_ENV_KEY];
    const previousRefreshMarker = process.env["NEX_CUA_PERMISSION_BROKER_REFRESH_MARKER"];
    const previousUnavailable = process.env[NEX_CUA_BROKER_UNAVAILABLE_ENV];
    resetCapturedNexCuaBrokerCredentialsForTest();
    try {
      process.env[NEX_CUA_BROKER_SOCKET_ENV] = socketPath;
      process.env[LEGACY_BROKER_TOKEN_ENV] = token;
      process.env[NEX_CUA_PLUGIN_AUTHORITY_ENV_KEY] = "dev.nex.cua-helper/dev";
      process.env["NEX_CUA_PERMISSION_BROKER_REFRESH_MARKER"] =
        `${socketPath}.permission-refresh.json`;
      delete process.env[NEX_CUA_BROKER_UNAVAILABLE_ENV];

      // 模拟真实 CLI 入口：sanitize 剔除凭据（剔除前捕获到进程内私有存储）。
      sanitizeNexRuntimeEnvInPlace(process.env);
      expect(process.env[NEX_CUA_BROKER_SOCKET_ENV]).toBeUndefined();
      expect(process.env[LEGACY_BROKER_TOKEN_ENV]).toBeUndefined();

      const result = omitMcpServers(
        { node_repl: { type: "stdio", command: "node", args: ["server.js"] } },
        new Set(),
        new Set(["node_repl"]),
      );
      const nodeRepl = result.node_repl;
      const env = nodeRepl?.type === "stdio" ? (nodeRepl.env ?? {}) : {};

      expect(nodeRepl?.type === "stdio" ? nodeRepl.args : undefined).toEqual(["server.js"]);
      expect(env[NEX_CUA_BROKER_SOCKET_ENV]).toBe(socketPath);
      // identity 模式：凭据只有 socket + authority(+ refresh marker)。遗留 token 即使存在于
      // process.env 也必须止步于 sanitize，不得被重新注入给 node_repl —— 否则等于把一个
      // 旧 Helper 认的口令又发给了子进程。
      expect(env[LEGACY_BROKER_TOKEN_ENV]).toBeUndefined();
      expect(env[NEX_CUA_PLUGIN_AUTHORITY_ENV_KEY]).toBe("dev.nex.cua-helper/dev");
      expect(env["NEX_CUA_PERMISSION_BROKER_REFRESH_MARKER"]).toBe(
        `${socketPath}.permission-refresh.json`,
      );
    } finally {
      resetCapturedNexCuaBrokerCredentialsForTest();
      if (previousSocket === undefined) delete process.env[NEX_CUA_BROKER_SOCKET_ENV];
      else process.env[NEX_CUA_BROKER_SOCKET_ENV] = previousSocket;
      if (previousToken === undefined) delete process.env[LEGACY_BROKER_TOKEN_ENV];
      else process.env[LEGACY_BROKER_TOKEN_ENV] = previousToken;
      if (previousAuthority === undefined) delete process.env[NEX_CUA_PLUGIN_AUTHORITY_ENV_KEY];
      else process.env[NEX_CUA_PLUGIN_AUTHORITY_ENV_KEY] = previousAuthority;
      if (previousRefreshMarker === undefined)
        delete process.env["NEX_CUA_PERMISSION_BROKER_REFRESH_MARKER"];
      else process.env["NEX_CUA_PERMISSION_BROKER_REFRESH_MARKER"] = previousRefreshMarker;
      if (previousUnavailable === undefined) delete process.env[NEX_CUA_BROKER_UNAVAILABLE_ENV];
      else process.env[NEX_CUA_BROKER_UNAVAILABLE_ENV] = previousUnavailable;
    }
  });

  it("prefers captured broker credentials over stale/injected process.env values", () => {
    const previousSocket = process.env[NEX_CUA_BROKER_SOCKET_ENV];
    const previousToken = process.env[LEGACY_BROKER_TOKEN_ENV];
    const previousAuthority = process.env[NEX_CUA_PLUGIN_AUTHORITY_ENV_KEY];
    const previousUnavailable = process.env[NEX_CUA_BROKER_UNAVAILABLE_ENV];
    resetCapturedNexCuaBrokerCredentialsForTest();
    try {
      // 捕获可信凭据（模拟 CLI 入口 sanitize 从 Nex 下发的 spawn env 捕获）。
      const spawnEnv: Record<string, string | undefined> = {
        [NEX_CUA_BROKER_SOCKET_ENV]: "/tmp/captured.sock",
        [LEGACY_BROKER_TOKEN_ENV]: "captured-token",
        [NEX_CUA_PLUGIN_AUTHORITY_ENV_KEY]: "dev.nex.cua-helper/captured",
      };
      sanitizeNexRuntimeEnvInPlace(spawnEnv);
      // 运行时 env 里残留/被注入了不同的 broker 值：绝不能覆盖 captured。
      process.env[NEX_CUA_BROKER_SOCKET_ENV] = "/tmp/stale.sock";
      process.env[LEGACY_BROKER_TOKEN_ENV] = "stale-token";
      delete process.env[NEX_CUA_BROKER_UNAVAILABLE_ENV];

      const result = omitMcpServers(
        { node_repl: { type: "stdio", command: "node", args: ["server.js"] } },
        new Set(),
        new Set(["node_repl"]),
      );
      const nodeRepl = result.node_repl;
      const env = nodeRepl?.type === "stdio" ? (nodeRepl.env ?? {}) : {};

      expect(nodeRepl?.type === "stdio" ? nodeRepl.args : undefined).toEqual(["server.js"]);
      expect(env[NEX_CUA_BROKER_SOCKET_ENV]).toBe("/tmp/captured.sock");
      // captured 凭据也不含 token（identity 模式）；spawn env 里的遗留 token 被 sanitize 吃掉。
      expect(env[LEGACY_BROKER_TOKEN_ENV]).toBeUndefined();
      expect(env[NEX_CUA_PLUGIN_AUTHORITY_ENV_KEY]).toBe("dev.nex.cua-helper/captured");
    } finally {
      resetCapturedNexCuaBrokerCredentialsForTest();
      if (previousSocket === undefined) delete process.env[NEX_CUA_BROKER_SOCKET_ENV];
      else process.env[NEX_CUA_BROKER_SOCKET_ENV] = previousSocket;
      if (previousToken === undefined) delete process.env[LEGACY_BROKER_TOKEN_ENV];
      else process.env[LEGACY_BROKER_TOKEN_ENV] = previousToken;
      if (previousAuthority === undefined) delete process.env[NEX_CUA_PLUGIN_AUTHORITY_ENV_KEY];
      else process.env[NEX_CUA_PLUGIN_AUTHORITY_ENV_KEY] = previousAuthority;
      if (previousUnavailable === undefined) delete process.env[NEX_CUA_BROKER_UNAVAILABLE_ENV];
      else process.env[NEX_CUA_BROKER_UNAVAILABLE_ENV] = previousUnavailable;
    }
  });

  describe("retired direct-CLI nex-cua never starts an MCP server", () => {
    const restore = (snapshot: Record<string, string | undefined>) => {
      for (const [key, value] of Object.entries(snapshot)) {
        if (value === undefined) delete process.env[key];
        else process.env[key] = value;
      }
    };
    const KEYS = [
      NEX_CUA_BROKER_SOCKET_ENV,
      LEGACY_BROKER_TOKEN_ENV,
      NEX_CUA_BROKER_UNAVAILABLE_ENV,
      "NEX_CUA_HELPER_ALLOW_UNAUTHENTICATED_LOCAL",
    ];
    const snapshot = () =>
      Object.fromEntries(KEYS.map((k) => [k, process.env[k]])) as Record<
        string,
        string | undefined
      >;
    const clearAll = () => KEYS.forEach((k) => delete process.env[k]);
    const cuaServer = (): Record<string, McpServerConfig> => ({
      "nex-cua": {
        type: "stdio",
        command: "uvx",
        args: ["nex-cua"],
      },
    });

    it("omits a global nex-cua regardless of broker state", () => {
      const saved = snapshot();
      resetCapturedNexCuaBrokerCredentialsForTest();
      try {
        clearAll();
        const result = omitMcpServers(cuaServer(), new Set());
        expect(result["nex-cua"]).toBeUndefined();
      } finally {
        resetCapturedNexCuaBrokerCredentialsForTest();
        restore(saved);
      }
    });
  });
});
