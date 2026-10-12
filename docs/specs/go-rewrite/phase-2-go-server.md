# Phase 2 — Go server shell (RPC + HTTP + gateway to Node)

Part of [the Go rewrite plan](README.md). Depends on [Phase 0](phase-0-contracts.md). Independent of Phase 1 (can run in parallel).

## Goal

A Go `nex-server` that is the **only process the browser/desktop talks to**:

- it serves the Web UI static files, `/api/*` routes it owns, and the WebSocket endpoints `/ws`, `/ws/host`;
- it speaks the binary channel RPC byte-compatibly with `packages/rpc`;
- it owns web authentication (token login, sessions, rate limit) and the one-shot host capability;
- for every RPC channel and HTTP route it does **not** yet own, it forwards to a Node `nex-server` child process
  (strangler pattern), so the UI sees a complete backend from day one.

At the end of this phase **no service has moved to Go yet** except the HTTP shell and auth. The value is the
transport, the routing table, and the proof that the Go front is invisible to the existing UI.

## Non-goals

No service ports (Phase 3). No change to UI code or to RPC wire format. No new environment variables (D7); routing is configured by a file.

## Entry criteria

`scripts/go-rewrite/gate.sh 0` exits 0 — in particular RPC traces R01–R11 and `rpc-vectors.json` exist.

## Facts from the TypeScript source that this phase depends on

These were read while writing the plan; each is covered by a test below.

1. **Framing (`packages/rpc/src/protocol.ts`).** Each WebSocket **binary message** carries exactly one `ProtocolMessage`:
   13-byte header `[type u8][id u32 BE][ack u32 BE][length u32 BE]` followed by `length` bytes. `type`: `Regular=1`, `Control=2`, `Ack=3`,
   `Disconnect=5`, `ReplayRequest=6`, `Pause=7`, `Resume=8`. `SocketProtocol` (used by `http.ts` `setupChannelServer` and by the
   browser client in `packages/client/src/websocket.ts`) only emits/accepts `Regular`; other types are read and **ignored**; a
   zero-length `Regular` yields an empty message. The reader accumulates across chunks (`ChunkStream`) — a frame may span or share WebSocket messages.
2. **Message body (`channelServer.ts`, `serialization.ts`).** A `Regular` payload is `serialize(header) ++ serialize(body)`.
   Request header `[type, id, channelName, methodName]` with `type ∈ {100 Promise, 101 PromiseCancel, 102 EventListen, 103 EventDispose}`;
   for 101/103 only `header[1]` (the id) is used. Response header `[200 Initialize]` or `[201..204, id]` with `body` = data.
   The server sends `Initialize` (200) immediately on construction (unless `deferInit`).
3. **Value encoding.** Tag byte then payload: `0` undefined, `1` string (VQL byte length + UTF-8), `2` Uint8Array, `3` VSBuffer (both: VQL length + bytes),
   `4` array (VQL count + elements), `5` object = JSON text (VQL length + UTF-8), `6` int (VQL value). A JS number takes tag 6 iff `(n|0) === n`;
   anything else non-array/non-string/non-binary (including `null`, booleans, floats, plain objects) takes tag 5. Nested `Uint8Array` inside a JSON object is
   wrapped as `{"__nex_rpc_nested_uint8array_v1":true,"base64":"…"}` (exactly those two keys) and unwrapped on decode.
4. **Method/event mapping (`proxy-channel.ts`).** `call(channel, method, args[])` invokes `service[method](...args)`; `args` is the array body. A method named
   `on` + Uppercase is an **event**; `onDynamic` + Uppercase is a **dynamic event** (the method is called with `arg` and returns an event).
   Events are buffered until the first listener, then flushed. Unknown method → error `Method not found: <name>`; unknown event → `Event not found: <name>`.
5. **Requests for a channel not yet registered** are queued and retried after registration, and dropped after `timeoutDelay = 1000` ms
   (`ChannelServer` constructor default). Read the rest of `channelServer.ts` (`collectPendingRequest`, `flushPendingRequests`) before step 2.3.
6. **Error payload.** For `Error` instances the server sends `PromiseError (202)` with `{message, name, stack: string[]|undefined, code?, …}`;
   the client copies `code, kind, status, retryAfterMs, data, detail, details, taskId, traceId` back onto the error. Non-`Error` rejections use
   `PromiseErrorObj (203)` with the raw value. (Read `channelServer.ts` lines after 140 for the full payload — not yet read.)
7. **Per-connection services (`http.ts`).** `setupChannelServer(ws, services, clientMode, windowController)` registers every service as a channel, but
   **overrides** three per connection: `INexAgentService` → `createNexAgentConnectionScope(...)` (with `clientMode` and
   `role: "trusted-host-relay"` for `/ws/host`, else `"terminal-client"`), `IProviderProvisioningTargetService` (rejects unless `desktop-continuous`),
   `IWindowControllerService` → a per-connection attachment. All are disposed on socket close.
