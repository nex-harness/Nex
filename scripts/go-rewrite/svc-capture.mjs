/* eslint-disable max-lines -- svc 行为录制：一个脚本按服务名驱动 Node 侧全部 method，拆开会让录制上下文断裂 */
// G3 (step 3.2, recipe sub-step b): behavior capture for the Tier-1 services.
//
// Drives the REAL Node service instances through the real RPC layer
// (ProxyChannel.fromService + ChannelServer/ChannelClient over createQueuePair,
// the same transport the WebSocket uses with framing stripped) and saves
// go/testdata/services/<svc>/<method>/{in,out}.json golden files. Serialization
// is included, so what is recorded is exactly what the Go channel must produce.
//
// Usage: node scripts/go-rewrite/svc-capture.mjs <svc> [method...]
//   svc ∈ system | file | filewatcher | media
//
// The fixtures under go/testdata/services/fixtures/ are rebuilt before each run
// so captures are deterministic. Inputs that must vary per machine (homedir,
// temp paths) are normalized on write: absolute temp paths are replaced with
// "<TMP>" and mtimes with 0 — svc-diff.mjs applies the same normalization when
// comparing Go against Node, so both sides reduce to the same shape.
//
// This script must run via tsx: the services package is TS-only (the committed
// dist imports src paths). record-rpc.mjs uses the built rpc dist; here we
// import the rpc source through tsx as well for one consistent import graph.
import {
  mkdirSync,
  rmSync,
  writeFileSync,
  readdirSync,
  statSync,
  existsSync,
  realpathSync,
} from "node:fs";
import { join, resolve as resolvePath } from "node:path";
import { tmpdir } from "node:os";
import { setTimeout as delay } from "node:timers/promises";

// realpath: $HOME/Developer may be a symlink onto another mount; a stale
// symlinked prefix makes import() fail even though the file exists.
const HERE = new URL(".", import.meta.url).pathname;
const ROOT = realpathSync(resolvePath(HERE, "../.."));
const OUT = join(ROOT, "go/testdata/services");
const FIXTURES = join(OUT, "fixtures");
const TMP = tmpdir();

const { ProxyChannel, ChannelServer, ChannelClient, createQueuePair, Event } = await import(
  join(ROOT, "packages/rpc/src/index.js")
);

const descriptors = await import(join(ROOT, "packages/services/src/descriptors.js"));

// ---------------------------------------------------------------------------
// fixture construction (shared with svc-diff.mjs by rebuilding the same shape)
// ---------------------------------------------------------------------------

