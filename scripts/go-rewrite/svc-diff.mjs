// svc-diff.mjs: differential test — run the same recorded calls against the
// Node service and the Go service on identical fixture directories and
// compare the result JSON plus the resulting on-disk state (tree snapshot).
//
// Usage: node scripts/go-rewrite/svc-diff.mjs <svc> [--all]
//   svc ∈ system | file | filewatcher | media
//
// The Go side is driven through `go run ./cmd/svc-diff-harness`, which builds
// a Registry with the step-3.2 services and speaks the real RPC framing over
// stdio (the same codec the gateway uses). The Node side is driven through
// ProxyChannel over createQueuePair (svc-capture.mjs's transport). Both
// sides get a FRESH fixture tree, so the comparison includes on-disk writes
// (.nexignore auto-create/transform/write).
//
// Timing-dependent fields (latencyMs, checkedAt, mtimeMs) and machine paths
// are normalized with the same rules as the captures.
import { mkdirSync, rmSync, writeFileSync, readdirSync, statSync } from "node:fs";
import { join, resolve as resolvePath, relative } from "node:path";
import { realpathSync } from "node:fs";
import { setTimeout as delay } from "node:timers/promises";
import { spawn } from "node:child_process";

const HERE = new URL(".", import.meta.url).pathname;
const ROOT = realpathSync(resolvePath(HERE, "../.."));
const GO = join(ROOT, "go");
const FIXTURES = join(ROOT, "go/testdata/services/fixtures");

const { ProxyChannel, ChannelServer, ChannelClient, createQueuePair } = await import(
  join(ROOT, "packages/rpc/src/index.js")
);

// fixture builder — identical shape to svc-capture.mjs (both engines must see
// the same tree, byte for byte)
function buildFixtureWorkspace(root) {
  const dir = join(root, "workspace");
  const files = {
    "README.md": "# workspace\nhello\n",
    "src/main.ts": "export function main() {}\n",
    "src/util/helper.ts": "export const x = 1;\n",
    "src/text/big.txt": "a".repeat(300 * 1024),
    "src/binary/blob.bin": null,
    "docs/note.md": "note\n",
    ".hidden/root-hidden.txt": "hidden\n",
    ".hidden/nested/file.txt": "nested\n",
    "media/sample.txt": "not media\n",
    "node_modules/dep/index.js": "dep\n",
    "coverage/lcov.info": "cov\n",
    "build/out.js": "out\n",
  };
  for (const [rel, content] of Object.entries(files)) {
    const p = join(dir, rel);
    mkdirSync(join(p, ".."), { recursive: true });
    if (content === null) {
      writeFileSync(p, Buffer.from([0x00, 0x41, 0x42, 0xff, 0xfe, 0x01, 0x1f, 0x7f, 0x0a]));
    } else {
      writeFileSync(p, content);
    }
  }
  writeFileSync(join(dir, ".gitignore"), "build/\n*.log\n!keep.log\n");
  return dir;
}

function buildIgnoreFixture(root) {
  const dir = join(root, "ignore-workspace");
  mkdirSync(join(dir, "sub"), { recursive: true });
  writeFileSync(join(dir, "sub", "file.txt"), "x\n");
  writeFileSync(join(dir, "keep.log"), "kept\n");
  writeFileSync(join(dir, "other.log"), "no\n");
  return dir;
}

function buildFixtures(root) {
  rmSync(root, { recursive: true, force: true });
  mkdirSync(root, { recursive: true });
  return {
    workspace: buildFixtureWorkspace(root),
    ignoreWorkspace: buildIgnoreFixture(root),
  };
}

function treeSnapshot(dir) {
  const entries = [];
  const walk = (p, rel) => {
    let names;
    try {
      names = readdirSync(p).sort();
    } catch {
      return;
    }
    for (const name of names) {
      const full = join(p, name);
      const relName = rel ? `${rel}/${name}` : name;
      let s;
      try {
        s = statSync(full);
      } catch {
        continue;
      }
      if (s.isDirectory()) {
        entries.push(`${relName}/`);
        walk(full, relName);
      } else {
        entries.push(relName);
      }
    }
  };
  walk(dir, "");
  return entries;
}