8. **Endpoints (`http.ts`).** `GET /ws` → `web-remote-replayable`. `GET /ws/host` → `desktop-continuous`, guarded by header
   `NEX_RPC_HOST_CAPABILITY_HEADER` consumed from the in-memory one-shot store; `POST /api/rpc-host-capability` issues
   `{capability, expiresAt}` (32 random bytes, base64url, TTL 30 000 ms, single use, even a failed/expired consume deletes it).
   `GET /ws/remote/:id` bridges a remote backend created by `POST /api/connect-remote` (ssh/wsl/docker; stays Node until Phase 3).
   `GET /api/server-info`, `GET /api/pets/:id/spritesheet`, `POST /api/bots/:provider[/:botId]`, static files with SPA fallback.
9. **Auth (`webAuth.ts`, `docs/specs/web-token-login.md`).** Protected paths = `/ws`, `/ws/*`, and `/api/*` except `/api/auth/*`.
   Session cookie value is a random id; `idHash = sha256(sessionId)` is what is persisted in `web-sessions.json`
   (`{version:1, tokenFingerprint, sessions:[{idHash, createdAt, expiresAt}]}`, mode `0600`, atomic write). `?token=` query: constant-time compare; on success for
   non-protected, non-WS paths a session cookie is set; wrong token on a protected path → 401. Login rate limiter returns 429 with `Retry-After` on
   `/api/auth/login`. Routes: `GET /api/auth/session`, `POST /api/auth/login`, `POST /api/auth/logout` (all `Cache-Control: no-store`).
10. **Static files (`http.ts`).** MIME table (css, gif, html, ico, jpg/jpeg, js, json, map, png, svg, txt, wasm, webp, woff, woff2, else `application/octet-stream`),
    `Cache-Control: no-cache` for `index.html`, `public, max-age=31536000, immutable` for everything else; path traversal blocked by a
    "inside root" check; SPA fallback only when the path is not token-protected.

## Layout (all new)

```
go/cmd/nex-server/                  entry; flags/config; supervises Node upstream
go/internal/rpc/                    value model, serialization, framing, channel server/client (no net imports)
go/internal/rpc/rpctest/            in-memory protocol pair for tests
go/internal/server/                 http routes, static, ws upgrade, connection setup
go/internal/server/webauth/         session store, limiter, cookies
go/internal/server/hostcap/         one-shot capability store
go/internal/gateway/                routing table, channel router, upstream connection, http reverse proxy
go/internal/adapters/node/          spawning/supervising the Node upstream process
go/internal/adapters/ws/            websocket library wrapper implementing rpc.Socket
```

Dependencies: WebSocket via `github.com/coder/websocket` (context-aware, binary frames, no global state) or `gorilla/websocket`; choose in step 2.1 by
passing the test in 2.3 (ping/close semantics, 16 MiB message limit configurable). HTTP via the standard library `net/http` (Go 1.22+ routing patterns).

## Steps

### 2.1 Value model and serialization

- **Source:** `packages/rpc/src/serialization.ts`, `buffer.ts`; vectors `go/testdata/rpc-vectors.json` (step 0.8).
- **Target:** `go/internal/rpc/{value.go,serialize.go,vql.go}`.
- **Do:**
  - `type Value any` restricted to: `Undefined` (sentinel struct), `string`, `[]byte` (tag 2), `Buffer` (named `[]byte` type, tag 3), `[]Value`, `int32`,
    and `JSON` (an `any` tree from `encoding/json`, decoded with `UseNumber`) for tag 5. Provide `Marshal(v Value) []byte` / `Unmarshal(r Reader) (Value, error)`.
  - Number rule: a Go `int` in int32 range → tag 6; any other number → tag 5 JSON. Floats that are integral (e.g. `3.0`) must follow the JS rule (they _are_
    ints in JS) → tag 6; the Go side must therefore decide using the _numeric value_, not the Go type.
  - `Marshal` of `map[string]any` containing `[]byte` applies the nested-Uint8Array wrapper; `Unmarshal` of JSON applies the unwrapper only when the object has exactly the
    two keys (`isRpcEncodedUint8Array`).
  - VQL: implement exactly the JS algorithm **including its behavior for values ≥ 2^28 and for negative int32** (the JS shifts with `>>>`). Do not "fix" it.
    The vectors file is the spec; if Go and JS disagree on any vector, Go changes.
  - Decode hardening (Go is exposed to the network): maximum nesting depth 64, maximum single length 64 MiB (configurable), VQL max 5 bytes, truncated input → error,
    never panic, never allocate before checking the remaining length.
