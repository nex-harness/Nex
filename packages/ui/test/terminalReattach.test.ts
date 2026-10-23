import assert from "node:assert/strict";
import test from "node:test";
import {
  clearPersistedTerminalId,
  persistTerminalId,
  readPersistedTerminalId,
  resolveTerminalCreateOutcome,
} from "../src/terminal/terminalReattach.js";

// 运行：pnpm --dir packages/ui exec tsx --tsconfig tsconfig.json --test test/terminalReattach.test.ts

function installSessionStorage(): Map<string, string> {
  const store = new Map<string, string>();
  const storage = {
    get length() {
      return store.size;
    },
    clear: () => store.clear(),
    getItem: (key: string) => store.get(key) ?? null,
    key: (index: number) => [...store.keys()][index] ?? null,
    removeItem: (key: string) => {
      store.delete(key);
    },
    setItem: (key: string, value: string) => {
      store.set(key, value);
    },
  } satisfies Storage;
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: { sessionStorage: storage },
  });
  return store;
}

function removeWindow(): void {
  Reflect.deleteProperty(globalThis, "window");
}

test("create outcome is reattach only when the returned id equals the requested id", () => {
  // runner 存活：以同一 id 重挂。
  assert.equal(
    resolveTerminalCreateOutcome({ sentTerminalId: "t-7", returnedTerminalId: "t-7" }),
    true,
  );
  // 桌面/local host 忽略 terminalId，返回新 id：按新建处理。
  assert.equal(
    resolveTerminalCreateOutcome({ sentTerminalId: "t-7", returnedTerminalId: "t-8" }),
    false,
  );
  // 首次打开没有历史 id：新建。
  assert.equal(
    resolveTerminalCreateOutcome({ sentTerminalId: null, returnedTerminalId: "t-1" }),
    false,
  );
});

test("persists terminal id per tab id and clears it on tab close", () => {
  const store = installSessionStorage();
  try {
    assert.equal(readPersistedTerminalId("terminal:a"), null);
    persistTerminalId("terminal:a", "t-1");
    persistTerminalId("terminal:b", "t-2");
    assert.equal(readPersistedTerminalId("terminal:a"), "t-1");
    assert.equal(readPersistedTerminalId("terminal:b"), "t-2");
    // tab 之间互不影响。
    clearPersistedTerminalId("terminal:a");
    assert.equal(readPersistedTerminalId("terminal:a"), null);
    assert.equal(readPersistedTerminalId("terminal:b"), "t-2");
    assert.equal(store.size, 1);
  } finally {
    removeWindow();
  }
});

test("degrades to no persistence without window/sessionStorage", () => {
  removeWindow();
  assert.equal(readPersistedTerminalId("terminal:a"), null);
  assert.doesNotThrow(() => persistTerminalId("terminal:a", "t-1"));
  assert.doesNotThrow(() => clearPersistedTerminalId("terminal:a"));
});

test("storage failures never throw", () => {
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: {
      get sessionStorage(): Storage {
        throw new Error("denied");
      },
    },
  });
  try {
    assert.equal(readPersistedTerminalId("terminal:a"), null);
    assert.doesNotThrow(() => persistTerminalId("terminal:a", "t-1"));
    assert.doesNotThrow(() => clearPersistedTerminalId("terminal:a"));
  } finally {
    removeWindow();
  }
});
