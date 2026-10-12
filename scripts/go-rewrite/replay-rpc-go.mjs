// Replay the recorded RPC scenarios against the GO server over a real WebSocket.
//
// Phase-2 step 2.7 introduced the Go WebSocket endpoints. The recordings in go/testdata/traces/rpc
// were captured by scripts/go-rewrite/record/record-rpc.mjs, which drives the REAL @nex/rpc
// ChannelServer over an in-process queue pair — the same codec, but not the WebSocket transport.
// They must still come back byte for byte when the client speaks WebSocket to the Go process, which
// is the only way to prove the framing and the handshake changed nothing the UI can observe.
//
// The Go half of the same comparison (recorded frames fed straight into rpc.Server) is
// go/internal/server/wsreplay_test.go. This script covers what a Go-only test cannot: the Node
// client half, using the same `ws` package the TypeScript server uses.
//
//   node scripts/go-rewrite/replay-rpc-go.mjs
//   node scripts/go-rewrite/replay-rpc-go.mjs --url http://127.0.0.1:1234
//
// Scenarios: R01 (Initialize handshake, compared byte for byte), R11 (the subscribe-shaped
// clientMode envelope crossing the real transport), and R10. R10 is the auth scenario and has no
// recorded frames — phase 0 declared that exception (docs/specs/go-rewrite/phase-0-status.md) — so
// it is exercised live here: 401 without credentials, a successful upgrade with `?token=`, a
// one-shot host capability, and a refused replay of that capability.
import { spawn } from "node:child_process";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { WebSocket } from "ws";

const REPO = new URL("../..", import.meta.url).pathname.replace(/\/$/, "");
const TRACE_DIR = join(REPO, "go/testdata/traces/rpc");
const CAPABILITY_HEADER = "x-nex-rpc-host-capability";
const AUTH_TOKEN = "replay-rpc-go-token";

/**
 * Read a recorded scenario and split its frames by direction.
 * @param {string} id
 * @returns {{client: {hex: string}[], server: {hex: string}[]}}
 */
function readRecorded(id) {
  const path = join(TRACE_DIR, id, "frames.jsonl");
  const client = [];
  const server = [];
  for (const line of readFileSync(path, "utf8").split("\n")) {
    if (!line.trim()) continue;
    const frame = JSON.parse(line);
    if (frame.direction === "client-to-server") client.push(frame);
    else if (frame.direction === "server-to-client") server.push(frame);
  }
  if (server.length === 0) throw new Error(`${id}: recording has no server frames`);
  return { client, server };
}

/**
 * Wrap a serialized RPC payload in the 13-byte ProtocolMessage header SocketProtocol writes:
 * [type u8][id u32 BE][ack u32 BE][length u32 BE].
 */
function frameOf(messageType, id, payloadHex) {
  const payload = Buffer.from(payloadHex, "hex");
  const header = Buffer.alloc(13);
  header.writeUInt8(messageType, 0);
  header.writeUInt32BE(id >>> 0, 1);
  header.writeUInt32BE(0, 5);
  header.writeUInt32BE(payload.length, 9);
  return Buffer.concat([header, payload]);
}

/** The body of a Regular frame with the 13-byte header stripped, as hex. */
function payloadOf(frameHex) {
  return frameHex.slice(26);
}

/**
 * A socket plus every binary message it has received.
 *
 * Buffering from the moment the socket opens is not cosmetic: the Go server sends Initialize in the
 * same tick as the handshake response, so a listener attached only after `open` resolves can miss
 * it — and `ws` drops a message nobody is listening for, which would silently turn every replay
 * into a timeout.
 */
class FrameReader {
  constructor(socket) {
    this.messages = [];
    this.waiters = [];
    this.failure = null;
    socket.on("message", (data, isBinary) => {
      if (!isBinary) return;
      this.messages.push(data.toString("hex"));
      this.flush();
    });
    socket.once("error", (error) => {
      this.failure = error;
      this.flush();
    });
  }

  flush() {
    for (const waiter of [...this.waiters]) {
      if (this.messages.length >= waiter.count) this.settle(waiter);
      else if (this.failure) this.fail(waiter, this.failure);
    }
  }

  settle(waiter) {
    clearTimeout(waiter.timer);
    this.waiters = this.waiters.filter((candidate) => candidate !== waiter);
    waiter.resolve(this.messages.slice(0, waiter.count));
  }

