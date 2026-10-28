import assert from "node:assert/strict";
import test from "node:test";
import type { RunnerModelStatus } from "@nex/shared";
import { resolveRunnerModelOnboardingState } from "../src/lib/runnerModelOnboarding.js";

// 运行：pnpm --dir packages/ui exec tsx --tsconfig tsconfig.json --test test/runnerModelOnboarding.test.ts

function status(fields: Partial<RunnerModelStatus> & { runnerId: string }): RunnerModelStatus {
  return {
    name: fields.runnerId,
    connected: true,
    mode: "server",
    modelCount: 0,
    lastAppliedVersion: 1,
    ...fields,
  };
}

test("new runner still unknown keeps checking", () => {
  const state = resolveRunnerModelOnboardingState({
    statuses: [status({ runnerId: "new", mode: "unknown" })],
    runnerId: "new",
    timedOut: false,
  });
  assert.deepEqual(state, { kind: "checking" });
});

test("missing runner keeps checking before timeout", () => {
  const state = resolveRunnerModelOnboardingState({
    statuses: [],
    runnerId: "new",
    timedOut: false,
  });
  assert.deepEqual(state, { kind: "checking" });
});

test("runner with models is ready", () => {
  const state = resolveRunnerModelOnboardingState({
    statuses: [status({ runnerId: "new", modelCount: 3 })],
    runnerId: "new",
    timedOut: false,
  });
  assert.deepEqual(state, { kind: "ready", modelCount: 3 });
});

test("resolved runner with zero models needs models without waiting for timeout", () => {
  const state = resolveRunnerModelOnboardingState({
    statuses: [status({ runnerId: "new", mode: "fallback", modelCount: 0 })],
    runnerId: "new",
    timedOut: false,
  });
  assert.deepEqual(state, { kind: "needs-models", sources: [] });
});

test("unknown after timeout counts as needs-models", () => {
  const state = resolveRunnerModelOnboardingState({
    statuses: [status({ runnerId: "new", mode: "unknown" })],
    runnerId: "new",
    timedOut: true,
  });
  assert.deepEqual(state, { kind: "needs-models", sources: [] });
});

test("sources exclude the new runner, disconnected machines, and empty catalogs", () => {
  const state = resolveRunnerModelOnboardingState({
    statuses: [
      status({ runnerId: "new", modelCount: 0 }),
      status({ runnerId: "alpha", name: "Alpha", modelCount: 4 }),
      status({ runnerId: "beta", name: "Beta", modelCount: 2, connected: false }),
      status({ runnerId: "gamma", name: "Gamma", modelCount: 0 }),
    ],
    runnerId: "new",
    timedOut: true,
  });
  assert.equal(state.kind, "needs-models");
  if (state.kind !== "needs-models") return;
  assert.deepEqual(
    state.sources.map((source) => source.runnerId),
    ["alpha"],
  );
});
