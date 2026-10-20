import { test } from "node:test";
import assert from "node:assert/strict";
import { PermissionGate } from "../src/engine/permissions/gate.js";
import type { EngineEvent } from "../src/engine/types.js";

/**
 * The gate is the single writer of pending approval state. These tests pin the two
 * failure modes the spec calls out: no hang on abort, and idempotent resolve.
 */

function makeGate(): { gate: PermissionGate; events: EngineEvent[] } {
  const events: EngineEvent[] = [];
  const gate = new PermissionGate({ emit: (event) => events.push(event) });
  return { gate, events };
}

function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

test("gate: read-only bash never asks and emits no approval event", async () => {
  const { gate, events } = makeGate();
  const decision = await gate.authorize({
    toolCallId: "t1",
    toolName: "bash",
    input: { command: "ls -la" },
    signal: undefined,
  });
  assert.deepEqual(decision, { block: false });
  assert.equal(events.length, 0);
});

test("gate: plan mode denies a write without emitting a card", async () => {
  const { gate, events } = makeGate();
  gate.setMode("build", true);
  const decision = await gate.authorize({
    toolCallId: "t1",
    toolName: "write",
    input: { path: "a.txt", content: "x" },
    signal: undefined,
  });
  assert.equal(decision.block, true);
  assert.match(decision.reason ?? "", /Plan mode/u);
  assert.equal(events.filter((event) => event.type === "approval_request").length, 0);
  assert.equal(gate.pendingApprovals().length, 0);
});

test("gate: ask → allow once unblocks and settles with outcome allow", async () => {
  const { gate, events } = makeGate();
  const pending = gate.authorize({
    toolCallId: "t1",
    toolName: "write",
    input: { path: "a.txt", content: "x" },
    signal: undefined,
  });
  const requests = events.filter((event) => event.type === "approval_request");
  assert.equal(requests.length, 1);
  const request = requests[0]!.type === "approval_request" ? requests[0]!.approval : null;
  assert.ok(request);
  assert.equal(request.toolName, "write");
  assert.match(request.summary, /write a\.txt/u);
  assert.equal(gate.pendingApprovals().length, 1);

  const outcome = gate.resolve(request.interactionId, { optionId: "allowOnce" });
  assert.deepEqual(outcome, { resolved: true, outcome: "allow" });
  assert.deepEqual(await pending, { block: false });
  assert.equal(gate.pendingApprovals().length, 0);
  const settled = events.filter((event) => event.type === "approval_settled");
  assert.equal(settled.length, 1);
  assert.equal(settled[0]!.type === "approval_settled" ? settled[0]!.outcome : "", "allow");
});

test("gate: deny blocks with the user's freeText in the reason", async () => {
  const { gate, events } = makeGate();
  const pending = gate.authorize({
    toolCallId: "t1",
    toolName: "bash",
    input: { command: "git push" },
    signal: undefined,
  });
  const request = gate.pendingApprovals()[0]!;
  gate.resolve(request.interactionId, { optionId: "deny", freeText: "not this branch" });
  const decision = await pending;
  assert.equal(decision.block, true);
  assert.match(decision.reason ?? "", /not this branch/u);
  const settled = events.find((event) => event.type === "approval_settled");
  assert.equal(settled?.type === "approval_settled" ? settled.outcome : "", "deny");
});

test("gate: resolve is idempotent — duplicate and unknown ids are no-ops", async () => {
  const { gate, events } = makeGate();
  const pending = gate.authorize({
    toolCallId: "t1",
    toolName: "write",
    input: { path: "a", content: "b" },
    signal: undefined,
  });
  const request = gate.pendingApprovals()[0]!;
  assert.deepEqual(gate.resolve(request.interactionId, { optionId: "allowOnce" }), {
    resolved: true,
    outcome: "allow",
  });
  assert.deepEqual(gate.resolve(request.interactionId, { optionId: "deny" }), {
    resolved: false,
  });
  assert.deepEqual(gate.resolve("perm_unknown", { optionId: "allowOnce" }), {
    resolved: false,
  });
  assert.equal(events.filter((event) => event.type === "approval_settled").length, 1);
  assert.deepEqual(await pending, { block: false });
});