- **Tests:**
  - `serialize_test.go`: every vector in `rpc-vectors.json` — Go `Marshal(value) == hex` and `Unmarshal(hex) == value`.
  - Round-trip property test with `rapid`: random `Value` trees.
  - Fuzz `FuzzUnmarshal` (60 s in CI): no panic, no unbounded allocation (assert `testing.AllocsPerRun` bound on crafted inputs: length prefix 2^31 with 3 bytes available).
  - **Differential test against Node:** `scripts/go-rewrite/rpc-diff.mjs` generates 5 000 random values (seeded), runs TS `serialize`, writes hex; Go test decodes and re-encodes; equality required.
    Run in CI nightly and on any change in `go/internal/rpc` or `packages/rpc`.
- **Done when:** all vectors pass, differential test passes for the seed set, fuzz clean.

### 2.2 Framing (`SocketProtocol`)

- **Source:** `packages/rpc/src/protocol.ts` lines 195–300 (`ProtocolMessage`, `writeProtocolMessage`, `SocketProtocol`, `ChunkStream`).
- **Target:** `go/internal/rpc/{frame.go,protocol.go}`; interface `type Socket interface { Read(ctx) ([]byte, error); Write(ctx, []byte) error; Close() error }`
  (message-oriented, matching WebSocket binary messages).
- **Do:** Reader reassembles messages from an arbitrary byte stream (even though WebSocket delivers whole messages, port the chunk logic so TCP/stdio transports
  work later — the TS `ISocket` is stream-like and `ChunkStream` handles partial headers). Ignore non-`Regular` types after consuming their body. Enforce a
  per-frame length limit (default 64 MiB) and close with a protocol error beyond it.
- **Tests:** table tests for header split at every byte offset; zero-length Regular; non-Regular ignored; two frames in one chunk; oversize length → error without allocation.
  Replay R01 (initialize) at byte level.
- **Done when:** tests pass; R01 byte-equal.

### 2.3 Channel server (call / event / cancel / dispose / pending)

- **Source:** `packages/rpc/src/channelServer.ts` (all of it — read lines 140–end before coding), `channels.shared.ts`, `foundation.ts` (cancellation, emitters),
  `proxy-channel.ts` semantics above.
- **Target:** `go/internal/rpc/{server.go,channel.go,events.go}`.
- **Do:**
  - `type Channel interface { Call(ctx context.Context, method string, args []Value) (Value, error); Listen(ctx context.Context, event string, arg Value) (<-chan Value, func(), error) }`.
  - `Server` per connection: sends `Initialize` on creation; `RegisterChannel(name, Channel)`; dispatches by header type; keeps `activeRequests map[id]cancel`;
    `101/103` cancel and remove; closing the connection cancels all.
  - Pending queue for unregistered channels with a 1 000 ms timeout, flushed on `RegisterChannel`, exact semantics from the source (including what is sent when it times out).
  - Error mapping `error → PromiseError` with `{message, name, stack []string, code?, kind?, status?, retryAfterMs?, data?, detail?, details?, taskId?, traceId?}`
    through a `rpc.Error` type; any other error → `{message, name:"Error"}`.
  - Event delivery is `EventFire (204)` with the request id; Go channels are the delivery mechanism, **one goroutine per active listener**, bounded buffer, and a
    documented overflow policy (close the listener with an error fire; never block the connection reader).
  - Concurrency: the read loop never executes handlers inline; each Promise call runs in its own goroutine bounded by a per-connection semaphore (default 256) to protect
    the process.
- **Tests:**
  - Table: call success, call error (Error vs non-Error), unknown channel, unknown method, cancel before start, cancel mid-flight (context observed), event listen/dispose,
    duplicate ids, id reuse after completion, request for unregistered channel then register within 1 s (served) and after 1 s (timeout behavior).
  - Replay R02–R08 and R11 against a Go server with a fake test service (`rpctest`), comparing frames to recordings after id normalization.
  - Race tests (`-race`), goroutine-leak test (`goleak`) after closing a connection with 100 pending calls and 10 listeners.
  - Back-pressure test: a listener producing 100k events with a stalled socket → bounded memory, connection closed with the documented error.
- **Done when:** R02–R08, R11 pass; leak and race tests clean.

### 2.4 RPC client (Go side) for the upstream leg and for tests

- **Source:** `channelClient.ts` (read fully: request ids start at 0 and increment; waits for `Initialize`; `PromiseError` passthrough keys).
- **Target:** `go/internal/rpc/client.go`.
- **Do:** Needed by the gateway (step 2.8) to talk to the Node upstream **at the RPC level** only when a Go-owned handler needs a Node channel (Phase 3). For Phase 2
  the gateway forwards raw frames; the client is used in tests and `nex-trace replay-rpc`.