  fail(waiter, error) {
    clearTimeout(waiter.timer);
    this.waiters = this.waiters.filter((candidate) => candidate !== waiter);
    waiter.reject(error);
  }

  /** Resolve with the first `count` messages, waiting for them if they have not arrived yet. */
  take(count, timeoutMs) {
    if (this.messages.length >= count) return Promise.resolve(this.messages.slice(0, count));
    if (this.failure) return Promise.reject(this.failure);
    return new Promise((resolve, reject) => {
      const waiter = { count, resolve, reject, timer: null };
      waiter.timer = setTimeout(
        () =>
          this.fail(waiter, new Error(`timed out after ${this.messages.length}/${count} frames`)),
        timeoutMs,
      );
      this.waiters.push(waiter);
    });
  }
}

/**
 * Open a WebSocket and resolve once the handshake completed, with a FrameReader already attached so
 * no message can be missed.
 */
async function open(url, options = {}) {
  return new Promise((resolve, reject) => {
    const socket = new WebSocket(url, options);
    socket.binaryType = "nodebuffer";
    const reader = new FrameReader(socket);
    socket.once("open", () => resolve({ socket, reader }));
    socket.once("error", reject);
  });
}

/**
 * Spawn the Go replay server (go/internal/server/wsprocess_test.go) and wait for it to print its
 * URL. Returns {baseUrl, stop}.
 */
async function startGoServer() {
  const child = spawn(
    "go",
    ["test", "-count=1", "-run", "TestReplayServerProcess", "-v", "./internal/server/"],
    {
      cwd: join(REPO, "go"),
      env: { ...process.env, NEX_REPLAY_LISTEN: "1", NEX_SERVER_AUTH_TOKEN: AUTH_TOKEN },
      stdio: ["ignore", "pipe", "inherit"],
    },
  );

  const wsUrl = await new Promise((resolve, reject) => {
    let buffer = "";
    const timer = setTimeout(
      () => reject(new Error("go test did not print NEX_REPLAY_URL")),
      180_000,
    );
    child.stdout.on("data", (chunk) => {
      buffer += chunk.toString();
      const match = /NEX_REPLAY_URL=(ws:\/\/[^\s]+)/.exec(buffer);
      if (match) {
        clearTimeout(timer);
        resolve(match[1]);
      }
    });
    child.once("error", reject);
    child.once("exit", (code) => {
      clearTimeout(timer);
      reject(new Error(`go test exited early with code ${code}`));
    });
  });

  return {
    baseUrl: wsUrl.replace(/^ws/, "http"),
    stop() {
      child.kill("SIGKILL");
    },
  };
}

/**
 * R01: the Initialize handshake, compared byte for byte with the recording. The handshake is owned
 * entirely by step 2.7, so this must match exactly — it is the one frame nothing else can influence.
 */
async function replayR01(baseUrl) {
  const { client, server } = readRecorded("R01");
  const { socket, reader } = await open(`${baseUrl.replace(/^http/, "ws")}/ws?token=${AUTH_TOKEN}`);
  try {
    const pending = reader.take(server.length, 10_000);
    for (const frame of client) socket.send(frameOf(1, 0, frame.hex));
    const got = await pending;
    for (let i = 0; i < server.length; i++) {
      if (payloadOf(got[i]) === server[i].hex) continue;
      console.error(
        `  R01 frame ${i} mismatch\n` +
          `    recorded: ${server[i].hex}\n` +
          `    go:       ${payloadOf(got[i])}`,
      );
      return false;
    }
    return true;
  } finally {
    socket.close();
  }
}

/**
 * R11: the subscribe-shaped clientMode envelope, crossing the real transport.
 *
 * The request names the channel `subscribe-shaped`, which is a GATEWAY channel — step 2.8 owns the
 * routing table that registers it. Until then the Go front correctly parks the request and answers
 * the pending-timeout error, so what step 2.7 can prove here is that the full envelope was decoded
 * off the wire and produced exactly that answer. wsreplay_test.go compares R11's answer byte for
 * byte with the channel installed.
 */
