// G2.5: record the HTTP shell (static files + GET /api/server-info) of the REAL
// @nex/server, so the Go port (go/internal/server) can be compared byte for byte.
//
// The recorder drives `createHttpServer` from packages/server/src/http.ts itself — the same
// function the shipped entry point calls — over loopback TCP with Node's http client, and
// stores status + headers + a SHA-256 of the body for every case under go/testdata/http/.
//
// Why not the built dist bundle: entry-http.ts needs a build-time embedded provider config and
// dist/ is gitignored, so a committed fixture must not depend on a build. tsx runs the same
// source; NEX_VERSION therefore falls back to "0.0.0-dev", which is also the Go default.
//
// What is deliberately NOT recorded: paths owned by later steps (/api/auth/*, /api/pets/*,
// /api/bots/*, /api/rpc-host-capability, /api/connect-remote). The Go front answers 404 for
// them until step 2.6/2.7 own them, so pinning them here would freeze strangler-in-progress
// behavior. /ws and /api/does-not-exist ARE recorded: both sides answer 404 for them.
//
// Ignored headers: Date (always fresh), Server (absent in both stacks) and Connection
// (a client-agent artifact, not a property of the handler).
//
//   node --import tsx scripts/go-rewrite/record/record-http.mjs
import { createHash } from "node:crypto";
import { mkdirSync, rmSync, writeFileSync } from "node:fs";
import http from "node:http";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO = resolve(HERE, "../../..");
const OUT = join(REPO, "go/testdata/http");
const FIXTURE_ROOT = join(OUT, "static-root");

const IGNORED_HEADERS = new Set(["date", "server", "connection"]);

// Fixture content is written as hex for the binary assets so this file stays 7-bit clean.
// Byte content matters: the recorded body SHA is compared against the same bytes served by
// Go, which makes this fixture the contract, not decoration.
const FIXTURE_FILES = {
  "index.html": "<!doctype html><title>nex static fixture</title>\n",
  "README.md": "# nex static fixture\n",
  "no-extension": "plain-bytes\n",
  "UPPER.PNG": "PNG-UPPER-1",
  "assets/app.js": "export const answer = 42;\n",
  "assets/style.css": "body{color:#123456}\n",
  "assets/data.json": '{"ok":true}\n',
  "assets/icon.svg": "<svg xmlns='http://www.w3.org/2000/svg'/>\n",
  "assets/fallback.bin": "binary\n",
  "nested/index.html": "<!doctype html><title>nested</title>\n",
  "nested/deep/page.html": "<!doctype html><title>deep</title>\n",
  "file with space.txt": "spaces\n",
  "assets/logo.png": "hex:89504e470d0a1a0a0000000d49484452",
  "assets/mod.wasm": "hex:0061736d01000000",
  "assets/font.woff2": "hex:774f4632",
  "assets/icon.ico": "hex:00010000",
};

const FIXTURE_DIRS = ["empty-dir", "nested/no-index"];

/**
 * Environment groups. `createServerInfo` reads these per request, not at startup, so a single
 * server serves all three — which is itself the property being recorded.
 *   plain — nothing set: no name, no workspaces, authRequired from the NEX_SERVER_TOKEN fallback
 *   named — ID/NAME/WORKSPACE set: name present, workspaces non-empty, label = basename
 *   token — only NEX_SERVER_TOKEN set: authRequired flips true while web auth stays off. That
 *           asymmetry is the NEX_SERVER_TOKEN vs NEX_SERVER_AUTH_TOKEN inconsistency the Go port
 *           reproduces on purpose (see serverinfo.go).
 */
const GROUPS = {
  // NEX_SERVER_ID is always pinned: with it unset the response carries os.hostname(), which
  // would make the fixture unreproducible off the recording machine.
  plain: {
    NEX_SERVER_ID: "record-plain",
    NEX_SERVER_NAME: "",
    NEX_SERVER_WORKSPACE: "",
    NEX_SERVER_TOKEN: "",
  },
  named: {
    NEX_SERVER_ID: "record-named",
    NEX_SERVER_NAME: "Recorded Server",
    NEX_SERVER_WORKSPACE: "/srv/demo",
    NEX_SERVER_TOKEN: "",
  },
  token: {
    NEX_SERVER_ID: "record-token",
    NEX_SERVER_NAME: "",
    NEX_SERVER_WORKSPACE: "",
    NEX_SERVER_TOKEN: "record-token-value",
  },
};

