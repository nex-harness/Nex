import { describe, expect, it } from "vitest";
import { join } from "node:path";
import { resolveServerLayout } from "../src/runtime/paths.js";

describe("server install and application data roots", () => {
  it("keeps the default installation root separate from the home data base", () => {
    const layout = resolveServerLayout("/home/user/.nex/server");
    expect(layout.serverRoot).toBe("/home/user/.nex/server");
    expect(layout.dataBaseDir).toBe("/home/user");
  });

  it("does not nest a second .nex below an explicit application root", () => {
    const layout = resolveServerLayout("/tmp/nex-test/.nex");
    expect(layout.dataBaseDir).toBe("/tmp/nex-test");
  });

  it("keeps arbitrary installation roots isolated from their parent", () => {
    const layout = resolveServerLayout("/opt/nex-server");
    expect(layout.dataBaseDir).toBe("/opt/nex-server");
  });
});
