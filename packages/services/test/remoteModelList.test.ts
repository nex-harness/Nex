import { describe, expect, it } from "vitest";
import type { ProviderConfigObject } from "@nex/provider";
import {
  buildRemoteModelListRequest,
  fetchRemoteModelList,
  parseRemoteModelIds,
} from "../src/model-provider/remoteModelList.js";

function providerConfig(overrides: {
  type?: "anthropic-messages" | "openai-chat-completions";
  baseUrl?: string;
  headers?: Record<string, string>;
  apiKey?: string;
}): ProviderConfigObject {
  return {
    api: {
      type: overrides.type ?? "openai-chat-completions",
      baseUrl: overrides.baseUrl ?? "https://api.example.com/v1/",
      ...(overrides.headers ? { headers: overrides.headers } : {}),
    },
    access: { type: "api-key", apiKey: overrides.apiKey ?? "sk-test" },
  } as ProviderConfigObject;
}

describe("parseRemoteModelIds", () => {
  it("handles OpenAI, Gemini and bare-array payloads", () => {
    expect(parseRemoteModelIds({ data: [{ id: "gpt-5.4" }, { id: "gpt-5.4" }] })).toEqual([
      "gpt-5.4",
    ]);
    expect(parseRemoteModelIds({ models: [{ name: "models/gemini-3-pro" }, { name: "  " }] })).toEqual(
      ["gemini-3-pro"],
    );
    expect(parseRemoteModelIds(["a", { id: "b" }, 42, null])).toEqual(["a", "b"]);
    expect(parseRemoteModelIds("not json object")).toEqual([]);
  });
});

describe("buildRemoteModelListRequest", () => {
  it("uses bearer auth and trims trailing slashes", () => {
    const { url, headers } = buildRemoteModelListRequest(providerConfig({}));
    expect(url).toBe("https://api.example.com/v1/models");
    expect(headers.get("authorization")).toBe("Bearer sk-test");
    expect(headers.get("accept")).toBe("application/json");
  });

  it("uses Anthropic headers for anthropic-messages", () => {
    const { headers } = buildRemoteModelListRequest(
      providerConfig({ type: "anthropic-messages" }),
    );
    expect(headers.get("x-api-key")).toBe("sk-test");
    expect(headers.get("anthropic-version")).toBe("2023-06-01");
    expect(headers.has("authorization")).toBe(false);
  });

  it("never overrides user headers, regardless of case", () => {
    const { headers } = buildRemoteModelListRequest(
      providerConfig({ headers: { Authorization: "Custom token" } }),
    );
    expect(headers.get("authorization")).toBe("Custom token");
  });

  it("rejects a provider without base URL", () => {
    expect(() => buildRemoteModelListRequest(providerConfig({ baseUrl: "  " }))).toThrow();
  });
});

describe("fetchRemoteModelList", () => {
  it("surfaces HTTP failures", async () => {
    const request = (async () => new Response("nope", { status: 401 })) as typeof fetch;
    await expect(fetchRemoteModelList(providerConfig({}), request)).rejects.toThrow(/HTTP 401/);
  });
});
