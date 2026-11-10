import { describe, expect, it } from "vitest";
import { resolveDefaultEmbeddedSearchBackend } from "../src/app/embedded-search-backend.js";

describe("resolveDefaultEmbeddedSearchBackend", () => {
  it("uses bundled bfs, ugrep, and ripgrep paths by default", () => {
    expect(
      resolveDefaultEmbeddedSearchBackend({
        env: {
          NEX_BFS_BINARY: "/opt/nex/tools/bfs/bfs",
          NEX_RG_BINARY: "/opt/nex/tools/ripgrep/rg",
          NEX_UGREP_BINARY: "/opt/nex/tools/ugrep/ugrep",
        },
      }),
    ).toEqual({
      kind: "native-binaries",
      findCommand: "/opt/nex/tools/bfs/bfs",
      grepCommand: "/opt/nex/tools/ugrep/ugrep",
      rgCommand: "/opt/nex/tools/ripgrep/rg",
    });
  });

  it("falls back to bfs, ugrep, and rg command names when bundled paths are unavailable", () => {
    expect(resolveDefaultEmbeddedSearchBackend({ env: {} })).toEqual({
      kind: "native-binaries",
      findCommand: "bfs",
      grepCommand: "ugrep",
      rgCommand: "rg",
    });
  });

  it("preserves the explicit internal CLI compatibility override", () => {
    expect(
      resolveDefaultEmbeddedSearchBackend({
        env: { NEX_EMBEDDED_SEARCH_COMMAND: "/tmp/nex-test" },
      }),
    ).toEqual({
      kind: "internal-cli",
      command: "/tmp/nex-test",
      args: ["__internal-search"],
    });
  });
});
