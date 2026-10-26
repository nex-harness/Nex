/**
 * Materializes skill and prompt-template bundles (docs/next/config-mcp-skills.md
 * P2, research §B "Skills"/"Prompt templates").
 *
 * Layout per bundle: `<data>/bundles/<hex>/files/<name>/<path>` (pi needs real
 * files, and a skill's directory is the base for its references; the per-name
 * root also keeps two skills of one digest apart) and
 * `<data>/bundles/<hex>/manifest.json` (what was verified). The digest is
 * recomputed from the received files before anything is written; a mismatch
 * drops the bundle and reports `bundle-integrity` — the runner never loads
 * content the server cannot vouch for.
 */

import { createHash } from "node:crypto";
import { mkdir, readFile, rename, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import type { WireBundle } from "./runtimeConfig.js";

export interface MaterializedBundles {
  /** Directories to pass as `additionalSkillPaths` (one per skill bundle). */
  readonly skillPaths: ReadonlyArray<string>;
  /** Directories holding `*.md` prompt templates. */
  readonly promptPaths: ReadonlyArray<string>;
  readonly issues: ReadonlyArray<{ scope: string; path: string; code: string; message: string }>;
}

/**
 * Content digest of a bundle: `sha256:` over the sorted `path\0sha256(content)\n`
 * listing — the same rule the server used to address it.
 */
export function digestFiles(files: ReadonlyArray<{ path: string; content: string }>): string {
  const sorted = [...files].sort((left, right) =>
    left.path < right.path ? -1 : left.path > right.path ? 1 : 0,
  );
  const sum = createHash("sha256");
  for (const file of sorted) {
    sum.update(
      `${file.path}\0${createHash("sha256").update(file.content, "utf8").digest("hex")}\n`,
    );
  }
  return `sha256:${sum.digest("hex")}`;
}

/** One bundle-relative path that cannot escape its bundle directory. */
export function safeBundlePath(path: string): string | null {
  const normalized = path.replace(/\\/gu, "/").replace(/^\.\//u, "");
  if (!normalized || normalized.startsWith("/")) return null;
  for (const segment of normalized.split("/")) {
    if (!segment || segment === "." || segment === "..") return null;
  }
  return normalized;
}

/**
 * Writes every bundle of one generation that is referenced by a skill or prompt
 * ref, verifying digests. Materialization is idempotent: an existing verified
 * bundle directory is reused.
 */
export async function materializeBundles(
  dataDir: string,
  bundles: ReadonlyArray<WireBundle>,
): Promise<MaterializedBundles> {
  const skillPaths: string[] = [];
  const promptPaths: string[] = [];
  const issues: Array<{ scope: string; path: string; code: string; message: string }> = [];
  for (const bundle of bundles) {
    const hex = bundle.digest.replace(/^sha256:/u, "");
    const directory = join(dataDir, "bundles", hex);
    const filesDir = join(directory, "files");
    const actual = digestFiles(bundle.files);
    if (actual !== bundle.digest || !/^[a-f0-9]{64}$/u.test(hex)) {
      issues.push({
        scope: "bundle",
        path: bundle.name,
        code: "bundle-integrity",
        message: `bundle ${bundle.name} digest mismatch (${actual})`,
      });
      continue;
    }
    for (const file of bundle.files) {
      if (!safeBundlePath(file.path)) {
        issues.push({
          scope: "bundle",
          path: `${bundle.name}/${file.path}`,
          code: "bundle-integrity",
          message: "bundle contains an invalid file path",
        });
      }
    }
    if (issues.some((issue) => issue.path.startsWith(`${bundle.name}/`))) continue;
    try {
      await writeBundle(directory, bundle);
    } catch (error) {
      issues.push({
        scope: "bundle",
        path: bundle.name,
        code: "bundle-write-failed",
        message: error instanceof Error ? error.message : String(error),
      });
      continue;
    }
    // Only prompt bundles are prompt paths: a skill directory carries SKILL.md,
    // which pi would otherwise list as a `/SKILL` template.
    if (bundle.kind === "prompt") promptPaths.push(filesDir);
    else skillPaths.push(join(filesDir, bundle.name));
  }
  return {
    skillPaths: Object.freeze(skillPaths),
    promptPaths: Object.freeze(promptPaths),
    issues: Object.freeze(issues),
  };
}

async function writeBundle(directory: string, bundle: WireBundle): Promise<void> {
  const manifestPath = join(directory, "manifest.json");
  try {
    const existing = JSON.parse(await readFile(manifestPath, "utf8")) as { digest?: string };
    if (existing.digest === bundle.digest) return;
  } catch {
    // Missing or unreadable manifest: rewrite below.
  }
  const temporary = `${directory}.${process.pid}.${Date.now()}.tmp`;
  await rm(temporary, { recursive: true, force: true });
  try {
    for (const file of bundle.files) {
      const relative = safeBundlePath(file.path);
      if (!relative) continue;
      // Prompt bundles keep their files directly under `files/` (pi scans the
      // directory and would descend into a skill folder, but a prompt directory
      // must contain the `*.md` templates); skill bundles get their own root.
      const target = join(
        temporary,
        "files",
        bundle.kind === "prompt" ? relative : join(bundle.name, relative),
      );
      await mkdir(join(target, ".."), { recursive: true });
      await writeFile(target, file.content, { mode: 0o600 });
    }
    await mkdir(temporary, { recursive: true });
    await writeFile(
      join(temporary, "manifest.json"),
      JSON.stringify({ name: bundle.name, kind: bundle.kind, digest: bundle.digest }, null, 2) +
        "\n",
      { mode: 0o600 },
    );
    await rm(directory, { recursive: true, force: true });
    await mkdir(join(directory, ".."), { recursive: true });
    await rename(temporary, directory);
  } catch (error) {
    await rm(temporary, { recursive: true, force: true });
    throw error;
  }
}
