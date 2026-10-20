import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { PiEngine } from "../src/engine/pi/piEngine.js";
import { createProjection } from "../src/projection/state.js";
import { applyEngineEvent } from "../src/projection/fold.js";
import { buildConversationSnapshot } from "../src/projection/snapshot.js";
import { createLogger } from "../src/logger.js";
import type { EngineEvent } from "../src/engine/types.js";

/**
 * Opt-in real-model smoke test. Requires the user's pi credentials in ~/.pi/agent.
 * Run with: RUN_PI_SMOKE=1 pnpm --filter @nex/next-runner test
 */
const enabled = process.env["RUN_PI_SMOKE"] === "1";

test(
  "pi smoke: real session answers pong",
  { skip: enabled ? false : "set RUN_PI_SMOKE=1 to run the real-model smoke test" },
  async () => {
    const logger = createLogger("warn");
    const root = await mkdtemp(join(tmpdir(), "nex-next-runner-smoke-"));
    const workspace = join(root, "workspace");
    await import("node:fs/promises").then((fs) => fs.mkdir(workspace, { recursive: true }));
    const engine = new PiEngine({ dataDir: join(root, "data"), logger });
    try {
      await engine.init();
      const catalog = await engine.catalog();
      assert.ok(catalog.models.length > 0, "pi must expose at least one chat model");
      const selection = catalog.defaultSelection ?? {
        providerId: catalog.models[0]!.providerId,
        modelId: catalog.models[0]!.modelId,
        options: { reasoningLevel: catalog.models[0]!.reasoningLevels[0] ?? "off" },
      };

      const { sessionId } = await engine.createSession(workspace, {
        modelSelection: selection,
      });

      let projection = createProjection({
        sessionId,
        logEpoch: "smoke-epoch",
        createdAt: Date.now(),
        model: {
          providerId: selection.providerId,
          modelId: selection.modelId,
          thought: selection.options?.reasoningLevel ?? "off",
          thoughtLevels: catalog.models.find(
            (model) =>
              model.providerId === selection.providerId && model.modelId === selection.modelId,
          )?.reasoningLevels ?? ["off"],
        },
      });
      const subscription = engine.subscribe(sessionId, (event: EngineEvent) => {
        projection = applyEngineEvent(projection, event);
      });
      try {
        await engine.submit(sessionId, "Reply with exactly: pong", { requestId: "smoke-1" });
        const deadline = Date.now() + 120_000;
        let found = "";
        while (Date.now() < deadline) {
          const snapshot = buildConversationSnapshot(projection);
          const text = snapshot.rows.window
            .filter(
              (row): row is Extract<typeof row, { kind: "assistantText" }> =>
                row.kind === "assistantText",
            )
            .map((row) => row.text)
            .join("");
          if (/pong/iu.test(text)) {
            found = text;
            break;
          }
          if (snapshot.control.phase === "error") {
            throw new Error(
              `pi run failed: ${snapshot.control.lastError?.message ?? "unknown provider error"}`,
            );
          }
          await new Promise((resolve) => setTimeout(resolve, 200));
        }
        assert.match(found, /pong/iu, "assistant text must contain pong");
      } finally {
        subscription.dispose();
        await engine.abort(sessionId);
      }
    } finally {
      await engine.dispose();
      await rm(root, { recursive: true, force: true });
    }
  },
);
