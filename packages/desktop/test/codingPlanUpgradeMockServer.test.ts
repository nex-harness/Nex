import { afterEach, describe, expect, it, vi } from "vitest";

import {
  resetCodingPlanUpgradeMockServer,
  shouldPrepareCodingPlanUpgradeMockForBuild,
  startCodingPlanUpgradeMockServer,
  type CodingPlanUpgradeMockServer,
} from "./e2e/helpers/coding-plan-upgrade-mock-server.js";

describe("Coding Plan upgrade E2E mock server", () => {
  let server: CodingPlanUpgradeMockServer | null = null;

  afterEach(async () => {
    await server?.stop();
    server = null;
  });

  it("controls real upgrade HTML readiness and network failure", async () => {
    server = await startCodingPlanUpgradeMockServer("manual-claim-experience");
    const base = server.baseUrl;
    const mode = (mode: string) =>
      fetch(`${base}/__e2e/coding-plan/webview-mode`, {
        method: "POST",
        body: JSON.stringify({ mode }),
      });
    await mode("hold");
    const response = fetch(`${base}/coding-plan`);
    await vi.waitFor(async () =>
      expect(await fetchJson(`${base}/__e2e/coding-plan/webview-state`)).toEqual({ pending: 1 }),
    );
    await mode("ready");
    expect(await (await response).text()).toContain("coding-plan-webview-fixture");
    await mode("fail");
    await expect(fetch(`${base}/coding-plan`)).rejects.toThrow();
    await mode("ready");
  });

  it("webview inventory starts with a resolvable existing personal connection before purchase", async () => {
    server = await startCodingPlanUpgradeMockServer("webview-refresh-provider");
    const customer = await fetchJson<{ data: { organizations: unknown[] } }>(
      `${server.baseUrl}/api/biz/customer/getCustomerInfo`,
    );
    expect(customer.data.organizations).toEqual([
      { organizationId: "org-e2e", projects: [{ projectId: "proj-e2e", projectType: 0 }] },
    ]);
  });

  it.each(["personal-sold-out", "personal-system-busy"] as const)(
    "%s keeps credentials queryable while entitlement reports no plan",
    async (scenario) => {
      server = await startCodingPlanUpgradeMockServer(scenario);
      const customer = await fetchJson<{ data: { organizations: unknown[] } }>(
        `${server.baseUrl}/api/biz/customer/getCustomerInfo`,
      );
      expect(customer.data.organizations).toEqual([
        { organizationId: "org-e2e", projects: [{ projectId: "proj-e2e", projectType: 0 }] },
      ]);
      expect(await fetchJson(`${server.baseUrl}/api/biz/subscription/list`)).toMatchObject({
        data: [],
      });
      expect(await fetchJson(`${server.baseUrl}/api/monitor/usage/quota/limit`)).toMatchObject({
        code: 404,
        data: null,
      });
    },
  );

  it("expires Start without revoking Coding Plan and resets the expiry between workers", async () => {
    server = await startCodingPlanUpgradeMockServer("manual-claim-experience");
    await fetch(`${server.baseUrl}/api/v1/nex-plan/billing/claim`, { method: "POST" });
    const expired = await fetch(`${server.baseUrl}/__e2e/coding-plan/start-expired`, {
      method: "POST",
    });
    expect(expired.ok).toBe(true);
    const balance = await (await fetch(`${server.baseUrl}/api/v1/nex-plan/billing/balance`)).json();
    expect(balance.data).toMatchObject({ plans: [], balances: [] });
    const subscriptions = await (await fetch(`${server.baseUrl}/api/biz/subscription/list`)).json();
    expect(subscriptions.data).toHaveLength(1);
    await resetCodingPlanUpgradeMockServer(server.baseUrl, "manual-claim-experience");
    const restored = await (
      await fetch(`${server.baseUrl}/api/v1/nex-plan/billing/balance`)
    ).json();
    expect(restored.data.plans.length).toBeGreaterThan(0);
  });

  it("prepares the build-time origin for the default full-suite glob", () => {
    expect(shouldPrepareCodingPlanUpgradeMockForBuild(["./test/e2e/**/*.test.ts"])).toBe(true);
    expect(
      shouldPrepareCodingPlanUpgradeMockForBuild(["./test/e2e/conversation-session/*.test.ts"]),
    ).toBe(false);
  });

  it("resets scenario, purchase state, and request ledger between workers", async () => {
    server = await startCodingPlanUpgradeMockServer("personal-sold-out");

    expect(await fetchText(`${server.baseUrl}/coding-plan`)).toContain("Sold out");
    await resetCodingPlanUpgradeMockServer(server.baseUrl, "personal-system-busy");
    expect(server.scenario).toBe("personal-system-busy");

    const resetRequests = await fetchJson<{ requests: unknown[] }>(
      `${server.baseUrl}/__e2e/coding-plan/requests`,
    );
    expect(resetRequests.requests).toEqual([]);
    expect(await fetchText(`${server.baseUrl}/coding-plan`)).toContain("System busy");

    const preview = await fetchJson<{ code: number }>(
      `${server.baseUrl}/api/biz/pay/batch-preview`,
      { method: "POST" },
    );
    expect(preview.code).toBe(503);
  });

  it("keeps the old Start Plan bucket until the scheduled claim bucket becomes ready", async () => {
    server = await startCodingPlanUpgradeMockServer("manual-claim-experience");

    await fetchJson(`${server.baseUrl}/api/v1/nex-plan/billing/claim`, {
      method: "POST",
    });
    const pendingBalance = await fetchJson<{
      code: number;
      data: {
        balances: Array<{ capabilities?: string[]; entitlement_id?: string }>;
        plans: Array<{
          entitlements?: Array<{ effective_at?: number; entitlement_id?: string }>;
          plan_id?: string;
          status?: string;
        }>;
      };
    }>(`${server.baseUrl}/api/v1/nex-plan/billing/balance?app_version=e2e`);

    expect(pendingBalance.code).toBe(0);
    expect(pendingBalance.data.plans).toEqual([
      expect.objectContaining({ plan_id: "nex-v3-start-plan", status: "active" }),
      expect.objectContaining({
        plan_id: "weekend-plan-e2e",
        status: "active",
        entitlements: [
          expect.objectContaining({
            effective_at: expect.any(Number),
            entitlement_id: "weekend-glm-flash",
          }),
        ],
      }),
    ]);
    expect(pendingBalance.data.balances).toEqual([
      expect.objectContaining({
        capabilities: ["model:glm-5-turbo"],
        entitlement_id: "start-glm-turbo-e2e",
      }),
    ]);

    await fetchJson(`${server.baseUrl}/__e2e/coding-plan/manual-claim-bucket-ready`, {
      method: "POST",
    });
    const refreshedBalance = await fetchJson<{
      data: { balances: Array<{ capabilities?: string[]; entitlement_id?: string }> };
    }>(`${server.baseUrl}/api/v1/nex-plan/billing/balance?app_version=e2e`);
    expect(refreshedBalance.data.balances).toEqual([
      expect.objectContaining({ entitlement_id: "start-glm-turbo-e2e" }),
      expect.objectContaining({
        capabilities: ["model:glm-5.3-flash"],
        entitlement_id: "weekend-glm-flash",
      }),
    ]);

    const subscriptions = await fetchJson<{
      data: Array<{ productId?: string; status?: string }>;
    }>(`${server.baseUrl}/api/biz/subscription/list`);
    expect(subscriptions.data).toEqual([
      expect.objectContaining({ productId: "glm-coding-pro-e2e", status: "VALID" }),
    ]);

    const customer = await fetchJson<{
      data: { organizations?: Array<{ organizationId?: string }> };
    }>(`${server.baseUrl}/api/biz/customer/getCustomerInfo`);
    expect(customer.data.organizations).toEqual([
      expect.objectContaining({ organizationId: "org-e2e" }),
    ]);

    const copiedKey = await fetchJson<{ data: { secretKey?: string } }>(
      `${server.baseUrl}/api/biz/v1/organization/org-e2e/projects/proj-e2e/api_keys/copy/bigmodel-personal-api-key`,
    );
    expect(copiedKey.data.secretKey).toBe("");
  });

  it("reopens the manual claim preview for the next polling window", async () => {
    server = await startCodingPlanUpgradeMockServer("manual-claim-experience");

    await fetchJson(`${server.baseUrl}/api/v1/nex-plan/billing/claim`, {
      method: "POST",
    });
    const claimedPreview = await fetchJson<{ data: { plans: unknown[] } }>(
      `${server.baseUrl}/api/v1/nex-plan/billing/preview`,
    );
    expect(claimedPreview.data.plans).toEqual([]);

    await fetchJson(`${server.baseUrl}/__e2e/coding-plan/manual-claim-next-window`, {
      method: "POST",
    });
    const nextWindowPreview = await fetchJson<{
      data: { plans: Array<{ plan_id?: string }> };
    }>(`${server.baseUrl}/api/v1/nex-plan/billing/preview`);
    expect(nextWindowPreview.data.plans).toEqual([
      expect.objectContaining({ plan_id: "weekend-plan-e2e" }),
    ]);
  });

  it("returns the copied Z.ai secret after a webview purchase completes", async () => {
    server = await startCodingPlanUpgradeMockServer("webview-refresh-provider");

    await fetchJson(`${server.baseUrl}/__e2e/coding-plan/purchase-complete`, {
      method: "POST",
    });
    const copiedKey = await fetchJson<{ data: { secretKey?: string } }>(
      `${server.baseUrl}/api/biz/v1/organization/org-e2e/projects/proj-e2e/api_keys/copy/bigmodel-webview-refreshed-key`,
      { headers: { authorization: "Bearer zai-webview-oauth-token" } },
    );

    // Z.ai provider 拒绝裸 apiKey；fixture 必须覆盖 copy 接口的完整 key 合同。
    expect(copiedKey.data.secretKey).toBe("zai-webview-refreshed-secret");
  });

  it("does not append the Z.ai secret to a BigModel copied API key", async () => {
    server = await startCodingPlanUpgradeMockServer("webview-refresh-provider");

    await fetchJson(`${server.baseUrl}/__e2e/coding-plan/purchase-complete`, {
      method: "POST",
    });
    const copiedKey = await fetchJson<{ data: { secretKey?: string } }>(
      `${server.baseUrl}/api/biz/v1/organization/org-e2e/projects/proj-e2e/api_keys/copy/bigmodel-webview-refreshed-key`,
      { headers: { authorization: "e2e-bigmodel-oauth-token" } },
    );

    expect(copiedKey.data.secretKey).toBe("");
  });
});

async function fetchText(url: string) {
  const response = await fetch(url);
  expect(response.ok).toBe(true);
  return await response.text();
}

async function fetchJson<T>(url: string, init?: RequestInit): Promise<T> {
  const response = await fetch(url, init);
  expect(response.ok).toBe(true);
  return (await response.json()) as T;
}