// normalization: same rules as the captures (machine paths + timing fields)
function normalize(value, fixtureRoot) {
  if (typeof value === "string") {
    let v = value.split(fixtureRoot).join("<FIXTURE>").split(ROOT).join("<ROOT>");
    return v;
  }
  if (Array.isArray(value)) return value.map((v) => normalize(v, fixtureRoot));
  if (value instanceof Uint8Array) return { __bytes: [...value] };
  if (value && typeof value === "object") {
    const out = {};
    for (const [k, v] of Object.entries(value)) {
      out[k] = ["mtimeMs", "checkedAt", "latencyMs"].includes(k)
        ? `<${k}>`
        : k === "error" && typeof v === "string"
          ? "<error>"
          : normalize(v, fixtureRoot);
    }
    return out;
  }
  return value;
}

// ---------------------------------------------------------------------------
// Node side
// ---------------------------------------------------------------------------

async function runNode(svcName, cases, fixtureRoot) {
  const { createSystemService } = await import(
    join(ROOT, "packages/services/src/system/systemService.js")
  );
  const { createFileService } = await import(
    join(ROOT, "packages/services/src/file/fileService.js")
  );
  const { createFileWatcherService } = await import(
    join(ROOT, "packages/services/src/fileWatcher/fileWatcherService.js")
  );
  const { createMediaPreviewService } = await import(
    join(ROOT, "packages/services/src/media-preview/mediaPreview.js")
  );

  const builders = {
    system: () => ({ service: createSystemService(), channel: "system" }),
    file: () => ({ service: createFileService(), channel: "file" }),
    filewatcher: () => ({ service: createFileWatcherService(), channel: "file-watcher" }),
    media: () => {
      const fileService = createFileService();
      return {
        service: createMediaPreviewService({ fileService, inlineMaxBytes: 8 * 1024 * 1024 }),
        channel: "media-preview",
      };
    },
  };
  const { service, channel } = builders[svcName]();

  const serverChannel = ProxyChannel.fromService(service);
  const [protoA, protoB] = createQueuePair();
  const server = new ChannelServer(protoB, "ctx", 5000, true);
  const client = new ChannelClient(protoA);
  server.registerChannel(channel, serverChannel);
  server.ready();

  const results = [];
  for (const c of cases) {
    const rawChannel = client.getChannel(channel);
    try {
      const result = await rawChannel.call(c.method, c.args);
      results.push({ ok: true, result: normalize(result, fixtureRoot) });
    } catch (error) {
      results.push({
        ok: false,
        error: { name: error.name, message: normalize(error.message, fixtureRoot) },
      });
    }
  }
  if (svcName === "filewatcher" && service.disposeAll) service.disposeAll();
  return results;
}

// ---------------------------------------------------------------------------
// Go side: the harness reads cases on stdin, writes results on stdout
// ---------------------------------------------------------------------------

function runGo(svcName, cases, fixtureRoot) {
  return new Promise((resolvePromise, reject) => {
    const child = spawn("go", ["run", "./cmd/svc-diff-harness", svcName], {
      cwd: GO,
      stdio: ["pipe", "pipe", "inherit"],
    });
    let stdout = "";
    child.stdout.on("data", (chunk) => (stdout += chunk.toString()));
    child.on("error", reject);
    child.on("close", (code) => {
      if (code !== 0) {
        reject(new Error(`go harness exited ${code}`));
        return;
      }
      try {
        resolvePromise(JSON.parse(stdout));
      } catch (e) {
        reject(new Error(`go harness output not JSON: ${stdout.slice(0, 200)}`));
      }
    });
    child.stdin.write(JSON.stringify({ fixtureRoot, cases }));
    child.stdin.end();
  });
}

// ---------------------------------------------------------------------------
// scenarios: identical to the capture driver (minus watcher events which are
// timing-based and covered by the filewatcher package tests)
// ---------------------------------------------------------------------------

