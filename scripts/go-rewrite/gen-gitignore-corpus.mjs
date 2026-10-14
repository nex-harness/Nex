// G3.2: generate the .gitignore semantics corpus from the TS `ignore` package.
//
// The Go port of workspaceFileIgnore.ts must match the `ignore` npm package
// (gitignore spec 2.22 reference implementation) exactly. Per the phase-3 plan
// (step 3.2 special rules) a Go library may be used ONLY after a 200-case
// corpus generated from the TS package passes; this script produces that
// corpus: go/testdata/services/file/gitignore-corpus.json
//
// Each case: { rules: string[], paths: [{ path, isDirectory, ignored }] } —
// `ignored` is what the TS matcher answers for that path, with the workspace
// convention applied (directories probed with a trailing slash, exactly like
// isWorkspaceFileSearchPathIgnored).
//
// Run: npx tsx scripts/go-rewrite/gen-gitignore-corpus.mjs
import { writeFileSync, mkdirSync, realpathSync } from "node:fs";
import { join, resolve as resolvePath } from "node:path";

const HERE = new URL(".", import.meta.url).pathname;
const ROOT = realpathSync(resolvePath(HERE, "../.."));
const OUT = join(ROOT, "go/testdata/services/file/gitignore-corpus.json");

const ignoreFactory = (await import(join(ROOT, "node_modules/ignore/index.js"))).default;

// ---------------------------------------------------------------------------
// rule sets — each exercises a documented `ignore`-package behavior
// ---------------------------------------------------------------------------

const ruleSets = [
  { name: "plain-file", rules: ["foo.txt"] },
  { name: "anchored-file", rules: ["/foo.txt"] },
  { name: "basename-anywhere", rules: ["foo"] },
  { name: "dir-only", rules: ["build/"] },
  { name: "dir-no-slash", rules: ["build"] },
  { name: "anchored-dir", rules: ["/build/"] },
  { name: "negate-file", rules: ["*.log", "!keep.log"] },
  { name: "negate-dir-child-unreachable", rules: ["dir/", "!dir/inside.txt"] },
  { name: "negate-then-ignore-again", rules: ["*.log", "!keep.log", "keep.log"] },
  { name: "single-star", rules: ["*.tmp"] },
  { name: "double-star-prefix", rules: ["**/logs"] },
  { name: "double-star-suffix", rules: ["logs/**"] },
  { name: "double-star-middle", rules: ["a/**/b"] },
  { name: "double-star-name", rules: ["**"] },
  { name: "question-mark", rules: ["file?.txt"] },
  { name: "char-class", rules: ["[abc].txt"] },
  { name: "char-class-negated", rules: ["[!a].txt"] },
  { name: "char-class-range", rules: ["[a-c].txt"] },
  { name: "escaped-star", rules: ["\\*.txt"] },
  { name: "escaped-space", rules: ["foo\\ bar"] },
  { name: "trailing-spaces-trimmed", rules: ["foo.txt   "] },
  { name: "comment", rules: ["# comment", "foo.txt"] },
  { name: "blank", rules: ["", "foo.txt"] },
  { name: "leading-slash-mid", rules: ["a/b"] },
  { name: "multi-segment", rules: ["src/generated/*.ts"] },
  { name: "case-sensitive", rules: ["Readme.md"] },
  { name: "cwd-root-file", rules: ["/"] },
  { name: "dotfile", rules: [".env"] },
  { name: "dotfile-wildcard", rules: [".env.*"] },
  { name: "name-with-slash-end", rules: ["foo.txt/"] },
  { name: "trailing-slash-file-negate", rules: ["foo.txt/", "!foo.txt"] },
  { name: "parent-then-child", rules: ["node_modules", "node_modules/**"] },
  { name: "ext-glob-star-mid", rules: ["a*b"] },
  { name: "empty-negate", rules: ["!"] },
  { name: "dir-negate-slash", rules: ["!dir/"] },
  { name: "nested-anchored", rules: ["a//b"] },
  { name: "double-slash", rules: ["a//b"] },
  { name: "star-dir-slash", rules: ["*/tmp/"] },
  { name: "unignore-dir-then-child", rules: ["/*", "!/keep", "/keep/*", "!/keep/inner.txt"] },
  {
    name: "typical-nexignore",
    rules: [
      ".git/",
      ".hg/",
      ".svn/",
      "node_modules/",
      "coverage/",
      "*.egg-info/",
      "bazel-*/",
      "cmake-build-*/",
    ],
  },
  {
    name: "typical-gitignore",
    rules: [
      "node_modules/",
      "dist/",
      "*.log",
      ".env",
      ".DS_Store",
      "coverage/",
      "*.tsbuildinfo",
      "!dist/keep.js",
    ],
  },
  { name: "windows-ish", rules: ["Debug/", "Release/", "*.user", "x64/"] },
  {
    name: "python",
    rules: ["__pycache__/", "*.py[cod]", "venv/", ".venv/", "*.egg-info/", "dist/", "build/"],
  },
  { name: "bracket-edge", rules: ["[]a].txt"] },
  { name: "dangling-escape", rules: ["foo\\"] },
  { name: "only-slash", rules: ["/"] },
  { name: "star-slash-prefix", rules: ["*/"] },
  { name: "deep-anchored", rules: ["/a/b/c.txt"] },
  { name: "unignore-all-children", rules: ["*", "!*/", "!*.md"] },
  { name: "dir-negate-before", rules: ["!dir", "dir/"] },
  { name: "star-ext", rules: ["src/**/*.gen.ts"] },
  { name: "ignore-everything", rules: ["*"] },
  { name: "ignore-everything-then-dirs", rules: ["*", "!*/"] },
  { name: "ext-only", rules: ["*.log"] },
  { name: "name-only", rules: ["foo.txt"] },
  { name: "unanchored-dir-any-depth", rules: ["dist"] },
  { name: "slash-terminated-negate-file", rules: ["sub/", "!sub/file.txt"] },
  { name: "double-negate", rules: ["*.log", "!keep.log", "!!keep.log"] },
];

