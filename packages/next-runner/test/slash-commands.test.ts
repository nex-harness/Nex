import { test } from "node:test";
import assert from "node:assert/strict";
import {
  composeSlashCommands,
  skillCatalogFrom,
  slashCommandsFrom,
} from "../src/channels/piCatalog.js";
import type { PiResourceCatalog } from "../src/engine/pi/piResources.js";
import { nexSkillsReferenceCatalogResultSchema, nexSlashCommandSchema } from "@nex/shared";

/**
 * P2: prompt templates and `/skill:<name>` commands are built from pi's loaded
 * resources, and pi expands them at prompt time (see the integration test for
 * the expansion itself).
 */

const resources: PiResourceCatalog = {
  skills: [
    {
      name: "echo-helper",
      description: "Explains how to use the echo tool.",
      filePath: "/x/echo-helper/SKILL.md",
    },
    { name: "review", description: "Skill that shadows nothing.", filePath: "/x/review/SKILL.md" },
  ],
  prompts: [{ name: "review", description: "Review a diff", argumentHint: "<path>" }],
};

test("slashCommandsFrom lists templates first, then skill commands", () => {
  const commands = slashCommandsFrom(resources, { skillCommands: true }).map((command) =>
    nexSlashCommandSchema.parse(command),
  );
  assert.deepEqual(
    commands.map((command) => command.name),
    ["review", "skill:echo-helper", "skill:review"],
  );
  assert.equal(commands[0]?.inputHint, "<path>");
  assert.equal(commands[0]?.source, "custom");
});

test("skill commands are omitted when enableSkillCommands is off", () => {
  const commands = slashCommandsFrom(resources, { skillCommands: false });
  assert.deepEqual(
    commands.map((command) => command.name),
    ["review"],
  );
});

test("the skill catalog matches the composer schema and folds duplicates", () => {
  const catalog = nexSkillsReferenceCatalogResultSchema.parse({
    authority: "workspace",
    skills: skillCatalogFrom(resources),
  });
  assert.deepEqual(
    catalog.skills.map((skill) => [skill.id, skill.name, skill.scope]),
    [
      ["glm:user:/x/echo-helper/SKILL.md", "echo-helper", "user"],
      ["glm:user:/x/review/SKILL.md", "review", "user"],
    ],
  );
  // The composer's `/` and `$` panels only keep `glm:`-prefixed entries.
  assert.ok(catalog.skills.every((skill) => skill.id.startsWith("glm:")));
});

test("server-owned skills are listed first in the slash menu", () => {
  const commands = composeSlashCommands({
    slashCommands: [{ name: "docs-skill", description: "From the server doc", source: "custom" }],
    resources,
    skillCommands: true,
  });
  assert.deepEqual(
    commands.map((command) => command.name),
    ["compact", "docs-skill", "review", "skill:echo-helper", "skill:review"],
  );
});

test("built-in /compact stays first so typing /compact never picks a skill", () => {
  const commands = composeSlashCommands({
    slashCommands: [{ name: "compact", description: "shadow", source: "custom" }],
    resources,
    skillCommands: true,
  });
  assert.equal(commands[0]?.name, "compact");
  assert.equal(commands[0]?.source, "builtin");
  assert.equal(commands.filter((command) => command.name === "compact").length, 1);
});