function scenariosFor(svcName, fixtures) {
  const ws = fixtures.workspace;
  const ig = fixtures.ignoreWorkspace;
  const igArg = (p) => ({ ...p, rootPath: join(ig, p.rootPathRelative) });
  const cases = [];
  const add = (method, args) => cases.push({ method, args });
  const addPath = (method, path, extra = {}) => add(method, [{ path, ...extra }]);

  if (svcName === "system") {
    add("info", []);
    add("listIntegratedTerminalShells", []);
    add("probeIntranet", [{ targets: [] }]);
    add("probeIntranet", [{ targets: [{ host: "127.0.0.1", port: 1, timeoutMs: 200 }] }]);
    add("probeIntranet", [
      {
        targets: [
          { host: "  ", port: 1 },
          { kind: "tcp", host: "127.0.0.1", port: 1 },
        ],
        attempts: 1,
      },
    ]);
    add("probeIntranet", [
      {
        targets: [{ kind: "service", url: "http://127.0.0.1:1/", timeoutMs: 200 }],
        attempts: 1,
        requiredSuccessCount: 2,
      },
    ]);
    add("probeIntranet", [
      {
        targets: [
          { kind: "service", url: "ftp://x/", timeoutMs: 100 },
          { host: "localhost", port: 1 },
        ],
        attempts: 2,
      },
    ]);
  }

  if (svcName === "file") {
    add("readdir", [{ path: ws }]);
    add("readdir", [{ path: ws, includeHidden: true }]);
    add("readdir", [{ path: join(ws, "nope") }]);
    addPath("stat", join(ws, "README.md"));
    addPath("stat", join(ws, "src"));
    add("checkFilesExist", [{ paths: [join(ws, "README.md"), join(ws, "nope.txt")] }]);
    add("checkFilesExist", [{ paths: Array.from({ length: 16 }, (_, i) => `p${i}`) }]);
    addPath("resolvePath", join(ws, "src", ".."));
    addPath("resolvePath", join(ws, "missing"));
    addPath("readTextFile", join(ws, "README.md"));
    addPath("readTextFile", join(ws, "README.md"), { offset: 2, length: 5 });
    addPath("readTextFile", join(ws, "README.md"), { offset: 1000 });
    addPath("readTextFile", join(ws, "src", "text", "big.txt"), { length: 1000000 });
    addPath("readTextFile", join(ws, "src", "binary", "blob.bin"));
    addPath("readTextFile", join(ws, "src"));
    add("readFileRange", [{ path: join(ws, "src", "binary", "blob.bin"), offset: 0, length: 4 }]);
    add("readFileRange", [{ path: join(ws, "src", "binary", "blob.bin"), offset: 3, length: 100 }]);
    add("readFileRange", [{ path: join(ws, "src", "binary", "blob.bin"), offset: 100, length: 8 }]);
    addPath("readBinaryPreview", join(ws, "src", "binary", "blob.bin"));
    addPath("readBinaryPreview", join(ws, "src"));
    addPath("readMediaPreview", join(ws, "README.md"));
    add("readMediaPreview", [{ path: join(ws, "src", "text", "big.txt"), maxBytes: 10 }]);
    add("listWorkspaceFilesLength", [{ rootPath: ws }]);
    add("listWorkspaceFilesLength", [{ rootPath: ig }]);
    add("listWorkspaceFilesRange", [{ rootPath: ws, offset: 0, length: 100000 }]);
    add("listWorkspaceFilesRange", [{ rootPath: ws, offset: 3, length: 5 }]);
    add("listWorkspaceFilesRange", [{ rootPath: ws, offset: 10000, length: 5 }]);
    add("searchWorkspaceFiles", [{ rootPath: ws, query: "" }]);
    add("searchWorkspaceFiles", [{ rootPath: ws, query: "readme" }]);
    add("searchWorkspaceFiles", [{ rootPath: ws, query: "helper" }]);
    add("searchWorkspaceFiles", [{ rootPath: ws, query: "sr", limit: 3 }]);
    add("searchWorkspaceFiles", [{ rootPath: ws, query: "zzz-no-match" }]);
    add("searchWorkspaceFiles", [{ rootPath: ws, query: "readme", limit: 0 }]);
    add("searchWorkspaceFiles", [
      { rootPath: ws, query: "helper", workspaceIdentity: "identity-1" },
    ]);
    add("readWorkspaceFileSearchIgnore", [{ rootPath: ig }]);
    add("readWorkspaceFileSearchIgnore", [{ rootPath: ig }]);
    add("applyWorkspaceFileSearchIgnoreTransform", [{ rootPath: ig, transform: "sync-gitignore" }]);
    add("applyWorkspaceFileSearchIgnoreTransform", [{ rootPath: ig, transform: "reset-defaults" }]);
    add("writeWorkspaceFileSearchIgnore", [{ rootPath: ig, content: "*.log\n" }]);
    add("readWorkspaceFileSearchIgnore", [{ rootPath: ig }]);
  }

  if (svcName === "filewatcher") {
    // watch lifecycle only; events are timing-based (package tests cover
    // them). Paths are per-engine (the scenario function receives the root).
    const dir = join(fixtures.root, "watch-dir");
    mkdirSync(dir, { recursive: true });
    add("watch", [{ path: dir }]);
    add("unwatch", [{ id: "999" }]);
    add("watch", [{ path: join(fixtures.root, "missing-dir") }]);
  }

  if (svcName === "media") {
    mkdirSync(join(ws, "media"), { recursive: true });
    writeFileSync(
      join(ws, "media", "clip.mp4"),
      Buffer.from([0x00, 0x00, 0x00, 0x18, 0x66, 0x74, 0x79, 0x70]),
    );
    writeFileSync(join(ws, "media", "song.mp3"), Buffer.from([0x49, 0x44, 0x33, 0x04]));
    add("prepare", [{ path: join(ws, "media", "clip.mp4"), expectedKind: "video" }]);
    add("prepare", [{ path: join(ws, "media", "song.mp3"), expectedKind: "audio" }]);
    add("prepare", [{ path: join(ws, "media", "clip.mp4"), expectedKind: "audio" }]);
    add("prepare", [{ path: join(ws, "README.md"), expectedKind: "video" }]);
    add("prepare", [{ path: join(ws, "src"), expectedKind: "video" }]);
    add("prepare", [{ path: join(ws, "media", "missing.mp4"), expectedKind: "video" }]);
  }
  return cases;
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

async function main() {
  const args = process.argv.slice(2);
  const all = args.includes("--all");
  const services = all ? ["system", "file", "filewatcher", "media"] : [args[0]].filter(Boolean);
  if (!services.length || !["system", "file", "filewatcher", "media"].includes(services[0])) {
    console.error(
      "usage: node scripts/go-rewrite/svc-diff.mjs <system|file|filewatcher|media> [--all]",
    );
    process.exit(2);
  }

  let totalMismatches = 0;
  for (const svcName of services) {
    const nodeRoot = join(FIXTURES, "diff-node");
    const goRoot = join(FIXTURES, "diff-go");
    buildFixtures(nodeRoot);
    buildFixtures(goRoot);
    // the watch-dir for the watcher scenario
    mkdirSync(join(nodeRoot, "watch-dir"), { recursive: true });
    mkdirSync(join(goRoot, "watch-dir"), { recursive: true });

    // rewrite scenario paths per engine fixture root
    const makeCases = (root) =>
      scenariosFor(svcName, {
        root,
        workspace: join(root, "workspace"),
        ignoreWorkspace: join(root, "ignore-workspace"),
      }).map((c) => ({
        method: c.method,
        args: JSON.parse(JSON.stringify(c.args).split(nodeRoot).join(root)),
      }));
    const nodeCases = makeCases(nodeRoot);
    const goCases = makeCases(goRoot);

    const nodeResults = await runNode(svcName, nodeCases, nodeRoot);
    const goResults = await runGo(svcName, goCases, goRoot);

    // key order is not part of the contract (JS objects are insertion-ordered,
    // Go maps are not) — compare structurally with sorted keys.
    const canonical = (v) => JSON.stringify(sortKeys(v));
    function sortKeys(v) {
      if (Array.isArray(v)) return v.map(sortKeys);
      if (v && typeof v === "object") {
        const out = {};
        for (const k of Object.keys(v).sort()) out[k] = sortKeys(v[k]);
        return out;
      }
      return v;
    }

    let mismatches = 0;
    for (let i = 0; i < nodeCases.length; i++) {
      const nodeNorm = canonical(nodeResults[i]);
      const goNorm = canonical(goResults[i]);
      if (nodeNorm !== goNorm) {
        mismatches++;
        totalMismatches++;
        console.error(`MISMATCH ${svcName}.${nodeCases[i].method} [case ${i}]:`);
        console.error(`  node: ${nodeNorm.slice(0, 400)}`);
        console.error(`  go:   ${goNorm.slice(0, 400)}`);
      }
    }
    // on-disk state comparison (tree snapshots of the fixture roots)
    const nodeTree = JSON.stringify(treeSnapshot(nodeRoot));
    const goTree = JSON.stringify(treeSnapshot(goRoot));
    if (nodeTree !== goTree) {
      mismatches++;
      totalMismatches++;
      console.error(`MISMATCH ${svcName} on-disk state:`);
      console.error(`  node: ${nodeTree.slice(0, 400)}`);
      console.error(`  go:   ${goTree.slice(0, 400)}`);
    }
    console.log(`${svcName}: ${nodeCases.length} calls, ${mismatches} mismatches`);
    rmSync(nodeRoot, { recursive: true, force: true });
    rmSync(goRoot, { recursive: true, force: true });
  }
  process.exit(totalMismatches === 0 ? 0 : 1);
}

await main();