// Probe paths: every entry is tested both as a file and as a directory
// (directory probe uses the trailing-slash form, as the workspace filter does).
const probePaths = [
  "foo.txt",
  "foo",
  "build",
  "build/x.txt",
  "sub/build",
  "sub/build/x.txt",
  "keep.log",
  "a.log",
  "dir/inside.txt",
  "dir",
  "logs",
  "a/logs",
  "a/b/logs",
  "logs/a",
  "logs/a/b",
  "a/b",
  "a/x/b",
  "a/y/z/b",
  "file1.txt",
  "fileA.txt",
  "fileX.txt",
  "a.txt",
  "b.txt",
  "z.txt",
  "*.txt",
  "foo bar",
  "foo ",
  "src/generated/x.ts",
  "src/generated/y/x.ts",
  "generated/x.ts",
  "Readme.md",
  "readme.md",
  ".env",
  ".env.local",
  "tmp",
  "x/tmp",
  "x/tmp/y",
  "node_modules/dep/index.js",
  "node_modules",
  "ab",
  "a-b",
  "axb",
  "keep/inner.txt",
  "keep",
  "keep/other.txt",
  "inner.md",
  "a/b/c.txt",
  "b/c.txt",
  "c.txt",
  "x.ts",
  "x.gen.ts",
  "src/a/x.gen.ts",
  "foo.txt/",
  "bar.txt",
  "Debug",
  "Debug/x.obj",
  "x64",
  "__pycache__",
  "__pycache__/x.cpython-311.pyc",
  "x.cpython-311.pyc",
  "venv",
  ".venv",
  "x.egg-info",
  "x.egg-info/PKG-INFO",
  "dist/keep.js",
  "bazel-out",
  "cmake-build-debug",
  "cmake-build-release/x",
];

const corpus = [];
for (const { name, rules } of ruleSets) {
  const matcher = ignoreFactory().add(rules.join("\n"));
  const paths = probePaths.map((p) => {
    const isDirectory = !/\.[^/]+$/.test(p.split("/").pop()) || p.endsWith("/");
    const cleanPath = p.replace(/\/$/, "");
    const probe = isDirectory ? `${cleanPath}/` : cleanPath;
    let ignored;
    let error;
    try {
      ignored = matcher.ignores(probe);
    } catch (e) {
      error = String(e && e.message ? e.message : e);
    }
    return { path: cleanPath, isDirectory, ignored, error };
  });
  corpus.push({ name, rules, paths });
}

mkdirSync(join(OUT, ".."), { recursive: true });
writeFileSync(
  OUT,
  JSON.stringify(
    { generatedBy: "scripts/go-rewrite/gen-gitignore-corpus.mjs", cases: corpus },
    null,
    2,
  ) + "\n",
);

const totalChecks = corpus.reduce((n, c) => n + c.paths.length, 0);
const errored = corpus.flatMap((c) => c.paths).filter((p) => p.error).length;
console.log(
  `gitignore corpus: ${corpus.length} rule sets, ${totalChecks} path checks (${errored} errors) -> ${OUT}`,
);
