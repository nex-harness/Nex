import { join } from "node:path";
import { tmpdir } from "node:os";
import { WorkspaceRoots } from "../../src/workspaces/workspaceRoots.js";
import type { Logger } from "../../src/logger.js";

const quietLogger: Logger = {
  debug: () => undefined,
  info: () => undefined,
  warn: () => undefined,
  error: () => undefined,
  child: () => quietLogger,
} as unknown as Logger;

/**
 * A `WorkspaceRoots` whose served folders are exactly `paths` (env roots, so
 * `remove` refuses them). `load()` is not needed: with no persisted file the
 * effective set is the env roots alone, which is what these tests assert.
 */
export function testRoots(paths: readonly string[], dataDir?: string): WorkspaceRoots {
  return new WorkspaceRoots({
    envRoots: [...paths],
    dataDir: dataDir ?? join(tmpdir(), "nex-runner-test-roots"),
    logger: quietLogger,
  });
}
