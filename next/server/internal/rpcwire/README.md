# rpcwire

Go implementation of the binary wire format used by the Nex web UI's Channel
RPC layer. Everything here is derived from, and verified against, the
TypeScript implementation in `packages/rpc/src`.

## Commands

From `next/server`:

```sh
go vet ./...
go test ./...
```

Regenerate the golden fixtures from the repository root:

```sh
node_modules/.bin/tsx next/server/internal/rpcwire/testdata/gen.ts
```

(`tsx` needs Node 24+; prepend
a Node 24 `bin` directory to `PATH` if the default
`node` is older.)

Regeneration is deterministic: re-running the generator leaves every
`testdata/*.bin` and `*.json` byte-identical.

## Wire format

### Frame

One frame is a 13-byte header followed by an opaque payload
(`packages/rpc/src/protocol.ts:207`, `:222-230`):

```
offset  size  field
0       1     type   (uint8)
1       4     id     (uint32, big-endian)
5       4     ack    (uint32, big-endian)
9       4     length (uint32, big-endian, payload byte count)
13      n     payload
```

`FrameReader` decodes frames from an arbitrary byte stream, so it accepts
several frames in one WS binary message and one frame split across messages.
Both ends currently put exactly one frame in each WS message
(`packages/client/src/websocket.ts:47`, `packages/server/src/http.ts:72`), but
`SocketProtocol`'s `ChunkStream` tolerates both shapes
(`packages/rpc/src/protocol.ts:260-296`), so the Go reader is equally tolerant.

Only `Regular` frames deliver payloads. `SocketProtocol.send` always writes
`Regular` with `id = 0, ack = 0` (`packages/rpc/src/protocol.ts:252-254`) and
`readMessages` ignores the received `id`/`ack` (`:267-270`). A zero-length
`Regular` frame still produces an (empty) message (`:281-286`). Frames of other
types are decoded by the reader and then skipped by `SocketProtocol`.

### Serialized value

Each value is `tag(1)` followed by an optional VQL length and bytes
(`packages/rpc/src/serialization.ts:109-117`, `:152-218`):

| tag | TypeScript type  | Go representation   |
| --- | ---------------- | ------------------- |
| 0   | `undefined`      | `rpcwire.Undefined` |
| 1   | `string` (UTF-8) | `string`            |
| 2   | `Uint8Array`     | `Buffer{VS:false}`  |
| 3   | `VSBuffer`       | `Buffer{VS:true}`   |
| 4   | array            | `[]any`             |
| 5   | JSON fallback    | `json.RawMessage`   |
| 6   | VQL integer      | `int64`             |

VQL is little-endian base-128: 7 data bits per byte, high bit = "more bytes"
(`packages/rpc/src/serialization.ts:67-103`). Encodings:
`0 → 00`, `127 → 7f`, `128 → 80 01`, `2^31 → 80 80 80 80 08`,
`0xffffffff → ff ff ff ff 0f`.

Tag 6 is only used when `(value | 0) === value`, i.e. for signed 32-bit
integers. Anything else — including `2^31` and `2^32-1` — goes through the tag 5
JSON fallback (`packages/rpc/src/serialization.ts:175`). Decoding tag 6 widens
with `int32` semantics because `readIntVQL` accumulates with 32-bit bitwise OR;
`0xffffffff` decodes as `-1`.

### Channel payload

Every frame payload is exactly two serialized values: `header`, then `body`
(`packages/rpc/src/channelClient.ts:185-188`,
`packages/rpc/src/channelServer.ts:57-65`).

Request headers (`packages/rpc/src/channels.shared.ts:18-23`,
`packages/rpc/src/channelClient.ts:178-207`):

| type | name          | header                         | body        |
| ---- | ------------- | ------------------------------ | ----------- |
| 100  | Promise       | `[100, id, channelName, name]` | argument    |
| 101  | PromiseCancel | `[101, id]`                    | `undefined` |
| 102  | EventListen   | `[102, id, channelName, name]` | argument    |
| 103  | EventDispose  | `[103, id]`                    | `undefined` |

Response headers (`packages/rpc/src/channels.shared.ts:25-31`,
`packages/rpc/src/channelServer.ts:43-55`):

| type | name            | header      | body                        |
| ---- | --------------- | ----------- | --------------------------- |
| 200  | Initialize      | `[200]`     | `undefined`                 |
| 201  | PromiseSuccess  | `[201, id]` | resolved value              |
| 202  | PromiseError    | `[202, id]` | `{message, name, stack?}`   |
| 203  | PromiseErrorObj | `[203, id]` | non-`Error` rejection value |
| 204  | EventFire       | `[204, id]` | event value                 |

The server sends `Initialize` as soon as the `ChannelServer` is constructed
(`packages/rpc/src/channelServer.ts:29-31`); the client queues every request
until it arrives (`packages/rpc/src/channelClient.ts:123-127`).

For `PromiseError`, the body is `JSON.stringify` of `{message, name, stack}`
where `stack` is `error.stack.split("\n")` or omitted when absent
(`packages/rpc/src/channelServer.ts:135-171`). The first element of the array is
JS's `"Name: message"` line.

## Fixtures

`testdata/gen.ts` produces every fixture from the real TypeScript code:

- client payloads are captured from a real `ChannelClient` (`call`, `listen`,
  cancellation via `CancellationTokenSource`);
- server payloads are captured from a real `ChannelServer` fed the captured
  client payloads;
- frame bytes come from `writeProtocolMessage`.

`ChannelClient.sendRequest` and `ChannelServer.sendResponse` are private, so the
generator drives the public classes and records what they write rather than
re-implementing their headers.

Each fixture is a `<name>.bin` (exact frame bytes) and a `<name>.json` sidecar
describing direction, frame header and the logical request/response content.
`rpcwire_test.go` decodes all of them, compares to the sidecar, and requires
re-encoding to reproduce the `.bin` byte-for-byte.

## JSON bridge

`ToJSON` / `FromJSON` translate between wire values and the server runner link:

- `Buffer` ⇄ `{"$buf":"<standard base64>","$vs":true|false}`
- `Undefined` ⇄ `{"$undef":true}`
- `string` ⇄ JSON string; `[]any` ⇄ JSON array; integers ⇄ JSON number
- tag 5 objects stay raw JSON text, never re-marshalled

`undefined` has no JSON representation, and an absent property is
indistinguishable from a missing one, so the explicit `$undef` sentinel is used
instead of dropping the value. `null` is left as JSON `null` (TypeScript
serializes it as a tag 5 body containing `null`). A tag 5 object that itself
declares `$buf`, `$vs`, `$undef` or `$esc` is wrapped as `{"$esc":{...}}` so
arbitrary wire JSON survives the bridge.

Buffers nested inside a tag 5 object are not converted: TypeScript only
base64-encodes nested `Uint8Array` when it constructs the object itself
(`packages/rpc/src/serialization.ts:220-250`); once decoded, a tag 5 body is
opaque raw JSON and is passed through unchanged.
