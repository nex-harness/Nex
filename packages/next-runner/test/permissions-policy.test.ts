import { test } from "node:test";
import assert from "node:assert/strict";
import { isReadOnlyBashCommand } from "../src/engine/permissions/bashClassifier.js";
import { decideToolCall } from "../src/engine/permissions/policy.js";

/**
 * Spec: docs/next/permissions.md — policy table and the conservative bash classifier.
 * Each false negative would run a mutation without approval, so the negative cases are
 * the important half of this file.
 */

const BASE = { alwaysAllowed: new Set<string>(), planEnabled: false };

test("classifier: read-only commands are allowed", () => {
  for (const command of [
    "ls",
    "ls -la",
    "cat notes.txt",
    "head -n 5 a.txt",
    "tail -f log",
    "pwd",
    "rg TODO src",
    "grep -r foo .",
    "find . -name '*.ts'",
    "wc -l a.txt",
    "stat a.txt",
    "file a.bin",
    "echo hello",
    "sort a.txt",
    "uniq a.txt",
    "diff a.txt b.txt",
    "which node",
    "git status",
    "git diff HEAD",
    "git log --oneline -5",
    "git show HEAD",
    "git rev-parse HEAD",
    "git branch",
  ]) {
    assert.equal(isReadOnlyBashCommand(command), true, command);
  }
});

test("classifier: composition, redirects, substitution and mutation are not read-only", () => {
  for (const command of [
    "ls; rm -rf /",
    "ls && rm -rf /",
    "ls || true",
    "cat a > b",
    "cat a >> b",
    "cat < a",
    "find . -delete",
    "find . -exec rm {} ;",
    "find . -execdir rm {} +",
    "sed -i s/a/b/ file",
    "git push",
    "git commit -m x",
    "git checkout main",
    "$(rm -rf /)",
    "echo `whoami`",
    "echo $HOME",
    "ls | xargs rm",
    "(rm -rf /)",
    "ls\nrm -rf /",
    "rm -rf /",
    "mv a b",
    "cp a b",
    "touch a",
    "",
    "   ",
    "git",
  ]) {
    assert.equal(isReadOnlyBashCommand(command), false, JSON.stringify(command));
  }
});

test("policy: build asks for mutations and unknown tools, allows reads", () => {
  const build = { ...BASE, mode: "build" as const };
  assert.equal(decideToolCall("read", {}, build), "allow");
  assert.equal(decideToolCall("grep", {}, build), "allow");
  assert.equal(decideToolCall("find", {}, build), "allow");
  assert.equal(decideToolCall("ls", {}, build), "allow");
  assert.equal(decideToolCall("bash", { command: "ls -la" }, build), "allow");
  assert.equal(decideToolCall("bash", { command: "rm -rf /" }, build), "ask");
  assert.equal(decideToolCall("edit", { path: "a" }, build), "ask");
  assert.equal(decideToolCall("write", { path: "a" }, build), "ask");
  assert.equal(decideToolCall("mcp__custom", {}, build), "ask");
});

test("policy: edit allows file mutations but still asks for bash and unknown tools", () => {
  const edit = { ...BASE, mode: "edit" as const };
  assert.equal(decideToolCall("edit", {}, edit), "allow");
  assert.equal(decideToolCall("write", {}, edit), "allow");
  assert.equal(decideToolCall("bash", { command: "rm -rf /" }, edit), "ask");
  assert.equal(decideToolCall("mcp__custom", {}, edit), "ask");
});

test("policy: yolo allows everything, including unknown tools", () => {
  const yolo = { ...BASE, mode: "yolo" as const };
  assert.equal(decideToolCall("edit", {}, yolo), "allow");
  assert.equal(decideToolCall("bash", { command: "rm -rf /" }, yolo), "allow");
  assert.equal(decideToolCall("mcp__custom", {}, yolo), "allow");
});

test("policy: planEnabled denies every non-read call in any mode", () => {
  for (const mode of ["build", "edit", "yolo"] as const) {
    const plan = { ...BASE, mode, planEnabled: true };
    assert.equal(decideToolCall("read", {}, plan), "allow", mode);
    assert.equal(decideToolCall("bash", { command: "ls" }, plan), "allow", mode);
    assert.equal(decideToolCall("bash", { command: "rm -rf /" }, plan), "deny", mode);
    assert.equal(decideToolCall("edit", {}, plan), "deny", mode);
    assert.equal(decideToolCall("write", {}, plan), "deny", mode);
    assert.equal(decideToolCall("mcp__custom", {}, plan), "deny", mode);
  }
});

test("policy: 'allow always' is per tool name and overrides ask in build/edit", () => {
  const always = { ...BASE, mode: "build" as const, alwaysAllowed: new Set(["bash"]) };
  assert.equal(decideToolCall("bash", { command: "rm -rf /" }, always), "allow");
  assert.equal(decideToolCall("write", {}, always), "ask");
  // Plan mode stays a hard gate even for an always-allowed tool.
  assert.equal(decideToolCall("bash", { command: "rm" }, { ...always, planEnabled: true }), "deny");
});
