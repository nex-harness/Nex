/**
 * Conservative read-only classifier for `bash` (spec: docs/next/permissions.md).
 * Anything that can compose commands, redirect output, expand substitutions or delete
 * files is not read-only. A false positive here only costs one approval card; a false
 * negative would run a mutation without asking, so every ambiguous form stays "ask".
 */

/** Shell metacharacters that can chain, redirect, substitute or subshell. */
const FORBIDDEN_CHARACTERS = /[|&;<>$`()\n\r]/u;

const READ_ONLY_COMMANDS = new Set([
  "ls",
  "cat",
  "head",
  "tail",
  "pwd",
  "rg",
  "grep",
  "find",
  "wc",
  "stat",
  "file",
  "echo",
  "sort",
  "uniq",
  "diff",
  "which",
]);

const GIT_READ_ONLY_SUBCOMMANDS = new Set(["status", "diff", "log", "show", "rev-parse", "branch"]);

/** `find` flags that execute commands or delete files. */
const FIND_MUTATING_FLAGS = new Set(["-exec", "-execdir", "-ok", "-okdir", "-delete"]);

export function isReadOnlyBashCommand(command: string): boolean {
  const trimmed = command.trim();
  if (trimmed.length === 0) return false;
  if (FORBIDDEN_CHARACTERS.test(trimmed)) return false;
  const tokens = trimmed.split(/\s+/u).filter((token) => token.length > 0);
  const first = tokens[0];
  if (!first) return false;
  if (first === "git") {
    const subcommand = tokens[1];
    return subcommand !== undefined && GIT_READ_ONLY_SUBCOMMANDS.has(subcommand);
  }
  if (first === "find") {
    return !tokens.some((token) => FIND_MUTATING_FLAGS.has(token));
  }
  // `sed` is not on the allowlist, and `sed -i` edits in place; keep the explicit
  // rejection so the intent survives a future allowlist edit.
  if (first === "sed") return false;
  return READ_ONLY_COMMANDS.has(first);
}