test("gate: allowAlways auto-allows the same tool later in this session only", async () => {
  const { gate } = makeGate();
  const first = gate.authorize({
    toolCallId: "t1",
    toolName: "bash",
    input: { command: "rm -rf build" },
    signal: undefined,
  });
  gate.resolve(gate.pendingApprovals()[0]!.interactionId, { optionId: "allowAlways" });
  assert.deepEqual(await first, { block: false });
  // Same gate (= same session): no second card.
  const second = await gate.authorize({
    toolCallId: "t2",
    toolName: "bash",
    input: { command: "rm -rf dist" },
    signal: undefined,
  });
  assert.deepEqual(second, { block: false });
  assert.equal(gate.pendingApprovals().length, 0);
  // A different gate (= another session) still asks.
  const other = makeGate();
  const third = other.gate.authorize({
    toolCallId: "t3",
    toolName: "bash",
    input: { command: "rm -rf dist" },
    signal: undefined,
  });
  assert.equal(other.gate.pendingApprovals().length, 1);
  other.gate.resolve(other.gate.pendingApprovals()[0]!.interactionId, { optionId: "deny" });
  assert.equal((await third).block, true);
});

test("gate: abort while pending settles as aborted without hanging", async () => {
  const { gate, events } = makeGate();
  const controller = new AbortController();
  const pending = gate.authorize({
    toolCallId: "t1",
    toolName: "write",
    input: { path: "a", content: "b" },
    signal: controller.signal,
  });
  assert.equal(gate.pendingApprovals().length, 1);
  controller.abort();
  const decision = await pending;
  assert.equal(decision.block, true);
  assert.match(decision.reason ?? "", /cancelled/u);
  assert.equal(gate.pendingApprovals().length, 0);
  const settled = events.find((event) => event.type === "approval_settled");
  assert.equal(settled?.type === "approval_settled" ? settled.outcome : "", "aborted");
});

test("gate: abortPending resolves live cards but keeps the gate usable", async () => {
  const { gate } = makeGate();
  const pending = gate.authorize({
    toolCallId: "t1",
    toolName: "write",
    input: { path: "a", content: "b" },
    signal: undefined,
  });
  gate.abortPending();
  assert.equal((await pending).block, true);
  const next = gate.authorize({
    toolCallId: "t2",
    toolName: "write",
    input: { path: "a", content: "b" },
    signal: undefined,
  });
  assert.equal(gate.pendingApprovals().length, 1);
  gate.resolve(gate.pendingApprovals()[0]!.interactionId, { optionId: "allowOnce" });
  assert.deepEqual(await next, { block: false });
});

test("gate: an already-aborted signal never shows a card", async () => {
  const { gate, events } = makeGate();
  const controller = new AbortController();
  controller.abort();
  const decision = await gate.authorize({
    toolCallId: "t1",
    toolName: "write",
    input: { path: "a", content: "b" },
    signal: controller.signal,
  });
  assert.equal(decision.block, true);
  // The abort listener runs on a microtask; give it one turn to remove the card.
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(gate.pendingApprovals().length, 0);
  assert.equal(events.filter((event) => event.type === "approval_settled").length, 1);
});

test("gate: setMode emits mode_changed only on an actual change", () => {
  const { gate, events } = makeGate();
  gate.setMode("build", false);
  assert.equal(events.length, 0);
  gate.setMode("edit", false);
  gate.setMode("edit", false);
  const changes = events.filter((event) => event.type === "mode_changed");
  assert.equal(changes.length, 1);
  assert.equal(changes[0]!.type === "mode_changed" ? changes[0]!.mode : "", "edit");
});

test("gate: two concurrent approvals settle independently", async () => {
  const { gate } = makeGate();
  const first = gate.authorize({
    toolCallId: "t1",
    toolName: "write",
    input: { path: "a", content: "b" },
    signal: undefined,
  });
  const second = gate.authorize({
    toolCallId: "t2",
    toolName: "edit",
    input: { path: "c", edits: [] },
    signal: undefined,
  });
  const [id1, id2] = gate.pendingApprovals().map((request) => request.interactionId);
  assert.ok(id1 && id2 && id1 !== id2);
  gate.resolve(id2!, { optionId: "deny" });
  gate.resolve(id1!, { optionId: "allowOnce" });
  assert.deepEqual(await first, { block: false });
  assert.equal((await second).block, true);
  assert.equal(gate.pendingApprovals().length, 0);
  void deferred;
});