async function replayR11(baseUrl) {
  const { client, server } = readRecorded("R11");
  const { socket, reader } = await open(`${baseUrl.replace(/^http/, "ws")}/ws?token=${AUTH_TOKEN}`);
  try {
    const pending = reader.take(2, 10_000);
    for (const frame of client) socket.send(frameOf(1, 0, frame.hex));
    const got = await pending;

    if (payloadOf(got[0]) !== server[0].hex) {
      console.error(
        `  R11 Initialize mismatch\n    recorded: ${server[0].hex}\n    go:       ${payloadOf(got[0])}`,
      );
      return false;
    }
    // The answer is a framed RPC message, so the error text is one JSON member inside it; searching
    // the bytes for both substrings is the check that does not depend on the frame layout.
    const answer = payloadOf(got[1]);
    const mentionsChannel = Buffer.from(answer, "hex").includes("subscribe-shaped");
    const isTimeout = Buffer.from(answer, "hex").includes("timed out");
    if (!mentionsChannel || !isTimeout) {
      console.error(
        `  R11 expected the Unknown-channel error naming subscribe-shaped, got: ${answer}`,
      );
      return false;
    }
    return true;
  } finally {
    socket.close();
  }
}

/**
 * R10: the auth scenario, live, because it has no recorded frames. A protected WebSocket path must
 * answer 401 without credentials and upgrade with `?token=`; /ws/host must additionally demand a
 * one-shot capability that cannot be replayed.
 */
async function replayR10(baseUrl) {
  const failures = [];
  const wsBase = baseUrl.replace(/^http/, "ws");

  // 1. /ws without credentials on an auth-enabled server must be refused.
  try {
    const refused = await open(`${wsBase}/ws`);
    refused.socket.close();
    failures.push("/ws connected without credentials on an auth-enabled server");
  } catch {
    /* expected */
  }

  // 2. With the token, /ws upgrades and the Initialize frame still matches the recording.
  const authorized = await open(`${wsBase}/ws?token=${AUTH_TOKEN}`);
  const initialize = await authorized.reader.take(1, 10_000);
  const expectedInit = readRecorded("R01").server[0].hex;
  if (payloadOf(initialize[0]) !== expectedInit) {
    failures.push(`Initialize mismatch: recorded ${expectedInit}, go ${payloadOf(initialize[0])}`);
  }
  authorized.socket.close();

  // 3. /ws/host needs a capability, and the ticket is single use.
  const issueResponse = await fetch(`${baseUrl}/api/rpc-host-capability?token=${AUTH_TOKEN}`, {
    method: "POST",
  });
  if (issueResponse.status !== 200) {
    failures.push(`POST /api/rpc-host-capability answered ${issueResponse.status}, want 200`);
  } else {
    const { capability } = await issueResponse.json();
    const first = await open(`${wsBase}/ws/host?token=${AUTH_TOKEN}`, {
      headers: { [CAPABILITY_HEADER]: capability },
    });
    await first.reader.take(1, 10_000);
    first.socket.close();

    try {
      const replayed = await open(`${wsBase}/ws/host?token=${AUTH_TOKEN}`, {
        headers: { [CAPABILITY_HEADER]: capability },
      });
      replayed.socket.close();
      failures.push("a reused host capability was accepted");
    } catch {
      /* expected */
    }
  }

  // 4. /ws/host without any capability must be refused.
  try {
    const noCapability = await open(`${wsBase}/ws/host?token=${AUTH_TOKEN}`);
    noCapability.socket.close();
    failures.push("/ws/host connected without a capability");
  } catch {
    /* expected */
  }

  for (const failure of failures) console.error(`  R10 ${failure}`);
  return failures.length === 0;
}

async function main() {
  const urlIndex = process.argv.indexOf("--url");
  const explicit = urlIndex >= 0 ? process.argv[urlIndex + 1] : null;
  let baseUrl = explicit;
  let stop = () => {};
  if (!baseUrl) {
    const server = await startGoServer();
    baseUrl = server.baseUrl;
    stop = server.stop;
  }
  console.log(`replaying against ${baseUrl}`);

  let failed = 0;
  try {
    for (const [id, replay] of [
      ["R01", replayR01],
      ["R11", replayR11],
      ["R10", replayR10],
    ]) {
      let ok = false;
      try {
        ok = await replay(baseUrl);
      } catch (error) {
        console.error(`  ${id} threw: ${error.message}`);
      }
      console.log(`${ok ? "PASS" : "FAIL"} ${id}`);
      if (!ok) failed++;
    }
  } finally {
    stop();
  }
  console.log(failed === 0 ? "all scenarios PASS" : `${failed} scenario(s) FAILED`);
  process.exit(failed === 0 ? 0 : 1);
}

await main();