/** [group, method, path] — MIME table, cache headers, traversal, SPA fallback, server-info. */
const REQUESTS = [
  ["plain", "GET", "/"],
  ["plain", "GET", "/index.html"],
  ["plain", "HEAD", "/"],
  ["plain", "HEAD", "/assets/app.js"],
  ["plain", "GET", "/assets/app.js"],
  ["plain", "GET", "/assets/style.css"],
  ["plain", "GET", "/assets/data.json"],
  ["plain", "GET", "/assets/logo.png"],
  ["plain", "GET", "/assets/mod.wasm"],
  ["plain", "GET", "/assets/font.woff2"],
  ["plain", "GET", "/assets/icon.ico"],
  ["plain", "GET", "/assets/icon.svg"],
  ["plain", "GET", "/assets/fallback.bin"],
  ["plain", "GET", "/assets/app.js.map"],
  ["plain", "GET", "/assets/app.js?v=1"],
  ["plain", "GET", "/assets/"],
  ["plain", "GET", "/assets"],
  ["plain", "GET", "/nested"],
  ["plain", "GET", "/nested/index.html"],
  ["plain", "GET", "/nested/deep/page.html"],
  ["plain", "GET", "/nested/no-index/"],
  ["plain", "GET", "/empty-dir/"],
  ["plain", "GET", "/UPPER.PNG"],
  ["plain", "GET", "/no-extension"],
  ["plain", "GET", "/README.md"],
  ["plain", "GET", "/file%20with%20space.txt"],
  ["plain", "GET", "/./assets/app.js"],
  ["plain", "GET", "/assets/../assets/app.js"],
  ["plain", "GET", "/missing-asset.png"],
  ["plain", "GET", "/login"],
  ["plain", "GET", "/settings/general"],
  ["plain", "GET", "/../etc/passwd"],
  ["plain", "GET", "/%2e%2e/etc/passwd"],
  ["plain", "GET", "/%2E%2E/%2E%2E/etc/passwd"],
  ["plain", "GET", "/..%2fetc%2fpasswd"],
  ["plain", "GET", "/assets%2fapp.js"],
  ["plain", "GET", "//assets/app.js"],
  ["plain", "GET", "/api/does-not-exist"],
  ["plain", "GET", "/ws"],
  ["plain", "GET", "/ws/remote/none"],
  ["plain", "GET", "/api/server-info"],
  ["named", "GET", "/api/server-info"],
  ["named", "GET", "/"],
  ["named", "GET", "/missing-page"],
  ["token", "GET", "/api/server-info"],
  ["token", "GET", "/"],
  ["token", "GET", "/api/does-not-exist"],
];

function sha256(buffer) {
  return createHash("sha256").update(buffer).digest("hex");
}

function writeFixture() {
  rmSync(FIXTURE_ROOT, { recursive: true, force: true });
  for (const dir of FIXTURE_DIRS) mkdirSync(join(FIXTURE_ROOT, dir), { recursive: true });
  for (const [rel, content] of Object.entries(FIXTURE_FILES)) {
    const file = join(FIXTURE_ROOT, rel);
    mkdirSync(dirname(file), { recursive: true });
    writeFileSync(
      file,
      content.startsWith("hex:") ? Buffer.from(content.slice(4), "hex") : content,
    );
  }
}

function request(port, method, path) {
  return new Promise((res, rej) => {
    const req = http.request(
      { host: "127.0.0.1", port, method, path, agent: false, timeout: 10_000 },
      (response) => {
        const chunks = [];
        response.on("data", (chunk) => chunks.push(chunk));
        response.on("end", () => {
          const headers = {};
          for (const [key, value] of Object.entries(response.headers)) {
            if (!IGNORED_HEADERS.has(key.toLowerCase())) headers[key.toLowerCase()] = value;
          }
          const body = Buffer.concat(chunks);
          res({ status: response.statusCode, headers, body });
        });
      },
    );
    req.on("error", rej);
    req.on("timeout", () => {
      req.destroy(new Error(`timeout on ${method} ${path}`));
    });
    req.end();
  });
}

function applyGroup(group) {
  for (const [key, value] of Object.entries(GROUPS[group])) {
    if (value === "") delete process.env[key];
    else process.env[key] = value;
  }
}

async function main() {
  writeFixture();

  // Explicit whitelist: a stray NEX_SERVER_* in the outer shell would change the recording.
  for (const key of Object.keys(process.env)) {
    if (key.startsWith("NEX_SERVER_")) delete process.env[key];
  }
  process.env["NEX_DATA_BASE_DIR"] = join(OUT, ".runtime");

  const { ServiceCollection } = await import(join(REPO, "packages/services/src/collection.ts"));
  const { createHttpServer } = await import(join(REPO, "packages/server/src/http.ts"));
  const server = createHttpServer(new ServiceCollection(), 0, {
    staticRoot: FIXTURE_ROOT,
    spaFallback: true,
  });
  await new Promise((r) => setTimeout(r, 250));
  const port = server.address().port;

  const cases = [];
  for (const [group, method, path] of REQUESTS) {
    applyGroup(group);
    const response = await request(port, method, path);
    cases.push({
      group,
      method,
      path,
      status: response.status,
      headers: response.headers,
      bodyBytes: response.body.length,
      bodySha256: sha256(response.body),
    });
  }

  server.close();
  rmSync(join(OUT, ".runtime"), { recursive: true, force: true });

  mkdirSync(OUT, { recursive: true });
  writeFileSync(
    join(OUT, "requests.json"),
    `${JSON.stringify(
      {
        version: 1,
        note: "recorded from packages/server/src/http.ts via tsx; see record-http.mjs",
        ignoredHeaders: [...IGNORED_HEADERS],
        fixtureRoot: "static-root",
        cases,
      },
      null,
      2,
    )}\n`,
  );
  console.log(`recorded ${cases.length} HTTP cases into ${OUT}`);
}

await main();
