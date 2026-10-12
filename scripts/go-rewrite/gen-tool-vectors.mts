/* eslint-disable max-lines -- 向量生成脚本：一条脚本驱动一组真实 TS 类并顺序产出全部向量，拆开会让用例与其期望值分居两个文件。 */
// 从真实 TypeScript 工具处理器导出差分向量：
//   go/testdata/vectors/tools-read.json / tools-write.json / tools-edit.json
//
// 做法：把每个 case 的 fixture 目录写进临时工作区，用真实的 NodeFileSystemAdapter
// 装配 ToolExecutionContext，调用真实 handler + formatModelContent，记录
// {input, 模型可见结果文本, 结构化 output, 结果文件树+内容+mode}。
// Go 侧用同一份 fixture 目录重放，必须产出完全一致的结果。
//
// 每次运行都在临时目录里进行，不写仓库工作区。
import {
  chmodSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  rmSync,
  statSync,
  utimesSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO = resolve(HERE, "../..");

const CORE = join(REPO, "apps/nex-cli/packages/core/src");
const { readToolEntry } = await import(join(CORE, "tool/handlers/read.ts"));
const { writeToolEntry } = await import(join(CORE, "tool/handlers/write.ts"));
const { editToolEntry } = await import(join(CORE, "tool/handlers/edit.ts"));
const { NodeFileSystemAdapter } = await import(
  join(REPO, "apps/nex-cli/packages/adapters/src/fs/index.ts")
);
const { findEditMatch } = await import(join(CORE, "tool/edit-matchers.ts"));
const { createStructuredPatch } = await import(join(CORE, "tool/diff.ts"));
const { createReadFileStateKey, findLatestReadFileState } = await import(
  join(CORE, "tool/read-file-state.ts")
);
const { prepareInitialToolExecutionInput } = await import(
  join(CORE, "tool/input-normalization.ts")
);
const { validateInitialModelToolInput, getInitialInputValidationModelContent } = await import(
  join(CORE, "tool/executor/validation.ts")
);
const { PermissionService } = await import(join(CORE, "permission/service.ts"));
const { recordScenario } = await import(join(HERE, "record/record-agent-session.mjs"));
const { buildProtocolPermissionOptions } = await import(
  join(REPO, "apps/nex-cli/packages/bootstrap/src/permission-options.ts")
);

type AnyRecord = Record<string, unknown>;

interface FixtureFile {
  path: string; // 相对工作区
  content?: string;
  bytes?: number[]; // 二进制 fixture（与 content 二选一）
  mode?: number; // chmod 位
}

interface Case {
  name: string;
  files: FixtureFile[];
  input: AnyRecord;
  /** 预置的 read-state（模拟“先 Read 过再写/改”）。key 是相对路径。 */
  preRead?: Array<{
    path: string;
    /** Read 的原始输入；用于命中 range key */
    input: AnyRecord;
    /** 相对路径对应的绝对路径由运行器填入。null 表示只伪造最小 state。 */
    offset?: number;
    limit?: number;
    /** 是否 partial view（截断）。 */
    isPartialView?: boolean;
  }>;
  /** touch 文件使其 mtime 晚于 preRead（触发 stale）。 */
  bumpMtime?: boolean;
  /** preRead 之后改写文件内容（触发基于内容/mtime 的 stale）。 */
  mutateAfterPreRead?: { path: string; content: string };
}

function writeFixture(root: string, files: FixtureFile[]): void {
  for (const f of files) {
    const abs = join(root, f.path);
    mkdirSync(dirname(abs), { recursive: true });
    if (f.bytes) writeFileSync(abs, Buffer.from(f.bytes));
    else writeFileSync(abs, f.content ?? "");
    if (f.mode !== undefined) {
      chmodSync(abs, f.mode);
    }
  }
}

function snapshotTree(root: string): AnyRecord {
  const out: AnyRecord = {};
  const walk = (rel: string): void => {
    const dir = rel ? join(root, rel) : root;
    for (const name of readdirSync(dir).sort()) {
      const childRel = rel ? `${rel}/${name}` : name;
      const abs = join(dir, name);
      const st = statSync(abs);
      if (st.isDirectory()) {
        walk(childRel);
      } else {
        const lst = lstatSync(abs);
        const buf: Buffer = readFileSync(abs);
        let text: string | null;
        try {
          text = buf.toString("utf8");
        } catch {
          text = null;
        }
        out[childRel] = {
          content: text,
          bytesBase64: buf.toString("base64"),
          sizeBytes: buf.length,
          // 只保留权限位，便于 Go 侧按 0644/0755 复现。
          mode: (lst.mode & 0o777).toString(8),
        };
      }
    }
  };
  walk("");
  return out;
}

interface HarnessResult {
  modelContent: unknown;
  output: unknown;
  failed: boolean;
  failureMessage?: string;
  errorName?: string;
  errorMessage?: string;
  readStateAfter: Array<{ key: string; path: string; isPartialView: boolean }>;
}

/** 递归把所有字符串里的临时工作区根路径换成 <WS>，让向量可复现。 */
function normalizePaths(value: unknown, root: string): unknown {
  if (typeof value === "string") return value.replaceAll(root, "<WS>");
  if (Array.isArray(value)) return value.map((v) => normalizePaths(v, root));
  if (value && typeof value === "object") {
    const out: AnyRecord = {};
    for (const [k, v] of Object.entries(value)) out[k] = normalizePaths(v, root);
    return out;
  }
  return value;
}

async function runHandler(
  entry: {
    handler: (i: unknown, c: unknown) => Promise<unknown>;
    formatModelContent?: (o: unknown) => unknown;
  },
  input: AnyRecord,
  root: string,
  opts: {
    preRead?: Case["preRead"];
    bumpMtime?: boolean;
    mutateAfterPreRead?: Case["mutateAfterPreRead"];
  } = {},
): Promise<HarnessResult> {
  const fs = new NodeFileSystemAdapter();
  const readFileState = new Map();
  const recordMetadata: AnyRecord[] = [];

  // 预置 read-state：先真实跑一次 Read 建立 state，再（可选）改 mtime 触发 stale。
  if (opts.preRead) {
    for (const pr of opts.preRead) {
      const readEntry = readToolEntry;
      const readCtx = makeContext(fs, root, readFileState, recordMetadata);
      const rel = pr.input.file_path as string;
      const abs = rel.startsWith("/") ? rel : join(root, rel);
      const readInput: AnyRecord = { ...pr.input, file_path: abs };
      await readEntry.handler(readInput, readCtx);
    }
    if (opts.mutateAfterPreRead) {
      writeFileSync(join(root, opts.mutateAfterPreRead.path), opts.mutateAfterPreRead.content);
    }
    // bumpMtime 必须放在改写之后：writeFileSync 会把 mtime 重置为“现在”，
    // 若先推后再改写，mtime 是否前进就取决于是否跨过毫秒边界，向量不再可复现。
    if (opts.bumpMtime) {
      for (const pr of opts.preRead) {
        const abs = join(root, pr.input.file_path as string);
        const st = statSync(abs);
        const future = st.mtimeMs + 5000;
        utimesSync(abs, future / 1000, future / 1000);
      }
    }
  }

  const ctx = makeContext(fs, root, readFileState, recordMetadata);
  const absoluteInput: AnyRecord = { ...input };
  if (
    typeof absoluteInput.file_path === "string" &&
    absoluteInput.file_path.length > 0 &&
    !absoluteInput.file_path.startsWith("/")
  ) {
    absoluteInput.file_path = join(root, absoluteInput.file_path);
  } else if (absoluteInput.file_path === "") {
    // 空路径保持为空：真实执行里 resolveWorkspacePath 会对空路径抛错。
  }

  let output: unknown;
  let failed = false;
  let failureMessage: string | undefined;
  let errorName: string | undefined;
  let errorMessage: string | undefined;
  try {
    const raw = await entry.handler(absoluteInput, ctx);
    if (raw && typeof raw === "object" && (raw as AnyRecord).result === false) {
      failed = true;
      failureMessage = String((raw as AnyRecord).message);
      output = raw;
    } else {
      output = raw;
    }
  } catch (error) {
    failed = true;
    errorName = error instanceof Error ? error.name : "Unknown";
    errorMessage = error instanceof Error ? error.message : String(error);
  }

  const modelContent =
    !failed && entry.formatModelContent && output !== undefined
      ? entry.formatModelContent(output)
      : failed
        ? (failureMessage ?? errorMessage)
        : undefined;

  return {
    modelContent,
    output,
    failed,
    failureMessage,
    errorName,
    errorMessage,
    readStateAfter: [...readFileState.entries()].map(([key, v]) => ({
      key: String(key).replaceAll(root, "<WS>"),
      path: String((v as AnyRecord).path).replaceAll(root, "<WS>"),
      isPartialView: (v as AnyRecord).isPartialView === true,
    })),
  };
}

function makeContext(
  fs: unknown,
  root: string,
  readFileState: Map<string, unknown>,
  recordMetadata: AnyRecord[],
): AnyRecord {
  return {
    toolCallId: "call_test",
    traceId: "trace_test",
    abortSignal: new AbortController().signal,
    fileSystemPort: fs,
    readFileState,
    recordReadFileStateMetadata: (m: AnyRecord) => recordMetadata.push(m),
    workingDirectory: root,
    workspaceRoot: root,
    sessionId: "sess_test",
  };
}

// ---------------------------------------------------------------------------
// 用例集
// ---------------------------------------------------------------------------

const TEXT = "alpha\nbeta\ngamma\n";
const MULTI = "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n";
const LONG_LINE = "x".repeat(30000) + "\nshort\n";
const CRLF = "a\r\nb\r\nc\r\n";
const EMPTY = "";

const readCases: Case[] = [
  {
    name: "read/basic",
    files: [{ path: "hello.txt", content: TEXT }],
    input: { file_path: "hello.txt" },
  },
  {
    name: "read/no-trailing-newline",
    files: [{ path: "f.txt", content: "hi there" }],
    input: { file_path: "f.txt" },
  },
  {
    name: "read/offset-limit",
    files: [{ path: "m.txt", content: MULTI }],
    input: { file_path: "m.txt", offset: 3, limit: 4 },
  },
  {
    name: "read/offset-only",
    files: [{ path: "m.txt", content: MULTI }],
    input: { file_path: "m.txt", offset: 5 },
  },
  {
    name: "read/limit-only",
    files: [{ path: "m.txt", content: MULTI }],
    input: { file_path: "m.txt", limit: 3 },
  },
  {
    name: "read/offset-0",
    files: [{ path: "m.txt", content: MULTI }],
    input: { file_path: "m.txt", offset: 0, limit: 2 },
  },
  {
    name: "read/offset-1",
    files: [{ path: "m.txt", content: MULTI }],
    input: { file_path: "m.txt", offset: 1, limit: 2 },
  },
  {
    name: "read/offset-beyond-eof",
    files: [{ path: "m.txt", content: MULTI }],
    input: { file_path: "m.txt", offset: 50 },
  },
  {
    name: "read/limit-0",
    files: [{ path: "m.txt", content: MULTI }],
    input: { file_path: "m.txt", limit: 0 },
  },
  {
    name: "read/empty-file",
    files: [{ path: "e.txt", content: EMPTY }],
    input: { file_path: "e.txt" },
  },
  { name: "read/crlf", files: [{ path: "c.txt", content: CRLF }], input: { file_path: "c.txt" } },
  {
    name: "read/crlf-range",
    files: [{ path: "c.txt", content: CRLF }],
    input: { file_path: "c.txt", offset: 2 },
  },
  {
    name: "read/missing",
    files: [{ path: "other.txt", content: "x\n" }],
    input: { file_path: "nope.txt" },
  },
  {
    name: "read/missing-similar",
    files: [{ path: "hello.txt", content: "x\n" }],
    input: { file_path: "helo.txt" },
  },
  {
    name: "read/missing-same-stem",
    files: [{ path: "data.json", content: "{}\n" }],
    input: { file_path: "data.txt" },
  },
  {
    name: "read/directory",
    files: [{ path: "dir/inner.txt", content: "x\n" }],
    input: { file_path: "dir" },
  },
  {
    name: "read/dot-directory",
    files: [{ path: "dir/inner.txt", content: "x\n" }],
    input: { file_path: "." },
  },
  {
    name: "read/relative-dot",
    files: [{ path: "hello.txt", content: TEXT }],
    input: { file_path: "./hello.txt" },
  },
  {
    name: "read/relative-dotdot",
    files: [{ path: "sub/inner.txt", content: "z\n" }],
    input: { file_path: "sub/../sub/inner.txt" },
  },
  {
    name: "read/utf8-bom",
    files: [
      { path: "b.txt", bytes: [...Buffer.from([0xef, 0xbb, 0xbf]), ...Buffer.from("bom\n")] },
    ],
    input: { file_path: "b.txt" },
  },
  {
    name: "read/utf16le-bom",
    files: [
      { path: "u.txt", bytes: [...Buffer.from([0xff, 0xfe]), ...Buffer.from("hi\n", "utf16le")] },
    ],
    input: { file_path: "u.txt" },
  },
  {
    name: "read/binary-nul",
    files: [{ path: "bin.dat", bytes: [0, 1, 2, 3, 4, 5] }],
    input: { file_path: "bin.dat" },
  },
  {
    // 仅靠 NUL 判定二进制：其余字节全是可打印 ASCII，控制字节比例远低于阈值。
    name: "read/nul-in-ascii",
    files: [{ path: "n.dat", bytes: [65, 66, 67, 0, 68, 69, 70, 71, 72, 73, 74] }],
    input: { file_path: "n.dat" },
  },
  {
    name: "read/unicode",
    files: [{ path: "u.txt", content: "héllo\n世界\n🎉\n" }],
    input: { file_path: "u.txt" },
  },
  {
    name: "read/long-line",
    files: [{ path: "long.txt", content: LONG_LINE }],
    input: { file_path: "long.txt" },
  },
  {
    name: "read/blank-lines",
    files: [{ path: "b.txt", content: "a\n\n\nb\n" }],
    input: { file_path: "b.txt" },
  },
  {
    name: "read/trailing-newline-only",
    files: [{ path: "t.txt", content: "\n" }],
    input: { file_path: "t.txt" },
  },
  { name: "read/no-file-path", files: [], input: { offset: 1 } },
  { name: "read/bad-type-file-path", files: [], input: { file_path: 42 } },
  {
    name: "read/extra-key",
    files: [{ path: "hello.txt", content: TEXT }],
    input: { file_path: "hello.txt", bogus: 1 },
  },
  {
    name: "read/limit-negative",
    files: [{ path: "hello.txt", content: TEXT }],
    input: { file_path: "hello.txt", limit: -3 },
  },
  {
    name: "read/offset-negative",
    files: [{ path: "hello.txt", content: TEXT }],
    input: { file_path: "hello.txt", offset: -1 },
  },
  { name: "read/empty-string-path", files: [], input: { file_path: "" } },
  // 二进制扩展名预检（合同层 custom issue）
  { name: "read/binary-exe-ext", files: [], input: { file_path: "app.exe" } },
  { name: "read/binary-zip-ext", files: [], input: { file_path: "a.zip" } },
  // 设备文件阻断
  { name: "read/device-devzero", files: [], input: { file_path: "/dev/zero" } },
  // 重复读（read-state 命中 file_unchanged）需在 harness 内先读一次
  {
    name: "read/unchanged-second-read",
    files: [{ path: "hello.txt", content: TEXT }],
    preRead: [{ path: "hello.txt", input: { file_path: "hello.txt" } }],
    input: { file_path: "hello.txt" },
  },
  {
    name: "read/gbk-legacy",
    files: [{ path: "gbk.txt", bytes: [214, 208, 206, 196, 178, 226, 202, 212, 10] }],
    input: { file_path: "gbk.txt" },
  },
  {
    name: "read/gb18030-only",
    // U+3400 (㐀) in GB18030 is the 4-byte 81 39 EE 39, which does not round-trip
    // under gb2312 or gbk — detection must reach the third legacy candidate.
    files: [{ path: "g.txt", bytes: [129, 57, 238, 57, 10] }],
    input: { file_path: "g.txt" },
  },
  {
    name: "read/range-then-full",
    files: [{ path: "m.txt", content: MULTI }],
    preRead: [{ path: "m.txt", input: { file_path: "m.txt", offset: 2, limit: 2 } }],
    input: { file_path: "m.txt" },
  },
  // ---- 以下是 G1.8 差分补充用例：覆盖此前未取样的 handler 分支 ----
  {
    // limit 恰好等于剩余行数：不截断，content 保留末尾换行产生的空行。
    name: "read/limit-equals-remaining",
    files: [{ path: "m.txt", content: MULTI }],
    input: { file_path: "m.txt", offset: 8, limit: 3 },
  },
  {
    // offset 落在最后一行（只有一个换行前的空行）：内容为空 + “shorter than offset”提醒。
    name: "read/offset-last-empty-line",
    files: [{ path: "m.txt", content: MULTI }],
    input: { file_path: "m.txt", offset: 11 },
  },
  {
    // 空文件带 offset：空内容 + totalLines 被归一化成 1 后的提醒分支。
    name: "read/empty-file-with-offset",
    files: [{ path: "e.txt", content: EMPTY }],
    input: { file_path: "e.txt", offset: 3 },
  },
  {
    // 目录下的相对路径 + 父目录回退，和 ./ 归一化是不同的输入形状。
    name: "read/nested-relative",
    files: [{ path: "sub/inner.txt", content: TEXT }],
    input: { file_path: "sub/inner.txt" },
  },
  {
    // 同一路径不同 range 各缓存一次：第二个 range 不是 file_unchanged。
    name: "read/same-file-other-range",
    files: [{ path: "m.txt", content: MULTI }],
    preRead: [{ path: "m.txt", input: { file_path: "m.txt", offset: 1, limit: 2 } }],
    input: { file_path: "m.txt", offset: 4, limit: 2 },
  },
  {
    // 只有空白字符的文件：不算空文件，totalLines 来自换行切分。
    name: "read/whitespace-only",
    files: [{ path: "w.txt", content: "   \n\t\n" }],
    input: { file_path: "w.txt" },
  },
  {
    // 单行 CRLF 文件：readTextFileRange 归一化成 LF 后只剩 1 行 + 末尾空行。
    name: "read/single-crlf-line",
    files: [{ path: "c.txt", content: "only\r\n" }],
    input: { file_path: "c.txt" },
  },
  {
    // .md 之类的普通扩展名不是二进制；同一 fixture 同时证明扩展名白名单不误伤。
    name: "read/plain-extension-not-binary",
    files: [{ path: "doc.md", content: "# title\n\nbody\n" }],
    input: { file_path: "doc.md" },
  },
  {
    // 完全没有扩展名的文件（README）：readFileExtension 返回 ""，不触发二进制预检。
    name: "read/no-extension",
    files: [{ path: "README", content: TEXT }],
    input: { file_path: "README" },
  },
  {
    // 控制字节比例刚过阈值（30%）：验证二进制判定边界。
    name: "read/control-byte-ratio",
    files: [{ path: "c.dat", bytes: [0x01, 0x02, 0x03, 0x41, 0x42, 0x43, 0x44, 0x45, 0x46, 0x0a] }],
    input: { file_path: "c.dat" },
  },
];
const writeCases: Case[] = [
  { name: "write/create", files: [], input: { file_path: "new.txt", content: "hello\n" } },
  {
    name: "write/create-deep-parents",
    files: [],
    input: { file_path: "a/b/c/new.txt", content: "deep\n" },
  },
  {
    name: "write/overwrite-after-read",
    files: [{ path: "notes.txt", content: "old\n" }],
    preRead: [{ path: "notes.txt", input: { file_path: "notes.txt" } }],
    input: { file_path: "notes.txt", content: "new\n" },
  },
  {
    name: "write/overwrite-not-read",
    files: [{ path: "notes.txt", content: "old\n" }],
    input: { file_path: "notes.txt", content: "new\n" },
  },
  {
    name: "write/overwrite-stale",
    files: [{ path: "notes.txt", content: "old\n" }],
    preRead: [{ path: "notes.txt", input: { file_path: "notes.txt" } }],
    bumpMtime: true,
    input: { file_path: "notes.txt", content: "new\n" },
  },
  {
    name: "write/overwrite-stale-content",
    files: [{ path: "notes.txt", content: "old\n" }],
    preRead: [{ path: "notes.txt", input: { file_path: "notes.txt" } }],
    bumpMtime: true,
    mutateAfterPreRead: { path: "notes.txt", content: "changed\n" },
    input: { file_path: "notes.txt", content: "new\n" },
  },
  {
    name: "write/overwrite-partial-read",
    files: [{ path: "m.txt", content: MULTI }],
    preRead: [{ path: "m.txt", input: { file_path: "m.txt", offset: 1, limit: 2 } }],
    input: { file_path: "m.txt", content: "replaced\n" },
  },
  {
    name: "write/preserve-mode-0755",
    files: [{ path: "s.sh", content: "#!/bin/sh\n", mode: 0o755 }],
    preRead: [{ path: "s.sh", input: { file_path: "s.sh" } }],
    input: { file_path: "s.sh", content: "#!/bin/sh\necho hi\n" },
  },
  {
    name: "write/preserve-mode-0644",
    files: [{ path: "f.txt", content: "x\n", mode: 0o644 }],
    preRead: [{ path: "f.txt", input: { file_path: "f.txt" } }],
    input: { file_path: "f.txt", content: "yy\n" },
  },
  {
    name: "write/crlf-preserved",
    files: [{ path: "w.txt", content: "a\r\nb\r\n" }],
    preRead: [{ path: "w.txt", input: { file_path: "w.txt" } }],
    input: { file_path: "w.txt", content: "a\nB\n" },
  },
  {
    name: "write/lf-preserved",
    files: [{ path: "w.txt", content: "a\nb\n" }],
    preRead: [{ path: "w.txt", input: { file_path: "w.txt" } }],
    input: { file_path: "w.txt", content: "a\r\nB\r\n" },
  },
  { name: "write/empty-content-create", files: [], input: { file_path: "e.txt", content: "" } },
  {
    name: "write/empty-content-overwrite",
    files: [{ path: "e.txt", content: "x\n" }],
    preRead: [{ path: "e.txt", input: { file_path: "e.txt" } }],
    input: { file_path: "e.txt", content: "" },
  },
  { name: "write/unicode", files: [], input: { file_path: "u.txt", content: "héllo\n世界\n" } },
  {
    name: "write/no-trailing-newline",
    files: [],
    input: { file_path: "n.txt", content: "no newline" },
  },
  { name: "write/no-file-path", files: [], input: { content: "x" } },
  { name: "write/no-content", files: [], input: { file_path: "x.txt" } },
  { name: "write/bad-content-type", files: [], input: { file_path: "x.txt", content: 5 } },
  { name: "write/extra-key", files: [], input: { file_path: "x.txt", content: "a", extra: true } },
  {
    name: "write/overwrite-same-content",
    files: [{ path: "s.txt", content: "same\n" }],
    preRead: [{ path: "s.txt", input: { file_path: "s.txt" } }],
    input: { file_path: "s.txt", content: "same\n" },
  },
  {
    name: "write/append-lines",
    files: [{ path: "a.txt", content: "l1\nl2\n" }],
    preRead: [{ path: "a.txt", input: { file_path: "a.txt" } }],
    input: { file_path: "a.txt", content: "l1\nl2\nl3\n" },
  },
  {
    name: "write/utf16le-roundtrip",
    files: [
      { path: "u16.txt", bytes: [...Buffer.from([0xff, 0xfe]), ...Buffer.from("hi\n", "utf16le")] },
    ],
    preRead: [{ path: "u16.txt", input: { file_path: "u16.txt" } }],
    input: { file_path: "u16.txt", content: "bye\n" },
  },
  { name: "write/dot-relative", files: [], input: { file_path: "./new.txt", content: "z\n" } },
  {
    name: "write/nested-overwrite",
    files: [{ path: "sub/deep/f.txt", content: "old\n" }],
    preRead: [{ path: "sub/deep/f.txt", input: { file_path: "sub/deep/f.txt" } }],
    input: { file_path: "sub/deep/f.txt", content: "new\n" },
  },
  { name: "write/empty-path", files: [], input: { file_path: "", content: "z" } },
  {
    name: "write/many-lines",
    files: [],
    input: {
      file_path: "big.txt",
      content: Array.from({ length: 50 }, (_, i) => `line ${i}`).join("\n") + "\n",
    },
  },
  {
    name: "write/tabs-and-spaces",
    files: [],
    input: { file_path: "t.txt", content: "\tindent\n    spaces\n" },
  },
  {
    name: "write/overwrite-binary-ext-create",
    files: [],
    input: { file_path: "x.bin", content: "text-in-bin\n" },
  },
  {
    name: "write/stale-after-partial-still-reads",
    files: [{ path: "m.txt", content: MULTI }],
    preRead: [{ path: "m.txt", input: { file_path: "m.txt", offset: 1, limit: 2 } }],
    input: { file_path: "m.txt", content: "full replace\n" },
  },
  {
    name: "write/create-existing-mode-preserved",
    files: [{ path: "x.sh", content: "#!/bin/sh\n", mode: 0o700 }],
    preRead: [{ path: "x.sh", input: { file_path: "x.sh" } }],
    input: { file_path: "x.sh", content: "#!/bin/sh\n:\n" },
  },
  {
    name: "write/newline-in-path-parent",
    files: [],
    input: { file_path: "dir/sub/i.txt", content: "v\n" },
  },
  // ---- 以下是 G1.8 差分补充用例：覆盖此前未取样的 Write handler 分支 ----
  {
    // 内容变短：structuredPatch 的 oldLines/newLines 不相等，负数上下文裁剪。
    name: "write/shrink-content",
    files: [{ path: "long.txt", content: MULTI }],
    preRead: [{ path: "long.txt", input: { file_path: "long.txt" } }],
    input: { file_path: "long.txt", content: "one\n" },
  },
  {
    // 内容变长并跨越多个 hunk 上下文边界。
    name: "write/grow-content",
    files: [{ path: "short.txt", content: TEXT }],
    preRead: [{ path: "short.txt", input: { file_path: "short.txt" } }],
    input: {
      file_path: "short.txt",
      content: Array.from({ length: 30 }, (_, i) => `added ${i}`).join("\n") + "\n",
    },
  },
  {
    // 只改末尾一行：hunk 必须落在文件尾部而不是开头。
    name: "write/edit-tail-line",
    files: [{ path: "m.txt", content: MULTI }],
    preRead: [{ path: "m.txt", input: { file_path: "m.txt" } }],
    input: {
      file_path: "m.txt",
      content: "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nTEN\n",
    },
  },
  {
    // GBK 文件读回后按原编码写回：结构化 patch 比对的是解码后的原文。
    name: "write/gbk-roundtrip",
    files: [{ path: "gbk.txt", bytes: [214, 208, 206, 196, 178, 226, 202, 212, 10] }],
    preRead: [{ path: "gbk.txt", input: { file_path: "gbk.txt" } }],
    input: { file_path: "gbk.txt", content: "改写\n" },
  },
  {
    // 预置 read-state 后内容被替换但大小不变：size 与 mtime 都没变时必须靠内容比较
    // 判为 stale。bumpMtime 是必需的——mtime 只精确到毫秒，不显式推后的话它是否
    // 前进取决于机器快慢，向量就不可复现了。
    name: "write/same-size-different-content",
    files: [{ path: "s.txt", content: "aaaa\n" }],
    preRead: [{ path: "s.txt", input: { file_path: "s.txt" } }],
    bumpMtime: true,
    mutateAfterPreRead: { path: "s.txt", content: "bbbb\n" },
    input: { file_path: "s.txt", content: "cccc\n" },
  },
  {
    // 只读过 range 再整文件写回：partial-range state 不是 partial view，仍然放行。
    name: "write/range-read-then-full-write",
    files: [{ path: "m.txt", content: MULTI }],
    preRead: [{ path: "m.txt", input: { file_path: "m.txt", offset: 2, limit: 3 } }],
    input: { file_path: "m.txt", content: MULTI },
  },
  {
    // 以 "." 开头的隐藏文件：路径策略不做过滤，原子写直接落盘。
    name: "write/dotfile",
    files: [],
    input: { file_path: ".env", content: "KEY=value\n" },
  },
  {
    // 父目录是已存在文件：mkdir 失败必须原样冒泡，不得静默创建。
    name: "write/parent-is-file",
    files: [{ path: "blocker", content: "i am a file\n" }],
    input: { file_path: "blocker/child.txt", content: "nope\n" },
  },
  {
    // 内容里含 NUL：Write 不做编码探测，原样写入（与 Read 的二进制拒绝相反）。
    name: "write/content-with-nul",
    files: [],
    input: { file_path: "nul.txt", content: "a\u0000b\n" },
  },
  {
    // 写入 CRLF 内容到新建文件：新文件没有 lineEndings 可继承，原样落盘。
    name: "write/create-crlf-content",
    files: [],
    input: { file_path: "crlf.txt", content: "a\r\nb\r\n" },
  },
];

const editCases: Case[] = [
  {
    name: "edit/exact",
    files: [{ path: "n.txt", content: TEXT }],
    preRead: [{ path: "n.txt", input: { file_path: "n.txt" } }],
    input: { file_path: "n.txt", old_string: "beta", new_string: "BETA" },
  },
  {
    name: "edit/exact-replace-all",
    files: [{ path: "n.txt", content: "aa\nbb\naa\ncc\naa\n" }],
    preRead: [{ path: "n.txt", input: { file_path: "n.txt" } }],
    input: { file_path: "n.txt", old_string: "aa", new_string: "AA", replace_all: true },
  },
  {
    name: "edit/ambiguous",
    files: [{ path: "n.txt", content: "aa\nbb\naa\n" }],
    preRead: [{ path: "n.txt", input: { file_path: "n.txt" } }],
    input: { file_path: "n.txt", old_string: "aa", new_string: "AA" },
  },
  {
    name: "edit/not-found",
    files: [{ path: "n.txt", content: TEXT }],
    preRead: [{ path: "n.txt", input: { file_path: "n.txt" } }],
    input: { file_path: "n.txt", old_string: "nope", new_string: "x" },
  },
  {
    name: "edit/no-op",
    files: [{ path: "n.txt", content: TEXT }],
    preRead: [{ path: "n.txt", input: { file_path: "n.txt" } }],
    input: { file_path: "n.txt", old_string: "beta", new_string: "beta" },
  },
  {
    name: "edit/not-read",
    files: [{ path: "n.txt", content: TEXT }],
    input: { file_path: "n.txt", old_string: "beta", new_string: "BETA" },
  },
  {
    name: "edit/stale",
    files: [{ path: "n.txt", content: TEXT }],
    preRead: [{ path: "n.txt", input: { file_path: "n.txt" } }],
    bumpMtime: true,
    input: { file_path: "n.txt", old_string: "beta", new_string: "BETA" },
  },
  {
    name: "edit/partial-read-rejected",
    files: [{ path: "m.txt", content: MULTI }],
    preRead: [{ path: "m.txt", input: { file_path: "m.txt", offset: 1, limit: 2 } }],
    input: { file_path: "m.txt", old_string: "two", new_string: "TWO" },
  },
  {
    name: "edit/missing-file",
    files: [{ path: "other.txt", content: "x\n" }],
    preRead: [],
    input: { file_path: "nope.txt", old_string: "a", new_string: "b" },
  },
  {
    name: "edit/create-with-empty-old-string",
    files: [],
    input: { file_path: "new.txt", old_string: "", new_string: "brand new\n" },
  },
  {
    name: "edit/empty-old-string-existing-nonempty",
    files: [{ path: "e.txt", content: "content\n" }],
    preRead: [{ path: "e.txt", input: { file_path: "e.txt" } }],
    input: { file_path: "e.txt", old_string: "", new_string: "new\n" },
  },
  {
    name: "edit/multiline-exact",
    files: [{ path: "m.txt", content: "one\ntwo\nthree\n" }],
    preRead: [{ path: "m.txt", input: { file_path: "m.txt" } }],
    input: { file_path: "m.txt", old_string: "one\ntwo", new_string: "ONE\nTWO" },
  },
  {
    name: "edit/quote-normalized",
    files: [{ path: "q.txt", content: "say \u201chello\u201d\n" }],
    preRead: [{ path: "q.txt", input: { file_path: "q.txt" } }],
    input: { file_path: "q.txt", old_string: 'say "hello"', new_string: "said hi" },
  },
  {
    name: "edit/line-number-prefix-stripped",
    files: [{ path: "p.txt", content: "alpha\nbeta\n" }],
    preRead: [{ path: "p.txt", input: { file_path: "p.txt" } }],
    input: { file_path: "p.txt", old_string: "2\tbeta", new_string: "BETA" },
  },
  {
    name: "edit/escape-normalized",
    files: [{ path: "e.txt", content: "tab\there\n" }],
    preRead: [{ path: "e.txt", input: { file_path: "e.txt" } }],
    input: { file_path: "e.txt", old_string: "tab\\there", new_string: "TAB" },
  },
  {
    name: "edit/line-trimmed",
    files: [{ path: "t.txt", content: "  indented line\n" }],
    preRead: [{ path: "t.txt", input: { file_path: "t.txt" } }],
    input: { file_path: "t.txt", old_string: "indented line", new_string: "flat line" },
  },
  {
    name: "edit/indentation-flexible",
    files: [{ path: "i.txt", content: "def f():\n    return 1\n    return 2\n" }],
    preRead: [{ path: "i.txt", input: { file_path: "i.txt" } }],
    input: {
      file_path: "i.txt",
      old_string: "def f():\n  return 1\n  return 2",
      new_string: "def g():\n    return 3\n    return 4",
    },
  },
  {
    name: "edit/replace-all-false-multi",
    files: [{ path: "m.txt", content: "x\nx\n" }],
    preRead: [{ path: "m.txt", input: { file_path: "m.txt" } }],
    input: { file_path: "m.txt", old_string: "x", new_string: "y" },
  },
  {
    name: "edit/replace-all-string-true",
    files: [{ path: "m.txt", content: "x\nx\n" }],
    preRead: [{ path: "m.txt", input: { file_path: "m.txt" } }],
    input: { file_path: "m.txt", old_string: "x", new_string: "y", replace_all: "true" },
  },
  {
    name: "edit/replace-all-string-one",
    files: [{ path: "m.txt", content: "x\nx\n" }],
    preRead: [{ path: "m.txt", input: { file_path: "m.txt" } }],
    input: { file_path: "m.txt", old_string: "x", new_string: "y", replace_all: "1" },
  },
  {
    name: "edit/replace-all-string-on",
    files: [{ path: "m.txt", content: "x\nx\n" }],
    preRead: [{ path: "m.txt", input: { file_path: "m.txt" } }],
    input: { file_path: "m.txt", old_string: "x", new_string: "y", replace_all: "yes" },
  },
  {
    name: "edit/replace-all-string-off",
    files: [{ path: "m.txt", content: "x\nx\n" }],
    preRead: [{ path: "m.txt", input: { file_path: "m.txt" } }],
    input: { file_path: "m.txt", old_string: "x", new_string: "y", replace_all: "off" },
  },
  {
    name: "edit/crlf-normalized",
    files: [{ path: "c.txt", content: "a\r\nb\r\n" }],
    preRead: [{ path: "c.txt", input: { file_path: "c.txt" } }],
    input: { file_path: "c.txt", old_string: "a\nb", new_string: "A\nB" },
  },
  {
    name: "edit/ipynb-refused",
    files: [{ path: "n.ipynb", content: "{}\n" }],
    preRead: [{ path: "n.ipynb", input: { file_path: "n.ipynb" } }],
    input: { file_path: "n.ipynb", old_string: "{}", new_string: "x" },
  },
  {
    name: "edit/no-file-path",
    files: [],
    input: { old_string: "a", new_string: "b" },
  },
  {
    name: "edit/empty-path",
    files: [],
    input: { file_path: "", old_string: "a", new_string: "b" },
  },
  {
    name: "edit/no-old-string",
    files: [],
    input: { file_path: "x.txt", new_string: "b" },
  },
  {
    name: "edit/no-new-string",
    files: [],
    input: { file_path: "x.txt", old_string: "a" },
  },
  {
    name: "edit/extra-key",
    files: [{ path: "n.txt", content: TEXT }],
    preRead: [{ path: "n.txt", input: { file_path: "n.txt" } }],
    input: { file_path: "n.txt", old_string: "beta", new_string: "BETA", bogus: 1 },
  },
  {
    name: "edit/delete-line",
    files: [{ path: "d.txt", content: "keep\ndelete me\nkeep2\n" }],
    preRead: [{ path: "d.txt", input: { file_path: "d.txt" } }],
    input: { file_path: "d.txt", old_string: "delete me\n", new_string: "" },
  },
  {
    name: "edit/replace-with-empty",
    files: [{ path: "d.txt", content: "a\nb\nc\n" }],
    preRead: [{ path: "d.txt", input: { file_path: "d.txt" } }],
    input: { file_path: "d.txt", old_string: "b\n", new_string: "" },
  },
  {
    name: "edit/ambiguous-digit",
    files: [{ path: "h.txt", content: "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n13\n14\n15\n16\n" }],
    preRead: [{ path: "h.txt", input: { file_path: "h.txt" } }],
    input: { file_path: "h.txt", old_string: "2", new_string: "TWO" },
  },
  {
    name: "edit/stale-content-changed",
    files: [{ path: "n.txt", content: "old\n" }],
    preRead: [{ path: "n.txt", input: { file_path: "n.txt" } }],
    bumpMtime: true,
    mutateAfterPreRead: { path: "n.txt", content: "changed by user\n" },
    input: { file_path: "n.txt", old_string: "old", new_string: "NEW" },
  },
  {
    name: "edit/spans-newline-hunk",
    files: [{ path: "s.txt", content: "a\nb\nc\nd\ne\nf\ng\nh\n" }],
    preRead: [{ path: "s.txt", input: { file_path: "s.txt" } }],
    input: { file_path: "s.txt", old_string: "b\nc", new_string: "b\nNEW1\nNEW2\nc" },
  },
  {
    name: "edit/append-at-eof-no-newline",
    files: [{ path: "e.txt", content: "line1\nline2" }],
    preRead: [{ path: "e.txt", input: { file_path: "e.txt" } }],
    input: { file_path: "e.txt", old_string: "line2", new_string: "line2\nline3" },
  },
  {
    name: "edit/unicode-escape",
    files: [{ path: "u.txt", content: "café\n" }],
    preRead: [{ path: "u.txt", input: { file_path: "u.txt" } }],
    input: { file_path: "u.txt", old_string: "caf\\u00e9", new_string: "tea" },
  },
  {
    name: "edit/replace-all-true-broad-matcher-skipped",
    files: [{ path: "b.txt", content: "  x\n  x\n" }],
    preRead: [{ path: "b.txt", input: { file_path: "b.txt" } }],
    input: { file_path: "b.txt", old_string: "x", new_string: "y", replace_all: true },
  },
  {
    name: "edit/dollar-signs-in-replacement",
    files: [{ path: "d.txt", content: "cost\n" }],
    preRead: [{ path: "d.txt", input: { file_path: "d.txt" } }],
    input: { file_path: "d.txt", old_string: "cost", new_string: "$100 & $$" },
  },
  {
    name: "edit/case-sensitive",
    files: [{ path: "c.txt", content: "ABC\n" }],
    preRead: [{ path: "c.txt", input: { file_path: "c.txt" } }],
    input: { file_path: "c.txt", old_string: "abc", new_string: "xyz" },
  },
  // ---- 以下是 G1.8 差分补充用例：覆盖此前未取样的 Edit handler / matcher 分支 ----
  {
    // block_anchor：首尾行相同、中间行相似度 >= 0.8，只有这一个候选块。
    name: "edit/block-anchor-match",
    files: [
      {
        path: "a.txt",
        content:
          "function a() {\n  const alpha = 1;\n  const beta = 2;\n  return alpha + beta;\n}\n",
      },
    ],
    preRead: [{ path: "a.txt", input: { file_path: "a.txt" } }],
    input: {
      file_path: "a.txt",
      old_string:
        "function a() {\n  const alpha = 1;\n  const beta = 3;\n  return alpha + beta;\n}",
      new_string: "function a() {\n  return 0;\n}",
    },
  },
  {
    // block_anchor 首尾相同但出现两次：ambiguous，且只报一次候选计数。
    name: "edit/block-anchor-ambiguous",
    files: [
      {
        path: "a.txt",
        content:
          "function a() {\n  const alpha = 1;\n  const beta = 2;\n  return alpha;\n}\n\nfunction a() {\n  const alpha = 9;\n  const beta = 8;\n  return alpha;\n}\n",
      },
    ],
    preRead: [{ path: "a.txt", input: { file_path: "a.txt" } }],
    input: {
      file_path: "a.txt",
      old_string: "function a() {\n  const alpha = 7;\n  const beta = 6;\n  return alpha;\n}",
      new_string: "function a() { return 0; }",
    },
  },
  {
    // replace_all=true 时跳过 block_anchor，只剩精确匹配：唯一一处被替换。
    name: "edit/replace-all-skips-block-anchor",
    files: [{ path: "a.txt", content: "x = 1\ny = 2\n" }],
    preRead: [{ path: "a.txt", input: { file_path: "a.txt" } }],
    input: { file_path: "a.txt", old_string: "x = 1", new_string: "x = 9", replace_all: true },
  },
  {
    // curly 引号归一化匹配后，replacement 必须沿用原文件的引号风格。
    name: "edit/curly-quote-preserved",
    files: [{ path: "q.txt", content: "say “hello” now\n" }],
    preRead: [{ path: "q.txt", input: { file_path: "q.txt" } }],
    input: { file_path: "q.txt", old_string: 'say "hello"', new_string: 'say "goodbye"' },
  },
  {
    // empty old_string 作用在只含空白的已存在文件上：trim() 非空，因此走“已存在”分支。
    name: "edit/empty-old-string-whitespace-file",
    files: [{ path: "w.txt", content: "   \n" }],
    preRead: [{ path: "w.txt", input: { file_path: "w.txt" } }],
    input: { file_path: "w.txt", old_string: "", new_string: "content\n" },
  },
  {
    // 目标是目录：stat 返回目录而不是缺失文件，handler 继续走到读文件失败。
    name: "edit/target-is-directory",
    files: [{ path: "dir/inner.txt", content: TEXT }],
    preRead: [{ path: "dir/inner.txt", input: { file_path: "dir/inner.txt" } }],
    input: { file_path: "dir", old_string: "alpha", new_string: "beta" },
  },
  {
    // old_string 多行、new_string 为空：整块删除，patch 的删除行连续。
    name: "edit/multiline-delete",
    files: [{ path: "m.txt", content: MULTI }],
    preRead: [{ path: "m.txt", input: { file_path: "m.txt" } }],
    input: { file_path: "m.txt", old_string: "three\nfour\nfive", new_string: "" },
  },
  {
    // 读过的文件在读之后被 touch（内容不变）：mtime 变化不得误判为 stale。
    name: "edit/touched-but-identical",
    files: [{ path: "n.txt", content: TEXT }],
    preRead: [{ path: "n.txt", input: { file_path: "n.txt" } }],
    bumpMtime: true,
    input: { file_path: "n.txt", old_string: "beta", new_string: "BETA" },
  },
  {
    // line_trimmed 归一化：old_string 尾部多余空白被裁掉后匹配整行。
    name: "edit/line-trimmed-trailing-space",
    files: [{ path: "t.txt", content: "keep\n  value here  \nkeep\n" }],
    preRead: [{ path: "t.txt", input: { file_path: "t.txt" } }],
    input: { file_path: "t.txt", old_string: "value here", new_string: "value there" },
  },
  {
    // new_string 里的转义序列只对 escape_normalized 生效，exact 匹配必须原样写入。
    name: "edit/exact-keeps-escape-literal",
    files: [{ path: "e.txt", content: "value = 1\n" }],
    preRead: [{ path: "e.txt", input: { file_path: "e.txt" } }],
    input: { file_path: "e.txt", old_string: "value = 1", new_string: "value = \\t2" },
  },
];

// ---------------------------------------------------------------------------
// 运行
// ---------------------------------------------------------------------------

interface VectorsOut {
  tool: string;
  cases: Array<
    AnyRecord & {
      name: string;
      files: FixtureFile[];
      input: AnyRecord;
      result: HarnessResult & { treeAfter: AnyRecord };
    }
  >;
}

async function collect(
  tool: string,
  entry: {
    handler: (i: unknown, c: unknown) => Promise<unknown>;
    formatModelContent?: (o: unknown) => unknown;
  },
  cases: Case[],
): Promise<VectorsOut> {
  const out: VectorsOut = { tool, cases: [] };
  for (const c of cases) {
    const root = mkdtempSync(join(tmpdir(), `toolvec-${tool}-`));
    try {
      writeFixture(root, c.files);
      const result = await runHandler(entry, c.input, root, {
        preRead: c.preRead,
        bumpMtime: c.bumpMtime,
        mutateAfterPreRead: c.mutateAfterPreRead,
      });
      const treeAfter = snapshotTree(root);
      out.cases.push({
        name: c.name,
        files: c.files.map((f) => ({ ...f, content: f.bytes ? undefined : (f.content ?? "") })),
        preRead: c.preRead,
        bumpMtime: c.bumpMtime ?? false,
        mutateAfterPreRead: c.mutateAfterPreRead,
        input: c.input,
        result: {
          ...(normalizePaths(result, root) as HarnessResult),
          treeAfter: normalizePaths(treeAfter, root) as AnyRecord,
        },
      });
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  }
  return out;
}

// read-state 命中查询结果（供 read 用例的 file_unchanged 断言）
async function collectReadOnlyExtras(): Promise<AnyRecord[]> {
  const extras: AnyRecord[] = [];
  const cases = [{ content: TEXT, input: { file_path: "f.txt", offset: 2, limit: 3 } }];
  for (const c of cases) {
    const root = mkdtempSync(join(tmpdir(), "toolvec-key-"));
    writeFixture(root, [{ path: "f.txt", content: c.content }]);
    const key = createReadFileStateKey(join(root, "f.txt"), c.input.offset, c.input.limit);
    extras.push({ path: "f.txt", input: c.input, key: key.replaceAll(root, "<WS>") });
    rmSync(root, { recursive: true, force: true });
  }
  return extras;
}

// ---------------------------------------------------------------------------
// 编辑匹配器级向量（直接测 findEditMatch）与 diff hunk 向量
// ---------------------------------------------------------------------------

const matcherCases = [
  { content: TEXT, search: "beta", replaceAll: false },
  { content: TEXT, search: "a", replaceAll: false },
  { content: TEXT, search: "a", replaceAll: true },
  { content: "say \u201chi\u201d\n", search: 'say "hi"', replaceAll: false },
  { content: "x\nx\n", search: "x", replaceAll: false },
  { content: "  indented\n", search: "indented", replaceAll: false },
  { content: "ab\ncd\n", search: "1\tab\n2\tcd", replaceAll: false },
  { content: "caf\u00e9\n", search: "caf\\u00e9", replaceAll: false },
  { content: "def f():\n    a\n    b\n", search: "def f():\n  a\n  b", replaceAll: false },
  { content: "line1\nline2\n", search: "missing", replaceAll: false },
  { content: "tab\there\n", search: "tab\\there", replaceAll: false },
  { content: "no match here\n", search: "", replaceAll: false },
];

const diffCases = [
  { old: TEXT, newContent: "alpha\nBETA\ngamma\n" },
  { old: "a\nb\nc\n", newContent: "a\nc\n" },
  { old: "line1\n", newContent: "line1\nline2\n" },
  { old: "", newContent: "brand new\n" },
  {
    old: "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n",
    newContent: "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nTEN\n",
  },
  { old: "x", newContent: "y" },
  { old: "a\nb\n", newContent: "a\nb\n" },
  {
    old: "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n13\n14\n15\n16\n",
    newContent: "1\n2\nTHREE\n4\n5\n6\n7\n8\n9\n10\n11\n12\n13\n14\n15\n16\n",
  },
  { old: "keep\ndelete me\nkeep2\n", newContent: "keep\nkeep2\n" },
  // 变化后跟一长段公共内容：hunk 收尾必须把上下文夹到 3 行（context clamp）。
  {
    old: "a\nCHANGED\nc\nd\ne\nf\ng\nh\ni\nj\nk\nl\nm\nn\no\np\n",
    newContent: "a\nNEW\nc\nd\ne\nf\ng\nh\ni\nj\nk\nl\nm\nn\no\np\n",
  },
  // 变化前也跟一长段公共内容。
  {
    old: "a\nb\nc\nd\ne\nf\ng\nOLD\ni\n",
    newContent: "a\nb\nc\nd\ne\nf\ng\nNEW\ni\n",
  },
  // 开头变化 + 长尾公共。
  {
    old: "HEAD\nb\nc\nd\ne\nf\ng\nh\n",
    newContent: "TOP\nb\nc\nd\ne\nf\ng\nh\n",
  },
];

// ---------------------------------------------------------------------------
// 输入校验向量（模型看到的 InputValidationError 文本）
// ---------------------------------------------------------------------------

const encodingFixtures: Array<{ name: string; bytes: number[] }> = [
  { name: "utf8", bytes: [...Buffer.from("hi\n")] },
  { name: "utf8-bom", bytes: [0xef, 0xbb, 0xbf, ...Buffer.from("hi\n")] },
  { name: "utf16le-bom", bytes: [...Buffer.from([0xff, 0xfe]), ...Buffer.from("hi\n", "utf16le")] },
  { name: "gbk-legacy", bytes: [214, 208, 206, 196, 178, 226, 202, 212, 10] },
  { name: "gb18030-only", bytes: [129, 57, 238, 57, 10] },
  { name: "binary-nul", bytes: [0, 1, 2, 3] },
  { name: "crlf", bytes: [...Buffer.from("a\r\nb\r\n")] },
];

async function collectEncodings(): Promise<AnyRecord[]> {
  const out: AnyRecord[] = [];
  for (const f of encodingFixtures) {
    const root = mkdtempSync(join(tmpdir(), "toolvec-enc-"));
    try {
      const abs = join(root, f.name + ".dat");
      writeFileSync(abs, Buffer.from(f.bytes));
      const fs = new NodeFileSystemAdapter();
      try {
        const read = await fs.readTextFile({ path: abs });
        out.push({
          name: f.name,
          bytes: f.bytes,
          encoding: read.encoding,
          lineEndings: read.lineEndings ?? null,
          content: read.content,
          failed: false,
        });
      } catch (error) {
        out.push({
          name: f.name,
          bytes: f.bytes,
          failed: true,
          errorName: error instanceof Error ? error.name : "Unknown",
          // 临时目录名会变；错误文本里的路径替换成 <WS>，否则向量无法逐字节复现。
          errorMessage: normalizePaths(
            error instanceof Error ? error.message : String(error),
            root,
          ) as string,
        });
      }
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  }
  return out;
}

const entriesByTool: Record<string, { metadata: { name: string }; [k: string]: unknown }> = {
  Read: readToolEntry,
  Write: writeToolEntry,
  Edit: editToolEntry,
};

const validationCases: Array<{ tool: string; name: string; input: unknown }> = [
  // Read
  { tool: "Read", name: "val/read-missing-file-path", input: { offset: 1 } },
  { tool: "Read", name: "val/read-wrong-type", input: { file_path: 42 } },
  { tool: "Read", name: "val/read-extra-key", input: { file_path: "a.txt", bogus: 1 } },
  { tool: "Read", name: "val/read-limit-zero", input: { file_path: "a.txt", limit: 0 } },
  { tool: "Read", name: "val/read-limit-float", input: { file_path: "a.txt", limit: 1.5 } },
  { tool: "Read", name: "val/read-offset-negative", input: { file_path: "a.txt", offset: -1 } },
  { tool: "Read", name: "val/read-valid", input: { file_path: "a.txt" } },
  { tool: "Read", name: "val/read-not-object", input: "nope" },
  { tool: "Read", name: "val/read-binary-ext", input: { file_path: "a.exe" } },
  { tool: "Read", name: "val/read-device", input: { file_path: "/dev/zero" } },
  { tool: "Read", name: "val/read-pages-bad", input: { file_path: "a.pdf", pages: "abc" } },
  { tool: "Read", name: "val/read-pages-open-range", input: { file_path: "a.pdf", pages: "5-" } },
  {
    tool: "Read",
    name: "val/read-pages-open-range-bad",
    input: { file_path: "a.pdf", pages: "-" },
  },
  { tool: "Read", name: "val/read-pages-too-large", input: { file_path: "a.pdf", pages: "1-50" } },
  { tool: "Read", name: "val/read-pages-ok", input: { file_path: "a.pdf", pages: "1-5" } },
  // Write
  { tool: "Write", name: "val/write-missing-content", input: { file_path: "a.txt" } },
  { tool: "Write", name: "val/write-missing-path", input: { content: "x" } },
  { tool: "Write", name: "val/write-wrong-type", input: { file_path: "a.txt", content: 5 } },
  { tool: "Write", name: "val/write-extra-key", input: { file_path: "a.txt", content: "x", z: 1 } },
  { tool: "Write", name: "val/write-valid", input: { file_path: "a.txt", content: "x" } },
  { tool: "Write", name: "val/write-not-object", input: [1, 2] },
  // Edit
  { tool: "Edit", name: "val/edit-missing-old", input: { file_path: "a.txt", new_string: "b" } },
  { tool: "Edit", name: "val/edit-missing-path", input: { old_string: "a", new_string: "b" } },
  {
    tool: "Edit",
    name: "val/edit-replace-all-string",
    input: { file_path: "a.txt", old_string: "a", new_string: "b", replace_all: "true" },
  },
  {
    tool: "Edit",
    name: "val/edit-replace-all-number",
    input: { file_path: "a.txt", old_string: "a", new_string: "b", replace_all: 1 },
  },
  {
    tool: "Edit",
    name: "val/edit-replace-all-str-1",
    input: { file_path: "a.txt", old_string: "a", new_string: "b", replace_all: "1" },
  },
  {
    tool: "Edit",
    name: "val/edit-replace-all-str-yes",
    input: { file_path: "a.txt", old_string: "a", new_string: "b", replace_all: "YES" },
  },
  {
    tool: "Edit",
    name: "val/edit-replace-all-str-on",
    input: { file_path: "a.txt", old_string: "a", new_string: "b", replace_all: " on " },
  },
  {
    tool: "Edit",
    name: "val/edit-replace-all-str-false",
    input: { file_path: "a.txt", old_string: "a", new_string: "b", replace_all: "false" },
  },
  {
    tool: "Edit",
    name: "val/edit-replace-all-str-0",
    input: { file_path: "a.txt", old_string: "a", new_string: "b", replace_all: "0" },
  },
  {
    tool: "Edit",
    name: "val/edit-replace-all-str-off",
    input: { file_path: "a.txt", old_string: "a", new_string: "b", replace_all: "off" },
  },
  {
    tool: "Edit",
    name: "val/edit-replace-all-bool-false",
    input: { file_path: "a.txt", old_string: "a", new_string: "b", replace_all: false },
  },
  {
    tool: "Edit",
    name: "val/edit-replace-all-number-0",
    input: { file_path: "a.txt", old_string: "a", new_string: "b", replace_all: 0 },
  },
  {
    tool: "Edit",
    name: "val/edit-extra-key",
    input: { file_path: "a.txt", old_string: "a", new_string: "b", q: 1 },
  },
  {
    tool: "Edit",
    name: "val/edit-valid",
    input: { file_path: "a.txt", old_string: "a", new_string: "b" },
  },
];

async function collectValidation(): Promise<AnyRecord[]> {
  const out: AnyRecord[] = [];
  for (const c of validationCases) {
    const entry = entriesByTool[c.tool] as unknown as {
      runtimeInputSchema?: unknown;
      inputSchema?: unknown;
      metadata: { name: string };
    };
    const prepared = prepareInitialToolExecutionInput({ entry: entry as never, input: c.input });
    let error: Error | undefined;
    let modelContent: string | undefined;
    const runtimeIssues = (prepared as { runtimeValidationIssues?: unknown })
      .runtimeValidationIssues;
    try {
      error = validateInitialModelToolInput(prepared.input, entry as never, runtimeIssues as never);
      if (error) modelContent = getInitialInputValidationModelContent(error);
    } catch (e) {
      error = e as Error;
    }
    out.push({
      tool: c.tool,
      name: c.name,
      input: c.input,
      normalizedInput: prepared.input,
      runtimeValidationIssues: runtimeIssues ?? null,
      valid: error === undefined,
      errorMessage: error instanceof Error ? error.message : String(error ?? ""),
      modelContent: modelContent ?? null,
    });
  }
  return out;
}

// ---------------------------------------------------------------------------
// 权限向量（PermissionService.checkPermission + 选项投影）
// ---------------------------------------------------------------------------

const permissionCases: Array<{
  tool: string;
  mode: string;
  input: unknown;
  planEnabled?: boolean;
  projectRules?: unknown;
  sessionGranted?: unknown[];
  name: string;
}> = [
  { tool: "Read", mode: "build", input: { file_path: "/ws/a.txt" }, name: "perm/read-build" },
  {
    tool: "Write",
    mode: "build",
    input: { file_path: "/ws/a.txt", content: "x" },
    name: "perm/write-build",
  },
  { tool: "Edit", mode: "build", input: { file_path: "/ws/a.txt" }, name: "perm/edit-build" },
  {
    tool: "Write",
    mode: "edit",
    input: { file_path: "/ws/a.txt", content: "x" },
    name: "perm/write-edit-mode",
  },
  { tool: "Edit", mode: "edit", input: { file_path: "/ws/a.txt" }, name: "perm/edit-edit-mode" },
  { tool: "Read", mode: "edit", input: { file_path: "/ws/a.txt" }, name: "perm/read-edit-mode" },
  { tool: "Read", mode: "plan", input: { file_path: "/ws/a.txt" }, name: "perm/read-plan" },
  {
    tool: "Write",
    mode: "plan",
    input: { file_path: "/ws/a.txt", content: "x" },
    name: "perm/write-plan",
  },
  { tool: "Edit", mode: "plan", input: { file_path: "/ws/a.txt" }, name: "perm/edit-plan" },
  {
    tool: "Write",
    mode: "yolo",
    input: { file_path: "/ws/a.txt", content: "x" },
    name: "perm/write-yolo",
  },
  { tool: "Edit", mode: "yolo", input: { file_path: "/ws/a.txt" }, name: "perm/edit-yolo" },
  { tool: "Read", mode: "yolo", input: { file_path: "/ws/a.txt" }, name: "perm/read-yolo" },
  {
    tool: "Write",
    mode: "build",
    input: { file_path: "/ws/a.txt", content: "x" },
    projectRules: { version: 1, allow: [{ toolName: "Write", ruleContent: "/ws/a.txt" }] },
    name: "perm/write-project-allow-exact",
  },
  {
    tool: "Write",
    mode: "build",
    input: { file_path: "/ws/other.txt", content: "x" },
    projectRules: { version: 1, allow: [{ toolName: "Write", ruleContent: "/ws/a.txt" }] },
    name: "perm/write-project-allow-nomatch",
  },
  {
    tool: "Edit",
    mode: "build",
    input: { file_path: "/ws/a.txt" },
    projectRules: { version: 1, allow: [{ toolName: "Write", ruleContent: "/ws/a.txt" }] },
    name: "perm/edit-allowed-by-write-rule",
  },
  {
    tool: "Write",
    mode: "build",
    input: { file_path: "/ws/a.txt", content: "x" },
    projectRules: { version: 1, deny: [{ toolName: "Write", ruleContent: "/ws/*" }] },
    name: "perm/write-project-deny-wildcard",
  },
  {
    tool: "Write",
    mode: "yolo",
    input: { file_path: "/ws/a.txt", content: "x" },
    projectRules: { version: 1, deny: [{ toolName: "Write" }] },
    name: "perm/write-yolo-deny-priority",
  },
  {
    tool: "Write",
    mode: "build",
    input: { file_path: "/ws/a.txt", content: "x" },
    projectRules: { version: 1, allow: [{ toolName: "Write" }] },
    name: "perm/write-project-allow-no-content",
  },
  {
    tool: "Edit",
    mode: "build",
    input: { file_path: "/ws/sub/a.txt" },
    projectRules: { version: 1, ask: [{ toolName: "Edit", ruleContent: "/ws/*" }] },
    name: "perm/edit-project-ask-wildcard",
  },
  // 尾部锚点敏感：规则 "/ws/a.txt*" 不应匹配 "/ws/a.txtX"。
  {
    tool: "Write",
    mode: "build",
    input: { file_path: "/ws/a.txtX", content: "x" },
    projectRules: { version: 1, allow: [{ toolName: "Write", ruleContent: "/ws/a.txt*" }] },
    name: "perm/write-wildcard-tail-anchor-nomatch",
  },
  {
    tool: "Write",
    mode: "build",
    input: { file_path: "/ws/a.txt.bak", content: "x" },
    projectRules: { version: 1, allow: [{ toolName: "Write", ruleContent: "/ws/a.txt*" }] },
    name: "perm/write-wildcard-prefix-match",
  },
  // 尾部锚点敏感场景：中间通配 + 尾部字面量，缺少 $ 会误匹配后跟字符的主题。
  {
    tool: "Write",
    mode: "build",
    input: { file_path: "/ws/x/a/b", content: "x" },
    projectRules: { version: 1, allow: [{ toolName: "Write", ruleContent: "/ws/*/a" }] },
    name: "perm/write-wildcard-mid-tail-anchor-nomatch",
  },
  {
    tool: "Write",
    mode: "build",
    input: { file_path: "/ws/x/a", content: "x" },
    projectRules: { version: 1, allow: [{ toolName: "Write", ruleContent: "/ws/*/a" }] },
    name: "perm/write-wildcard-mid-tail-anchor-match",
  },
  // "prefix:*" 语义（命令前缀）：只匹配 prefix 或 "prefix " 开头。
  {
    tool: "Write",
    mode: "build",
    input: { command: "git status", file_path: "/ws/a" },
    projectRules: { version: 1, allow: [{ toolName: "Write", ruleContent: "git:*" }] },
    name: "perm/write-colon-star-prefix",
  },
];

const permissionOptionsCases: Array<{
  name: string;
  toolName: string;
  input: unknown;
  optionsPolicy?: string;
}> = [
  {
    name: "opts/write-path-rule",
    toolName: "Write",
    input: { file_path: "/ws/notes.txt", content: "x" },
  },
  {
    name: "opts/edit-path-rule",
    toolName: "Edit",
    input: { file_path: "/ws/notes.txt", old_string: "a", new_string: "b" },
  },
  {
    name: "opts/no-always-allow",
    toolName: "Write",
    input: { file_path: "/ws/x.txt" },
    optionsPolicy: "no-always-allow",
  },
  {
    name: "opts/session-always-allow",
    toolName: "Write",
    input: { file_path: "/ws/x.txt" },
    optionsPolicy: "session-always-allow",
  },
  { name: "opts/no-rule-content", toolName: "Write", input: { content: "x" } },
];

function toolCapability(tool: string): unknown {
  const entry = entriesByTool[tool] as { metadata: AnyRecord; permission?: AnyRecord };
  return { ...entry.metadata, permission: entry.permission };
}

function collectPermission(): AnyRecord[] {
  const out: AnyRecord[] = [];
  for (const c of permissionCases) {
    const service = new PermissionService();
    if (c.sessionGranted) service.grantSessionPermission(c.sessionGranted as never);
    const decision = service.checkPermission(
      {
        toolName: c.tool,
        input: c.input,
        riskLevel: "medium",
        mode: c.mode,
        planEnabled: c.planEnabled,
        workingDirectory: "/ws",
      } as never,
      toolCapability(c.tool) as never,
      (c.projectRules ?? null) as never,
    );
    out.push({
      name: c.name,
      tool: c.tool,
      mode: c.mode,
      input: c.input,
      projectRules: c.projectRules ?? null,
      decision: {
        decision: decision.decision,
        allowed: decision.allowed,
        escalated: decision.escalated,
        reason: decision.reason,
        ruleId: decision.ruleId,
        riskLevel: decision.riskLevel,
        sideEffectScope: decision.sideEffectScope,
      },
    });
  }
  return out;
}

function collectPermissionOptions(): AnyRecord[] {
  return permissionOptionsCases.map((c) => ({
    name: c.name,
    toolName: c.toolName,
    input: c.input,
    optionsPolicy: c.optionsPolicy ?? null,
    options: buildProtocolPermissionOptions({
      toolName: c.toolName,
      input: c.input,
      optionsPolicy: c.optionsPolicy as never,
    }),
  }));
}

// ---------------------------------------------------------------------------
// 场景录制里的工具结果文本（S03 Read / S04 Write+Edit / S05 denial）
// 通过 NEX_REC_BODY_DIR 抓取真实 agent 发出的第二个模型请求里的 tool 消息。
// ---------------------------------------------------------------------------

async function collectScenarioToolResults(): Promise<AnyRecord[]> {
  const out: AnyRecord[] = [];
  const bodyDir = mkdtempSync(join(tmpdir(), "tooltrace-body-"));
  process.env.NEX_REC_BODY_DIR = bodyDir;
  const runDir = mkdtempSync(join(tmpdir(), "tooltrace-out-"));
  const scenarios = [
    { id: "S03", file: "S03-tool-read.json", files: { "hello.txt": "hi there\n" } },
    { id: "S04", file: "S04-write-edit.json", files: {} },
    { id: "S05", file: "S05-permission-denied.json", files: {} },
  ];
  for (const sc of scenarios) {
    rmSync(bodyDir, { recursive: true, force: true });
    mkdirSync(bodyDir, { recursive: true });
    await recordScenario(join(HERE, "record/scenarios", sc.file), join(runDir, sc.id), {
      quiet: true,
    });
    const toolMessages: string[] = [];
    for (const f of readdirSync(bodyDir).sort()) {
      const { body } = JSON.parse(readFileSync(join(bodyDir, f), "utf8"));
      for (const m of body.messages) {
        if (m.role === "tool") {
          const text = typeof m.content === "string" ? m.content : JSON.stringify(m.content);
          // 场景工作区是随机临时目录；换成固定占位，Go 测试用同一个占位比对。
          toolMessages.push(text.replace(/\/tmp\/nex-rec-[A-Za-z0-9-]+\/ws/g, "<WS>"));
        }
      }
    }
    // 记录最后一次出现的每个工具结果；同一工具多次调用时保留全部（按请求顺序）。
    out.push({ id: sc.id, toolMessages, files: sc.files });
  }
  delete process.env.NEX_REC_BODY_DIR;
  return out;
}

async function main(): Promise<void> {
  const outDir = join(REPO, "go/testdata/vectors");
  mkdirSync(outDir, { recursive: true });

  const results: AnyRecord = {};
  results.read = await collect("read", readToolEntry, readCases);
  results.write = await collect("write", writeToolEntry, writeCases);
  results.edit = await collect("edit", editToolEntry, editCases);
  results.readStateKeys = await collectReadOnlyExtras();
  results.matcher = matcherCases.map((c) => ({
    ...c,
    result: findEditMatch({ content: c.content, search: c.search, replaceAll: c.replaceAll }),
  }));
  results.diff = diffCases.map((c) => ({
    old: c.old,
    new: c.newContent,
    hunks: createStructuredPatch({
      filePath: "f.txt",
      oldContent: c.old,
      newContent: c.newContent,
    }),
  }));

  const validation = await collectValidation();
  const permission = collectPermission();
  const permissionOptions = collectPermissionOptions();
  const encodings = await collectEncodings();
  const scenarioResults = await collectScenarioToolResults();

  // 工具元数据（flags / permission / budget）——Go 侧断言相等
  results.metadata = {
    read: readToolEntry.metadata,
    write: writeToolEntry.metadata,
    edit: editToolEntry.metadata,
  };
  results.permissionSpec = {
    read: readToolEntry.permission,
    write: writeToolEntry.permission,
    edit: editToolEntry.permission,
  };
  results.resultBudget = {
    read: readToolEntry.resultBudget,
    write: writeToolEntry.resultBudget,
    edit: editToolEntry.resultBudget,
  };
  results.timeout = {
    read: readToolEntry.timeout,
    write: writeToolEntry.timeout,
    edit: editToolEntry.timeout,
  };

  // readStateKeys 的 key 里含 <WS>，正则化后再写盘。
  results.readStateKeys = (results.readStateKeys as AnyRecord[]).map((k) => k);

  for (const key of ["read", "write", "edit"]) {
    writeFileSync(join(outDir, `tools-${key}.json`), JSON.stringify(results[key], null, 1) + "\n");
  }
  writeFileSync(
    join(outDir, "tools-meta.json"),
    JSON.stringify(
      {
        metadata: results.metadata,
        permissionSpec: results.permissionSpec,
        resultBudget: results.resultBudget,
        timeout: results.timeout,
        readStateKeys: results.readStateKeys,
        matcher: results.matcher,
        diff: results.diff,
      },
      null,
      1,
    ) + "\n",
  );
  writeFileSync(join(outDir, "tools-validation.json"), JSON.stringify(validation, null, 1) + "\n");
  writeFileSync(join(outDir, "tools-encodings.json"), JSON.stringify(encodings, null, 1) + "\n");
  writeFileSync(
    join(outDir, "tools-scenarios.json"),
    JSON.stringify(scenarioResults, null, 1) + "\n",
  );
  writeFileSync(
    join(outDir, "tools-permission.json"),
    JSON.stringify({ decisions: permission, options: permissionOptions }, null, 1) + "\n",
  );

  const counts = {
    read: results.read.cases.length,
    write: results.write.cases.length,
    edit: results.edit.cases.length,
    validation: validation.length,
    permission: permission.length + permissionOptions.length,
  };
  console.log("tool vectors written:", JSON.stringify(counts));
}

await main();