/** Deterministic fixture workspace used by file/media/watcher captures. */
export function buildFixtureWorkspace(root) {
  const dir = join(root, "workspace");
  const files = {
    "README.md": "# workspace\nhello\n",
    "src/main.ts": "export function main() {}\n",
    "src/util/helper.ts": "export const x = 1;\n",
    "src/text/big.txt": "a".repeat(300 * 1024),
    "src/binary/blob.bin": null, // written with bytes below
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
      // binary: NUL byte + high-bit bytes + control chars
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

export function buildFixtures() {
  rmSync(FIXTURES, { recursive: true, force: true });
  mkdirSync(FIXTURES, { recursive: true });
  const root = join(FIXTURES, "root");
  mkdirSync(root, { recursive: true });
  return {
    root,
    workspace: buildFixtureWorkspace(root),
    ignoreWorkspace: buildIgnoreFixture(root),
  };
}

// ---------------------------------------------------------------------------
// capture helpers
// ---------------------------------------------------------------------------

/** Replace machine-specific strings so goldens are stable across machines. */
function normalize(value) {
  if (typeof value === "string") {
    return value.split(TMP).join("<TMP>").split(ROOT).join("<ROOT>");
  }
  if (Array.isArray(value)) return value.map(normalize);
  if (value instanceof Uint8Array) return { __bytes: [...value] };
  if (value && typeof value === "object") {
    const out = {};
    for (const [k, v] of Object.entries(value)) {
      // mtimeMs/checkedAt/latencyMs are machine/timing specific; the diff
      // compares presence and shape, not wall-clock values.
      out[k] = k === "mtimeMs" || k === "checkedAt" || k === "latencyMs" ? `<${k}>` : normalize(v);
    }
    return out;
  }
  return value;
}

function errorToJson(error) {
  if (!(error instanceof Error)) return { message: String(error) };
  const out = { name: error.name, message: error.message };
  if (error.code !== undefined) out.code = error.code;
  if (error.kind !== undefined) out.kind = error.kind;
  return out;
}

async function capture(client, channelName, method, args, outFile, opts = {}) {
  const inPath = outFile.replace(/out\.json$/, "in.json");
  mkdirSync(join(outFile, ".."), { recursive: true });
  writeFileSync(
    inPath,
    JSON.stringify({ channel: channelName, method, args: normalize(args) }, null, 2) + "\n",
  );
  const channel = client.getChannel(channelName);
  let out;
  try {
    const result = await channel.call(method, args);
    out = { ok: true, result: normalize(result) };
  } catch (error) {
    out = { ok: false, error: errorToJson(error) };
  }
  if (opts.post) {
    // on-disk state check after the call (tree hash for write-path methods)
    out.post = opts.post();
  }
  writeFileSync(outFile, JSON.stringify(out, null, 2) + "\n");
  return out;
}

function treeSnapshot(dir) {
  const entries = [];
  const walk = (p, rel) => {
    for (const name of readdirSync(p).sort()) {
      const full = join(p, name);
      const relName = rel ? `${rel}/${name}` : name;
      const s = statSync(full);
      entries.push(s.isDirectory() ? `${relName}/` : relName);
      if (s.isDirectory()) walk(full, relName);
    }
  };
  walk(dir, "");
  return normalize(entries);
}

function readFirstLine(path) {
  return existsSync(path) ? readdirSync(path).length : -1;
}

// ---------------------------------------------------------------------------
// service construction: the same instances createLocalServices registers
// ---------------------------------------------------------------------------

async function buildService(svc, fixtures) {
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

  if (svc === "system") return { service: createSystemService(), channel: "system" };
  if (svc === "file") return { service: createFileService(), channel: "file" };
  if (svc === "filewatcher")
    return { service: createFileWatcherService(), channel: "file-watcher" };
  if (svc === "media") {
    const fileService = createFileService();
    return {
      service: createMediaPreviewService({
        fileService,
        inlineMaxBytes: 8 * 1024 * 1024,
      }),
      channel: "media-preview",
    };
  }
  // Step 3.5 (settings family): each local-state service runs with its own
  // NEX_DESKTOP_HOME_DIR so captures never touch the real home, and the
  // fixture home is reused as the workspace for settings-sync.
  if (["setting", "onboarding", "credential", "settings-sync"].includes(svc)) {
    // setting/settings-sync read NEX_DESKTOP_HOME_DIR; credential/onboarding
    // read HOME (paths.ts getAppConfigDir). Set both.
    process.env.NEX_DESKTOP_HOME_DIR = fixtures.root;
    process.env.HOME = fixtures.root;
  }
  if (svc === "setting") {
    const { createSettingService } = await import(
      join(ROOT, "packages/services/src/setting/settingService.js")
    );
    return { service: createSettingService(), channel: "setting" };
  }
  if (svc === "onboarding") {
    const { createOnboardingRecordService } = await import(
      join(ROOT, "packages/services/src/onboarding/onboardingRecordService.js")
    );
    return { service: createOnboardingRecordService(), channel: "onboarding-record" };
  }
  if (svc === "credential") {
    const { createCredentialService } = await import(
      join(ROOT, "packages/services/src/credential/credentialService.js")
    );
    return { service: createCredentialService(), channel: "credential" };
  }
  if (svc === "settings-sync") {
    const { createSettingService } = await import(
      join(ROOT, "packages/services/src/setting/settingService.js")
    );
    const { createSettingsSyncService } = await import(
      join(ROOT, "packages/services/src/settings-sync/settingsSyncService.js")
    );
    return {
      service: createSettingsSyncService({ settingService: createSettingService() }),
      channel: "settings-sync",
    };
  }
  throw new Error(`unknown service: ${svc}`);
}

// ---------------------------------------------------------------------------
// per-service capture scenarios
// ---------------------------------------------------------------------------

const scenarios = {
  async system(fixtures, run) {
    await run("info", []);
    await run("listIntegratedTerminalShells", []);
    // tcp + service probes against unreachable local ports (deterministic).
    await run("probeIntranet", [{ targets: [] }]);
    await run("probeIntranet", [{ targets: [{ host: "127.0.0.1", port: 1, timeoutMs: 200 }] }]);
    await run("probeIntranet", [
      {
        targets: [
          { host: "  ", port: 1 },
          { kind: "tcp", host: "127.0.0.1", port: 1 },
        ],
        attempts: 1,
      },
    ]);
    await run("probeIntranet", [
      {
        targets: [{ kind: "service", url: "http://127.0.0.1:1/", timeoutMs: 200 }],
        attempts: 1,
        requiredSuccessCount: 2,
      },
    ]);
    await run("probeIntranet", [
      {
        targets: [
          { kind: "service", url: "ftp://x/", timeoutMs: 100 },
          { host: "localhost", port: 1 },
        ],
        attempts: 2,
      },
    ]);
  },

  async file(fixtures, run) {
    const ws = fixtures.workspace;
    const ig = fixtures.ignoreWorkspace;
    await run("readdir", [{ path: ws }]);
    await run("readdir", [{ path: ws, includeHidden: true }]);
    await run("readdir", [{ path: join(ws, "nope") }]);
    await run("stat", [{ path: join(ws, "README.md") }]);
    await run("stat", [{ path: join(ws, "src") }]);
    await run("checkFilesExist", [{ paths: [join(ws, "README.md"), join(ws, "nope.txt")] }]);
    await run("checkFilesExist", [{ paths: Array.from({ length: 16 }, (_, i) => `p${i}`) }]);
    await run("resolvePath", [{ path: join(ws, "src", "..") }]);
    await run("resolvePath", [{ path: join(ws, "missing") }]);
    await run("readTextFile", [{ path: join(ws, "README.md") }]);
    await run("readTextFile", [{ path: join(ws, "README.md"), offset: 2, length: 5 }]);
    await run("readTextFile", [{ path: join(ws, "README.md"), offset: 1000 }]);
    await run("readTextFile", [{ path: join(ws, "src", "text", "big.txt"), length: 1000000 }]);
    await run("readTextFile", [{ path: join(ws, "src", "binary", "blob.bin") }]);
    await run("readTextFile", [{ path: join(ws, "src") }]);
    await run("readFileRange", [
      { path: join(ws, "src", "binary", "blob.bin"), offset: 0, length: 4 },
    ]);
    await run("readFileRange", [
      { path: join(ws, "src", "binary", "blob.bin"), offset: 3, length: 100 },
    ]);
    await run("readFileRange", [
      { path: join(ws, "src", "binary", "blob.bin"), offset: 100, length: 8 },
    ]);
    await run("readBinaryPreview", [{ path: join(ws, "src", "binary", "blob.bin") }]);
    await run("readBinaryPreview", [{ path: join(ws, "src") }]);
    await run("readMediaPreview", [{ path: join(ws, "README.md") }]);
    await run("readMediaPreview", [{ path: join(ws, "src", "text", "big.txt"), maxBytes: 10 }]);
    await run("listWorkspaceFilesLength", [{ rootPath: ws }]);
    await run("listWorkspaceFilesLength", [{ rootPath: ig }]);
    await run("listWorkspaceFilesRange", [{ rootPath: ws, offset: 0, length: 100000 }]);
    await run("listWorkspaceFilesRange", [{ rootPath: ws, offset: 3, length: 5 }]);
    await run("listWorkspaceFilesRange", [{ rootPath: ws, offset: 10000, length: 5 }]);
    await run("searchWorkspaceFiles", [{ rootPath: ws, query: "" }]);
    await run("searchWorkspaceFiles", [{ rootPath: ws, query: "readme" }]);
    await run("searchWorkspaceFiles", [{ rootPath: ws, query: "helper" }]);
    await run("searchWorkspaceFiles", [{ rootPath: ws, query: "sr", limit: 3 }]);
    await run("searchWorkspaceFiles", [{ rootPath: ws, query: "zzz-no-match" }]);
    await run("searchWorkspaceFiles", [{ rootPath: ws, query: "readme", limit: 0 }]);
    await run("searchWorkspaceFiles", [
      { rootPath: ws, query: "helper", workspaceIdentity: "identity-1" },
    ]);
    // readWorkspaceFileSearchIgnore auto-creates .nexignore → capture on-disk post-state
    await run("readWorkspaceFileSearchIgnore", [{ rootPath: ig }], {
      post: () => treeSnapshot(ig),
    });
    await run("readWorkspaceFileSearchIgnore", [{ rootPath: ig }], {
      post: () => treeSnapshot(ig),
    });
    await run(
      "applyWorkspaceFileSearchIgnoreTransform",
      [{ rootPath: ig, transform: "sync-gitignore" }],
      {
        post: () => treeSnapshot(ig),
      },
    );
    await run(
      "applyWorkspaceFileSearchIgnoreTransform",
      [{ rootPath: ig, transform: "reset-defaults" }],
      {
        post: () => treeSnapshot(ig),
      },
    );
    await run("writeWorkspaceFileSearchIgnore", [{ rootPath: ig, content: "*.log\n" }], {
      post: () => treeSnapshot(ig),
    });
    await run("readWorkspaceFileSearchIgnore", [{ rootPath: ig }], {
      post: () => treeSnapshot(ig),
    });
  },

  async setting(fixtures, run) {
    await run("get", []);
    await run("update", [{ locale: "en-US" }]);
    await run("update", [{ recentProjects: ["/a", "/b", "/a"] }]);
    await run("update", [{ terminalFontFamily: "   " }]);
    await run("update", [{ httpProxy: "  proxy.example  " }]);
    await run("update", [{ integratedTerminalShell: { mode: "auto" } }]);
    await run("update", [{ locale: "fr-FR" }]);
    await run("update", [{ taskAutoArchiveOlderThanDays: 999 }]);
    await run("get", []);
    await run("ensureDefaultProject", [join(fixtures.root, "proj-home")], {
      post: () => treeSnapshot(join(fixtures.root, "proj-home")),
    });
  },

  async onboarding(fixtures, run) {
    const entry = {
      occupation: "developer",
      interfaceMode: "coding",
      memoryEnabled: true,
      proactiveSuggestionsEnabled: false,
      completedAt: "2024-01-01T00:00:00.000Z",
    };
    await run("getLatestEntry", []);
    await run("appendRecord", ["mid-capture", entry]);
    await run("getLatestEntry", []);
    await run("appendRecord", ["mid-capture", entry]); // replace
    await run("updateRecordPreferences", [
      { memoryEnabled: false, proactiveSuggestionsEnabled: true },
    ]);
    await run("dismissOnboarding", [{ deviceMid: "mid-capture" }]); // no-op with an entry
    await run("getRecords", []);
    await run("clearRecords", []);
    await run("getRecords", []);
  },

  async credential(fixtures, run) {
    await run("load", ["missing"]);
    await run("save", ["mcp.oauth.token", "tok-1"]);
    await run("load", ["mcp.oauth.token"]);
    await run("save", ["  padded  ", "tok-2"]);
    await run("delete", ["mcp.oauth.token"]);
    await run("load", ["mcp.oauth.token"]);
    await run("save", ["bad", ""]); // empty value allowed by schema
  },

  async "settings-sync"(fixtures, run) {
    const home = fixtures.root;
    const workspace = join(fixtures.root, "sync-workspace");
    mkdirSync(join(home, ".claude", "skills", "code-review"), { recursive: true });
    writeFileSync(
      join(home, ".claude", "skills", "code-review", "SKILL.md"),
      "---\nname: code-review\nversion: 1.2.0\n---\nReview.\n",
    );
    mkdirSync(join(home, ".claude", "commands"), { recursive: true });
    writeFileSync(
      join(home, ".claude", "commands", "deploy.md"),
      "---\ndescription: Deploy\nargument-hint: [env]\n---\nDeploy.\n",
    );
    mkdirSync(join(workspace, ".codex", "skills", "local-skill"), { recursive: true });
    writeFileSync(
      join(workspace, ".codex", "skills", "local-skill", "SKILL.md"),
      "---\nname: local-skill\n---\nLocal.\n",
    );
    writeFileSync(
      join(workspace, ".mcp.json"),
      JSON.stringify({ mcpServers: { fs: { command: "uvx", args: ["fs-server"] } } }),
    );
    await run("getClaudeAgentsFileMigrationStatus", [{ workspacePath: workspace }]);
    await run("detect", [
      { workspacePath: workspace, categories: ["skills"], intent: "manualImport" },
    ]);
    await run("detect", [
      { workspacePath: workspace, categories: ["commands"], intent: "manualImport" },
    ]);
    await run("detect", [
      { workspacePath: workspace, categories: ["mcpServers"], intent: "manualImport" },
    ]);
    await run("detect", [{ workspacePath: workspace, categories: ["skills"], intent: "firstRun" }]); // empty
    await run(
      "importSelected",
      [
        {
          workspacePath: workspace,
          selections: [{ agent: "claudeCode", category: "skills", importMode: "copy" }],
        },
      ],
      { post: () => treeSnapshot(home) },
    );
    await run("importSelected", [
      {
        workspacePath: workspace,
        selections: [{ agent: "claudeCode", category: "skills", importMode: "copy" }],
      },
    ]); // re-import: skips
    await run("getFirstRunPromptState", []);
    await run("markFirstRunPromptHandled", []);
    await run("getFirstRunPromptState", []);
  },

  async media(fixtures, run) {
    const ws = fixtures.workspace;
    mkdirSync(join(ws, "media"), { recursive: true });
    writeFileSync(
      join(ws, "media", "clip.mp4"),
      Buffer.from([0x00, 0x00, 0x00, 0x18, 0x66, 0x74, 0x79, 0x70]),
    );
    writeFileSync(join(ws, "media", "song.mp3"), Buffer.from([0x49, 0x44, 0x33, 0x04]));
    await run("prepare", [{ path: join(ws, "media", "clip.mp4"), expectedKind: "video" }]);
    await run("prepare", [{ path: join(ws, "media", "song.mp3"), expectedKind: "audio" }]);
    await run("prepare", [{ path: join(ws, "media", "clip.mp4"), expectedKind: "audio" }]);
    await run("prepare", [{ path: join(ws, "README.md"), expectedKind: "video" }]);
    await run("prepare", [{ path: join(ws, "src"), expectedKind: "video" }]);
    await run("prepare", [{ path: join(ws, "media", "missing.mp4"), expectedKind: "video" }]);
  },
};

/** Subscribe to onDynamicChange(id) through the RPC client and record events. */
async function captureEvents(rawChannel, id, trigger, out) {
  const event = rawChannel.listen("onDynamicChange", id);
  const received = new Promise((resolve) => {
    let count = 0;
    const d = event((payload) => {
      out.events.push(normalize(payload));
      count += 1;
      if (count >= 2) resolve();
    });
  });
  await trigger();
  await Promise.race([received, delay(2000)]);
}

/** Watch lifecycle + real file events through the RPC event stream. */
async function captureFileWatcher(fixtures, client, rawChannel, counter) {
  const run = async (method, args) => {
    const slug = `${String(counter.next()).padStart(2, "0")}-${method}`;
    return await capture(
      client,
      "file-watcher",
      method,
      args,
      join(OUT, "filewatcher", slug, "out.json"),
    );
  };

  const dir = join(fixtures.root, "watch-dir");
  mkdirSync(dir, { recursive: true });
  const out = await run("watch", [{ path: dir }]);
  const id = out.ok ? out.result.id : null;
  if (id !== null) {
    const eventsOut = { events: [] };
    await captureEvents(
      rawChannel,
      id,
      async () => {
        writeFileSync(join(dir, "a.txt"), "a\n");
        await delay(500);
        writeFileSync(join(dir, "b.txt"), "b\n");
        await delay(500);
      },
      eventsOut,
    );
    mkdirSync(join(OUT, "filewatcher/onDynamicChange"), { recursive: true });
    writeFileSync(
      join(OUT, "filewatcher/onDynamicChange/out.json"),
      JSON.stringify(eventsOut, null, 2) + "\n",
    );
    writeFileSync(
      join(OUT, "filewatcher/onDynamicChange/in.json"),
      JSON.stringify({ channel: "file-watcher", method: "onDynamicChange", args: [id] }, null, 2) +
        "\n",
    );
    await run("unwatch", [{ id }]);
  }
  await run("unwatch", [{ id: "999" }]);
  // watch on a missing directory throws
  await run("watch", [{ path: join(fixtures.root, "missing-dir") }]);
}

// ---------------------------------------------------------------------------
// runner
// ---------------------------------------------------------------------------

async function main() {
  const svcName = process.argv[2];
  const knownServices = new Set([
    "system",
    "file",
    "filewatcher",
    "media",
    "setting",
    "onboarding",
    "credential",
    "settings-sync",
  ]);
  if (!svcName || !knownServices.has(svcName)) {
    console.error("usage: node scripts/go-rewrite/svc-capture.mjs <system|file|filewatcher|media>");
    process.exit(2);
  }
  const onlyMethods = process.argv.slice(3);

  const fixtures = buildFixtures();
  const { service, channel } = await buildService(svcName, fixtures);

  const serverChannel = ProxyChannel.fromService(service);
  const [protoA, protoB] = createQueuePair();
  const server = new ChannelServer(protoB, "ctx", 5000, true);
  const client = new ChannelClient(protoA);
  server.registerChannel(channel, serverChannel);
  server.ready();
  const rawChannel = client.getChannel(channel);

  let caseIndex = 0;
  const run = async (method, args, opts = {}) => {
    if (onlyMethods.length && !onlyMethods.includes(method)) return { skipped: true };
    const slug = `${String(++caseIndex).padStart(2, "0")}-${method}`;
    return await capture(client, channel, method, args, join(OUT, svcName, slug, "out.json"), opts);
  };

  // rebind captureEvents with the real event subscription for the watcher
  const counter = { next: () => ++caseIndex };
  if (svcName === "filewatcher") {
    await captureFileWatcher(fixtures, client, rawChannel, counter);
  } else {
    await scenarios[svcName](fixtures, run);
  }

  // disposeAll is fire-and-forget in TS; call directly on the service.
  if (svcName === "filewatcher" && service.disposeAll) service.disposeAll();
  console.log(`captured ${caseIndex} cases for ${svcName} -> ${join(OUT, svcName)}`);
  // exit code 0 — the queue pair keeps the loop alive otherwise
  process.exit(0);
}

await main();
