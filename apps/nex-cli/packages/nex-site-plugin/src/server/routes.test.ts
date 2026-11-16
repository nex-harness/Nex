import { describe, expect, it } from "vitest";
import { resourcePath } from "./routes.ts";

const SITE = "1978123456789012480";
const DEP = "1978123456789012481";

describe("resourcePath", () => {
  it.each([
    ["nex-site://usage", "/api/v1/sites/usage"],
    ["nex-site://plans", "/api/v1/sites/plans"],
    ["nex-site://sites", "/api/v1/sites"],
    ["nex-site://sites?cursor=", "/api/v1/sites"],
    [
      "nex-site://sites?cursor=a%2Fb&limit=20",
      "/api/v1/sites?cursor=a%2Fb&limit=20",
    ],
    [`nex-site://sites/${SITE}`, `/api/v1/sites/${SITE}`],
    [
      `nex-site://sites/${SITE}/deployments?cursor=x`,
      `/api/v1/sites/${SITE}/deployments?cursor=x`,
    ],
    [
      `nex-site://sites/${SITE}/deployments/${DEP}`,
      `/api/v1/sites/${SITE}/deployments/${DEP}`,
    ],
    [`nex-site://sites/${SITE}/cover`, `/api/v1/sites/${SITE}/cover`],
    [`nex-site://sites/${SITE}/analytics`, `/api/v1/sites/${SITE}/analytics`],
    [
      `nex-site://sites/${SITE}/analytics?range=30d`,
      `/api/v1/sites/${SITE}/analytics?range=30d`,
    ],
    [
      `nex-site://sites/analytics/pv24h?siteIds=${SITE},${DEP}`,
      `/api/v1/sites/analytics/pv24h?siteIds=${SITE}%2C${DEP}`,
    ],
    [
      `nex-site://sites/${SITE}/deployments/${DEP}/logs?offset=12`,
      `/api/v1/sites/${SITE}/deployments/${DEP}/logs?offset=12`,
    ],
  ])("maps %s", (uri, path) => {
    expect(resourcePath(uri)).toBe(path);
  });

  it.each([
    "nex-site://other",
    "https://sites/1",
    "nex-site://usage?cursor=1",
    "nex-site://plans?limit=1",
    "nex-site://plans/free",
    "nex-site://sites/abc",
    "nex-site://sites/0123",
    "nex-site://sites/1/deployments/2/logs/extra",
    "nex-site://sites/1/cover/2",
    "nex-site://sites/1/cover?offset=1",
    "nex-site://sites/1/settings",
    "nex-site://sites/1/deployments/2/logs?offset=-1",
    "nex-site://sites?limit=1e3",
    "nex-site://sites?cursor=a&cursor=b",
    "nex-site://sites/1/deployments/2?offset=1",
    "nex-site://user@sites",
    "nex-site://sites/..%2Fusage",
    "nex-site://sites/1/analytics?range=1y",
    "nex-site://sites/1/analytics?offset=1",
    "nex-site://sites/1/analytics/pages",
    "nex-site://sites/analytics",
    "nex-site://sites/analytics/pv24h?siteIds=1,abc",
    `nex-site://sites/analytics/pv24h?siteIds=${Array(21).fill("1").join(",")}`,
    "nex-site://sites/analytics/pv24h?range=24h",
    "not a uri",
  ])("rejects %s", (uri) => {
    expect(resourcePath(uri)).toBeUndefined();
  });
});