- **Tests:** client↔server in-memory loop; client against the Node server (integration, needs Node build) for R01–R11.
- **Done when:** `nex-trace replay-rpc` runs R01–R11 against the **Node** server using the Go client, all PASS (also finishes G0.9's tooling).

### 2.5 HTTP shell: static files, server-info, headers

- **Source:** `packages/server/src/http.ts` (static section and `createServerInfo`), `packages/shared/src/server-remote.ts` (`ServerRemoteInfo`,
  `SERVER_REMOTE_PROTOCOL_VERSION`), `NEX_VERSION` in `packages/shared/src/version.ts`, `entry-http.ts` (reads `PORT` default 3030, `NEX_SERVER_HOST`/`HOST`,
  `NEX_WEB_STATIC_ROOT`, `NEX_SERVER_AUTH_TOKEN`), `NEX_SERVER_ID`, `NEX_SERVER_NAME`, `NEX_SERVER_WORKSPACE`.
- **Target:** `go/internal/server/{http.go,static.go,serverinfo.go}`.
- **Do:**
  - Reproduce exactly the response of `GET /api/server-info`: field names, optionality (`name` omitted when empty), `capabilities{desktopContinuous, websocketRpc,
processResourceTelemetry}`, `authRequired` derivation (**note:** the TS reads `NEX_SERVER_TOKEN` as a fallback in `createServerInfo` but `NEX_SERVER_AUTH_TOKEN` in
    `entry-http.ts` — reproduce, then record this inconsistency in `decisions.md` for a maintainer decision rather than silently fixing it).
  - Static serving: MIME table, cache headers, traversal guard (`filepath.Rel` check equivalent to `isInsideDirectory`), directory → `index.html`,
    percent-decoding, SPA fallback only for non-protected paths, 404 otherwise. Read file with `os.Open` + `http.ServeContent` only if headers can be made identical; otherwise write manually.
  - Existing environment variables are **read** exactly as today (no new ones). Document each in `phase-2-config.md` with default and error behavior.
- **Tests:** `static_test.go` table (traversal `/../etc/passwd`, `%2e%2e`, encoded slash, directory, missing file with and without SPA fallback, `/api/x` not falling back,
  `/login`, `/ws/x`); header equality tests against recorded TS responses (`scripts/go-rewrite/record/record-http.mjs` captures status+headers+body hash for 40 requests).
- **Done when:** the 40 recorded HTTP exchanges match (ignoring `Date`).

### 2.6 Web authentication

- **Source:** `packages/server/src/webAuth.ts` (411 lines; read fully — session store, limiter constants, `tokensEqual`, `SESSION_COOKIE_NAME`, `MAX_TOKEN_LENGTH`, `sessionCookie` attributes,
  `isSecureRequest` logic), `docs/specs/web-token-login.md` (full spec), `packages/web/src/webAuthSession.ts` (client expectations).
- **Target:** `go/internal/server/webauth/{store.go,limiter.go,cookie.go,middleware.go,routes.go}`.
- **Do:**
  - **State owner:** `web-sessions.json` under `getAppConfigDir()` (`<data>/v2/web-sessions.json`), shared with the Node server during the transition (D8).
    Read/validate/write with the same shape; atomic write (temp + rename, mode `0600`); `tokenFingerprint` computed identically (read `createWebSessionStore`);
    a mismatch invalidates sessions as in TS. Use file locking or write-then-reload-on-conflict because Node may also write — decide after reading the TS store's concurrency story.
  - Constant-time token comparison (`subtle.ConstantTimeCompare` on equal-length digests, to avoid length leak the same way `tokensEqual` does — read it).
  - Rate limiter: same thresholds/windows as `createLoginRateLimiter` (read constants); injectable clock; keyed by remote address (`r.RemoteAddr` host; **do not** trust
    `X-Forwarded-For` unless the TS does — read `remoteAddressKey`: it uses the connection info, not headers).
  - Middleware behavior table (from the TS) implemented as a pure function `decide(req) Decision` so the table can be unit-tested.
- **Tests:**
  - Decision table: (auth disabled | enabled) × (protected | page | ws path) × (no token | good `?token=` | bad `?token=` | valid cookie | invalid cookie | locked-out) → expected status/headers/Set-Cookie.
  - Cross-compat: session created by Node validates in Go and vice versa (`web-sessions.json` round trip with a shared fixture); expired session purged; token rotation invalidates.
  - Timing test (statistical, tolerant) for token compare is **not** done; instead a code-level test asserts `subtle` use via a small AST check.
  - Replay R10.
  - Security tests: 10 000 failed logins from one address → 429 within the documented threshold; cookie flags `HttpOnly; SameSite=Lax; Path=/` and `Secure` when TLS or `X-Forwarded-Proto: https`
    only if the TS honors that (read `isSecureRequest`).
- **Done when:** decision table and cross-compat tests pass; R10 passes.

### 2.7 Host capability and WebSocket endpoints

- **Source:** `hostCapability.ts` (above), `http.ts` `setupChannelServer`, `NEX_RPC_HOST_CAPABILITY_HEADER` (`packages/shared`), `stdioDeviceMid.ts`, `windowController.ts`.
- **Target:** `go/internal/server/hostcap/store.go`, `go/internal/server/ws.go`, `go/internal/adapters/ws/`.
- **Do:**
  - Capability store identical semantics (32 random bytes base64url, TTL 30 s, **single use even if expired**, purge on issue/consume), injectable clock and RNG, concurrency-safe.
  - `/ws` → connection mode `web-remote-replayable`; `/ws/host` → requires capability else `401 {"error":"Invalid or expired host capability"}` (exact body), then
    `desktop-continuous`. Both pass through the auth middleware first (they are protected paths) — the capability check happens after auth, same order as the TS.
  - Each accepted connection builds `rpc.Server` over the socket; registers channels via the **router** (step 2.8); on close disposes everything.
  - Max message size config; ping/pong keepalive (the TS has none at app level — check Hono/ws defaults and match observable behavior, do not add idle disconnects).
- **Tests:** capability store table (reuse, expiry boundary `expiresAt <= now`, replay, purge); WS integration: connect `/ws` with/without auth, `/ws/host` with valid, reused, expired, missing capability;
  close semantics (client close → server cancels active requests); replay R01, R10, R11.
- **Done when:** the above pass.

### 2.8 Gateway: routing table and upstream forwarding

- **Source:** the channel list from `inventory.md` (step 0.11): 36 service descriptor files; `INexAgentService`, `IWindowControllerService`,
  `IProviderProvisioningTargetService` overrides in `http.ts`.
- **Target:** `go/internal/gateway/{router.go,upstream.go,httpproxy.go,config.go}`.
- **Design (diagram — state owners and event order):**

  ```
  Browser/Desktop ──ws──► Go server ──► rpc.Server (per connection)
                                        │ header[2] = channelName
                                        ├─ channel owned by Go?  ──► Go handler (Phase 3 grows this set)
                                        └─ otherwise ───────────► Upstream leg (1 per client connection)
                                                                   Go raw-forwards the *same Regular frame bytes* to
                                                                   Node's /ws (or /ws/host) and returns Node's frames
  Order guarantees: frames for one request id keep order (single forwarder goroutine per direction);
                    Initialize (200) is produced by Go once and the upstream's Initialize is swallowed;
                    101/103 cancels are forwarded iff the request was forwarded (map id → route).
  ```

  - **Routing table** is a file `<appConfigDir>/gateway-routes.json` (D7: no env vars): `{"version":1,"default":"node","channels":{"<name>":"go"|"node"}, "http":{"<prefix>":"go"|"node"}}`.
    Missing file → everything `node` except routes Go owns structurally (static, auth, server-info, capability). Unknown names in the file → startup error listing them (typo protection).
  - **State owner of "who handles channel X":** the routing table loaded at connection start (immutable for the connection's lifetime). No hot reload in Phase 2.
  - **Per-connection upstream:** each client connection opens one upstream WebSocket to Node with the _same client mode_ (`/ws` for web, `/ws/host` for host; for the latter Go first calls Node's
    `POST /api/rpc-host-capability` on loopback to obtain a capability). Node's per-connection state (agent connection scope, window controller attachment, provisioning override) therefore stays
    1:1 with the client connection — this is why connection-scoped channels must be forwarded **on a dedicated upstream per client connection**, never pooled.
  - **Upstream security:** Node listens on `127.0.0.1` (or a unix socket where supported; Windows named pipe is not supported by `@hono/node-server` — use loopback TCP on an ephemeral port) and requires a
    shared secret generated by Go at start. Go must pass it using the **existing** mechanism (the token/`?token=` path of `webAuth`, since Node's `NEX_SERVER_AUTH_TOKEN` is read at startup). Go sets that when spawning Node
    (child process environment is not a user-facing env var). Node is never reachable from outside the machine.
  - **HTTP reverse proxy** for routes not owned by Go: `/api/pets/*`, `/api/bots/*`, `/api/connect-remote`, `/ws/remote/:id` (WebSocket upgrade proxied). Streaming bodies (no buffering), hop-by-hop headers stripped,
    `X-Forwarded-*` not added unless the TS reads them (it does not in the files read).
  - **Failure semantics:** upstream down at connection time → close the client socket with code 1013 ("try again later") and log at `error` with `traceId`; upstream drops mid-connection → close the client socket
    (the UI reconnects; the web client already handles close — `resolveWebAuthAfterDisconnect`); never fabricate responses.
  - **Frame inspection cost:** only the first two values of each client frame are parsed (header array → channel name), the body is not decoded (zero-copy forward).

- **Tests:**
  - Unit: router decisions; table-file validation (unknown channel, duplicate keys, bad version).
  - Integration (spawns the real Node server, needs build): replay R01–R11 **through the Go front to Node** — frames returned to the client must equal the recordings after id normalization; verify Go swallows the upstream Initialize
    (exactly one `200` frame to the client).
  - Ordering test: 1 000 concurrent calls + 50 listeners through the gateway; per-id order preserved; no cross-talk between two client connections (each sees only its own upstream — assert with distinct connection-scoped state, e.g. two `INexAgentService` scopes).
  - Cancel test: cancel forwarded; cancel for a Go-owned request not forwarded.
  - Failure tests: kill Node during an active call (client gets close, Go does not hang, goroutines leak-free); Node slow to start (client retry succeeds after Node is up).
  - Both client modes through the gateway: `/ws` → Node sees `terminal-client`; `/ws/host` → Node sees `trusted-host-relay`.
- **Done when:** the above pass, including the real-Node integration.

**Implemented (2026-10-12, step 2.8).** Layout differs from the _Target_ line above in one respect: the read loop belongs to the
gateway, not to `rpc.Server`. `Serve` owns the socket, and routing has to happen **before** dispatch — a frame for a
Node-owned channel must never reach a Go handler — so `internal/rpc` grew one seam (`Server.HandleMessage`) and the gateway
runs the reader, handing Go-owned frames to `rpc.Server` and forwarding the rest verbatim. `rpc.Server` stays the dispatcher;
it is simply no longer the reader.

- `config.go` — options, the `Logger` seam, `ErrUnknownChannel` / `ErrBadRoutes`, 1013.
- `router.go` + `rawkeys.go` — the table and its validation (unknown channel, duplicate keys, bad version, unknown field, trailing
  content), longest-prefix HTTP rules, and the structurally Go-owned routes.
- `channels/channels.go` — the channel-name list, with a test that re-reads `packages/shared/src/channels.ts` so the copy cannot
  drift.
- `upstream.go` — the dialer: same client mode, `?token=` with the Go-generated secret, and for `/ws/host` a capability minted
  **from Node's own one-shot store** (a Go-side store would be a second implementation of a security primitive).
- `leg.go` — the open leg, its one reader goroutine, `inspectPayload`, `isInitializeMessage`.
- `forward.go` — the per-connection gateway: the `id → forwarded` map, both loops, `lockedSocket` (one writer per socket, which
  is what makes per-id ordering hold across both directions).
- `gateway.go` — `Handle` (the `server.ConnectionOwner` seam) and the 1013 path.
- `httpproxy.go` — `httputil.ReverseProxy` with `Rewrite`, chosen over `Director` **because Director auto-adds `X-Forwarded-*`**
  and nothing in the TypeScript reads them. The client's `?token=` is replaced by the shared secret, never forwarded.

**Deviations from the plan text, and why.**

- _"only the first two values of each client frame are parsed"_: the implementation reads the whole 4-element header array
  (`[type, id, channel, method]`) and stops before the body. The body is still never decoded, which is the property the cost
  claim is about; `inspect_test.go` pins that with a body the codec would reject if it were read.
- _"1 upstream per client connection"_ holds, and is asserted from the upstream's own side: two client connections produce two
  upstream connections, each seeing only its own.
- The plan's HTTP list (`/api/pets/*`, `/api/bots/*`, `/api/connect-remote`, `/ws/remote/:id`) is what the **default** routing
  table produces, but the proxy asks the router rather than carrying a second route table — one statement of ownership instead of
  two.
- `internal/gateway` joined `internal/server` in `internal/archcheck`'s I/O allow-list: it dials the upstream, reverse-proxies,
  and reads the routing table. `channels/` stays pure, and `internal/rpc` remains transport-free.

**Test coverage against the list above.**

- Unit — `router_test.go`, `inspect_test.go`: routing decisions, table validation, prefix precedence, structural routes,
  header-only decoding, duplicate-key detection.
- Real-Node integration — `node_test.go`: R01's Initialize **byte for byte** through the Go front; exactly one 200 reaches the
  client although Node sends its own; Node's own unknown-channel timeout error (R05) forwarded unchanged; a real channel answered
  by Node with the client's id round-tripped; both client modes; Node killed mid-call; 1013 while Node is down.
  **R02–R04 and R06–R11 are NOT replayed through the gateway**: their channels (`demo`, `binary`, `nested`, `slow`, …) exist only
  in the recorder's in-process stub server, not in the real `createLocalServices` collection. That evidence stays where it already
  is — `internal/rpc`'s replay tests and `scripts/go-rewrite/replay-rpc-go.mjs` — and this step adds only what those cannot: the
  hop through the gateway to a real Node.
- Ordering / cross-talk — `forward_test.go`: 200 calls + 20 listeners on each of two concurrent connections, every answer
  carrying its own upstream's index and no id outside that connection's range.
- Cancel — forwarded for a forwarded request; **not** forwarded for an id the gateway never forwarded.
- Failure — upstream down at connection time → 1013; upstream dropped mid-connection → the client socket closes and the gateway
  leaks no goroutines over 20 drops; the retry after Node is up is served.
- HTTP proxy — `httpproxy_test.go`: the routing boundary, method/path/query/body preservation, hop-by-hop stripping, no
  `X-Forwarded-*`, credential replacement, a 4 MiB streamed response, 502 for a dead upstream, status/body passthrough.

**Left to step 2.9:** spawning Node (`go/internal/adapters/node`), generating the secret, and the crash budget. The gateway takes
its `UpstreamConfig` as input and never spawns anything, which is why every test here can arrange "Node is down" without a
supervisor.

### 2.9 Node upstream supervision

- **Source:** `packages/nex-server-cli/src/supervisor/{supervisor.ts,crashBudget.ts}`, `server-core/*` (the existing supervision/lifecycle design), `packages/server/src/{entry-http.ts,seaEntry.ts,stdio-lifecycle.ts}`.
- **Target:** `go/internal/adapters/node/{process.go,health.go,backoff.go}`.
- **Do:** Locate the Node entry (`packages/server` bundle / SEA, whichever `nex-server-cli` ships — read `runtime/agentWiring.ts` and `stableLauncher.ts`), spawn it with `PORT=<ephemeral>`, `NEX_SERVER_HOST=127.0.0.1`,
  `NEX_SERVER_AUTH_TOKEN=<generated>`, same `NEX_DATA_BASE_DIR` as Go; wait for `GET /api/auth/session` 200; restart with the crash budget policy from `crashBudget.ts`; forward SIGTERM/Ctrl-C and wait; kill process tree on exit
  (reuse the process-tree logic from Phase 1's `adapters/exec`). Parent-disconnect handling analogous to `server-core/parentDisconnect.ts`.
- **Tests:** fake upstream (tiny Go HTTP server) for crash/restart/backoff tests with a fake clock; real-Node test: start, health, kill -9, restart, clients reconnect; shutdown test leaves no child (check by pid on all OSes).
- **Done when:** tests pass on Linux/macOS/Windows.

### 2.10 Observability and tracing

- **Source:** `LoggingChannelServer` (`packages/rpc/src/logging-middleware.ts`), `formatLogPrefix` (`packages/shared/src/log-format.ts`), `AGENTS.md` logging section.
- **Target:** `go/internal/server/logging.go`, `go/internal/trace`.
- **Do:** Every connection gets a `connectionId` and `traceId`; every forwarded request logs `channel.method id` at **debug**; connection open/close at **info**; upstream failures at **warn/error**.
  Never log request bodies at info. Use the same prefix format as `formatLogPrefix` so existing log tooling keeps working. `traceId` is injected into error payloads (`traceId` is a passthrough key the client already reads).
- **Tests:** log capture test (levels, no body at info); trace propagation test (error returned to client contains `traceId`).
- **Done when:** tests pass.

### 2.11 Build, packaging, Docker

- **Source:** `packages/server/build-http-bundle.ts`, `tsup.config.ts`, `packaging/`, `scripts/stage-docker-context.sh`, `Dockerfile`, `docs/specs/docker-single-image.md`, `.github/workflows/release-server.yaml` (referenced by that spec; not read).
- **Target:** `scripts/go-rewrite/build-server.sh`, `docs/specs/go-rewrite/phase-2-usage.md`.
- **Do:** Build the Go server for the 5 targets; run it as a drop-in with the same `PORT`, `NEX_SERVER_HOST`, `NEX_WEB_STATIC_ROOT`, `NEX_SERVER_AUTH_TOKEN`, `NEX_DATA_BASE_DIR`. Document how Go locates the Node upstream bundle.
  Docker: an experimental image variant built from the same `dist-docker/` layout (Go binary + Node upstream + web) — **do not** change the published image in this phase.
- **Tests:** the Docker acceptance list from `docker-single-image.md` (health `GET /api/auth/session` → `{"authRequired":true,"authenticated":false}`; `/` serves the page; unauthenticated `/api/*` → 401; login; WebSocket connects; restart keeps sessions) run
  against the Go front.
- **Done when:** the Docker acceptance list passes for the Go-front image.

### 2.12 End-to-end with the real UI

- **Source:** `packages/web`, `packages/ui`, `.agents/skills/agent-browser`, `.agents/skills/dogfood`.
- **Do:** Build the web client (`pnpm --filter @nex/web build`), run the Go server with `NEX_WEB_STATIC_ROOT` and an auth token, and drive the UI with the `agent-browser` skill: login page flow (all 6 rules in `web-token-login.md`),
  open a workspace, create a task with the mock model (Node agent), send a prompt, receive streamed answer, open Settings, list providers, open a terminal, reload mid-turn, logout, expired-session redirect.
  Repeat with `pnpm dev:desktop` pointed at the Go server as a remote server (desktop remote mode — exercises `/ws/host`; find the exact recipe in `scripts/dev-desktop-remote-prod.mjs`).
- **Tests:** the scripted run saves screenshots + console logs under `go/testdata/e2e/phase2/`; the console log must have **no** `WebSocket` or RPC errors; an automated assertion script greps the browser console log.
  Compare to the same run against the pure-Node server: the sequence of RPC (channel, method) pairs per scenario must be identical (record via the gateway's debug log).
- **Done when:** both runs complete with identical RPC sequences and zero console errors.

## Exit gate — `scripts/go-rewrite/gate.sh 2`

| Check | Command                                                                                         | Pass condition                                                              |
| ----- | ----------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------- |
| G2.0  | common `GN.0` block                                                                             | exit 0 (or baseline-listed failures only)                                   |
| G2.1  | `gate.sh 0`                                                                                     | exit 0                                                                      |
| G2.2  | `cd go && go test -race ./internal/rpc/...`                                                     | pass, including all vectors in `rpc-vectors.json`                           |
| G2.3  | `cd go && go test -run xxx -fuzz FuzzUnmarshal -fuzztime 60s ./internal/rpc/`                   | clean                                                                       |
| G2.4  | `node scripts/go-rewrite/rpc-diff.mjs --seed 1 --count 5000 \| go run ./cmd/nex-trace rpc-diff` | 0 mismatches                                                                |
| G2.5  | `cd go && go test ./internal/server/... ./internal/gateway/...`                                 | pass (auth decision table, static, capability, router)                      |
| G2.6  | `go run ./cmd/nex-trace replay-rpc --url ws://127.0.0.1:<go-port> --all` (Go front → real Node) | R01–R11 PASS                                                                |
| G2.7  | `node scripts/go-rewrite/crosscheck-sessions.mjs`                                               | sessions file written by Node validates in Go and vice versa                |
| G2.8  | `bash scripts/go-rewrite/docker-accept.sh --front go`                                           | all 7 acceptance items from `docker-single-image.md` pass                   |
| G2.9  | `bash scripts/go-rewrite/e2e-web-phase2.sh`                                                     | identical RPC sequence to Node baseline, 0 console errors                   |
| G2.10 | `go test ./internal/adapters/node/... -run Supervisor` on 3 OSes                                | pass; no orphan child process                                               |
| G2.11 | `grep -rn "net/http\|os/exec" go/internal/rpc`                                                  | no hits (rpc stays transport-agnostic)                                      |
| G2.12 | `bash scripts/go-rewrite/check-file-lines.sh`                                                   | no Go file over 400 lines                                                   |
| G2.13 | gateway soak: `bash scripts/go-rewrite/soak-gateway.sh --minutes 30`                            | RSS growth < 30 MiB after warm-up, goroutine count stable, 0 dropped frames |

## Rollback

Run the Node server directly (unchanged). `web-sessions.json` stays valid for both. Delete `gateway-routes.json` to restore default routing. Nothing in this phase writes any other persistent state.

## Verify-first list

1. `channelServer.ts` after line 140: exact error payload, pending-request flush/timeout behavior, `EventFire` handling — only the first 140 lines were read.
2. **Resolved while reviewing:** `PersistentProtocol` is referenced only by `packages/rpc/src/{protocol,remote,persistent-protocol,index}.ts` and `packages/rpc/examples/04-remote-connection.ts`; the `connection-flow-v1` control messages exist only in the MessagePort
   transport in `protocol.ts`. The WebSocket paths (`http.ts`, `packages/client/src/websocket.ts`) use plain `SocketProtocol`, so the Go codec does not need ack/replay/flow-control for WebSocket. Still unchecked: whether the Desktop remote-server
   mode (`windowRemoteConnectionRegistry.ts`) wraps its WebSocket in a different protocol — check before step 2.7 if Desktop will connect to the Go server through `/ws/host`.
3. `SESSION_COOKIE_NAME`, `MAX_TOKEN_LENGTH`, limiter thresholds, session TTL, `isSecureRequest`, `tokensEqual` (not read).
4. The `NEX_SERVER_TOKEN` vs `NEX_SERVER_AUTH_TOKEN` inconsistency in `createServerInfo` vs `entry-http.ts`.
5. How `nex-server-cli` locates and launches the server bundle (needed for step 2.9).
6. Whether Node's `@hono/node-ws` imposes message size limits that the Go gateway must respect.
7. Whether a WebSocket library's default read limit (often 32 KiB–1 MiB) is lower than the UI's largest RPC message; v4 frames alone can be up to 1 MiB (`maxFrameBytes`) and attachments chunk at 512 KiB.
8. Desktop remote-mode recipe (`scripts/dev-desktop-remote-prod.mjs`) for exercising `/ws/host`.
