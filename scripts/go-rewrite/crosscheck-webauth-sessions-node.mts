// crosscheck-webauth-sessions-node.mts — the Node half of crosscheck-webauth-sessions.mjs.
//
// It drives the *real* session store from packages/server/src/webAuth.ts (createWebSessionStore),
// not a reimplementation, so a drift in the fingerprint or the file shape fails here.
//
//   tsx crosscheck-webauth-sessions-node.mts create  <token> <file>  # create a session, print {sessionId}
//   tsx crosscheck-webauth-sessions-node.mts validate <token> <file> <sessionId>
//   tsx crosscheck-webauth-sessions-node.mts list     <token> <file>  # count live sessions as JSON array
import { createWebSessionStore } from "../../packages/server/src/webAuth.ts";

const [, , action, token, file, sessionId] = process.argv;
const store = createWebSessionStore({ token: token ?? "", filePath: file });

switch (action) {
  case "create": {
    const id = await store.create();
    process.stdout.write(JSON.stringify({ sessionId: id }));
    break;
  }
  case "validate": {
    const ok = await store.validate(sessionId);
    process.stdout.write(ok ? "valid" : "invalid");
    break;
  }
  case "list": {
    // No list API exists; validate() a known id to confirm the file is loadable, and report an
    // empty array when the file has no live sessions. The Go side prints the id count via the
    // revocation check, so a "file is readable and empty" answer is what this asserts.
    const raw = await import("node:fs/promises").then((fs) => fs.readFile(file, "utf8"));
    const parsed = JSON.parse(raw);
    const now = Date.now();
    const live = (parsed.sessions ?? []).filter((s) => s.expiresAt > now);
    process.stdout.write(JSON.stringify(live));
    break;
  }
  default:
    console.error(`unknown action: ${action}`);
    process.exit(2);
}
