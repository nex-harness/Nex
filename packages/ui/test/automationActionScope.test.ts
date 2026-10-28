import assert from "node:assert/strict";
import test from "node:test";
import type { NexAutomation } from "@nex/shared";
import { resolveAutomationActionScope } from "../src/store/automationManagementStore.js";

// 运行：pnpm --dir packages/ui exec tsx --tsconfig tsconfig.json --test test/automationActionScope.test.ts

const viewScope = {
  workspacePath: "/srv/app",
  workspaceIdentity: "runner:alpha:/srv/app",
};

function automation(fields: Partial<NexAutomation>): NexAutomation {
  return { automationId: "automation-1", workspacePath: "/srv/app", ...fields } as NexAutomation;
}

test("path-scoped automation is not given the view's runner identity", () => {
  const scope = resolveAutomationActionScope(
    { ...viewScope, automations: [automation({})] },
    "automation-1",
  );
  assert.deepEqual(scope, { workspacePath: "/srv/app", workspaceIdentity: null });
});

test("identity-scoped automation keeps its own identity", () => {
  const scope = resolveAutomationActionScope(
    {
      ...viewScope,
      automations: [automation({ workspaceIdentity: "runner:beta:/srv/app" })],
    },
    "automation-1",
  );
  assert.deepEqual(scope, { workspacePath: "/srv/app", workspaceIdentity: "runner:beta:/srv/app" });
});

test("unknown automation falls back to the view scope", () => {
  const scope = resolveAutomationActionScope({ ...viewScope, automations: [] }, "missing");
  assert.deepEqual(scope, viewScope);
});
