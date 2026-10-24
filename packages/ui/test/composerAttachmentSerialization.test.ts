import assert from "node:assert/strict";
import test from "node:test";
import { PROTOCOL_V4_LIMITS } from "@nex/shared/nex-protocol-v4";
import {
  OversizedInlineFileAttachmentError,
  serializeChatComposerAttachment,
  type ChatComposerAttachment,
} from "../src/lib/chatAttachments.js";
import { uploadComposerAttachment } from "../src/v4/composer/attachmentUpload.js";

/**
 * Web 二进制附件分流：无 localPath 的非文本文件（.zip/.xlsx/.docx/.bin）必须以 base64
 * 进入 V4 upload 事务，而不是像修复前那样序列化成纯元信息、在上传分派处被丢弃。
 */

// Node 24 有 File 但没有 FileReader；补一个只覆盖 readAsDataURL 的最小实现，
// 让测试跑在 renderer 同一条序列化路径上（不另封装生产代码的读取方式）。
if (typeof globalThis.FileReader === "undefined") {
  class TestFileReader extends EventTarget {
    result: string | ArrayBuffer | null = null;
    error: Error | null = null;
    onload: (() => void) | null = null;
    onerror: (() => void) | null = null;
    readAsDataURL(file: File) {
      void file.arrayBuffer().then((buffer) => {
        const bytes = Buffer.from(buffer);
        this.result = `data:${file.type};base64,${bytes.toString("base64")}`;
        this.onload?.();
        this.dispatchEvent(new Event("load"));
      });
    }
  }
  (globalThis as { FileReader?: unknown }).FileReader = TestFileReader;
}

function makeAttachment(overrides: Partial<ChatComposerAttachment> & { bytes?: Uint8Array }) {
  const bytes = overrides.bytes ?? new Uint8Array([0x50, 0x4b, 0x03, 0x04, 0x00, 0xff]);
  const file = new File([bytes], overrides.filename ?? "sample.zip", {
    type: overrides.mimeType ?? "application/zip",
  });
  const attachment: ChatComposerAttachment = {
    id: "attachment-1",
    filename: overrides.filename ?? "sample.zip",
    mimeType: overrides.mimeType ?? "application/zip",
    sizeBytes: overrides.sizeBytes ?? bytes.byteLength,
    file,
    ...(overrides.localPath ? { localPath: overrides.localPath } : {}),
  };
  return { attachment, bytes };
}

test("web binary file uploads as base64 through the attachment put", async () => {
  const { attachment, bytes } = makeAttachment({
    filename: "report.xlsx",
    mimeType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
  });
  const serialized = await serializeChatComposerAttachment(attachment);
  assert.equal(serialized.kind, "file");
  assert.equal("localPath" in serialized, false);
  assert.equal("textContent" in serialized, false);
  if (!("dataBase64" in serialized)) assert.fail("expected dataBase64");
  assert.deepEqual(Buffer.from(serialized.dataBase64 as string, "base64"), Buffer.from(bytes));

  const puts: Array<Record<string, unknown>> = [];
  const ref = await uploadComposerAttachment(
    async (params) => {
      puts.push({ ...params });
      return { ref: "nex-runner://session/upload/report.xlsx" };
    },
    "session-1",
    serialized,
  );
  assert.equal(puts.length, 1);
  assert.equal(puts[0]?.fileName, "report.xlsx");
  assert.equal(puts[0]?.mime, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet");
  assert.deepEqual(Buffer.from(String(puts[0]?.dataBase64), "base64"), Buffer.from(bytes));
  assert.deepEqual(ref, {
    ref: "nex-runner://session/upload/report.xlsx",
    fileName: "report.xlsx",
    mime: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
    bytes: bytes.byteLength,
  });
});

test("generic application/octet-stream file also uploads as base64", async () => {
  const { attachment } = makeAttachment({
    filename: "firmware.bin",
    mimeType: "application/octet-stream",
  });
  const serialized = await serializeChatComposerAttachment(attachment);
  assert.equal("dataBase64" in serialized && serialized.dataBase64.length > 0, true);
});

test("web text file stays inline textContent and is never uploaded as bytes", async () => {
  const { attachment } = makeAttachment({
    filename: "notes.md",
    mimeType: "text/markdown",
    bytes: new TextEncoder().encode("# hello\n"),
  });
  const serialized = await serializeChatComposerAttachment(attachment);
  assert.equal(serialized.kind, "file");
  assert.equal("textContent" in serialized ? serialized.textContent : undefined, "# hello\n");
  assert.equal("dataBase64" in serialized, false);

  const puts: unknown[] = [];
  const ref = await uploadComposerAttachment(
    async (params) => {
      puts.push(params);
      return { ref: "nex-runner://session/upload/notes.md" };
    },
    "session-1",
    serialized,
  );
  assert.equal(puts.length, 1);
  assert.equal(ref?.bytes, Buffer.byteLength("# hello\n"));
});

test("web binary file over 20 MiB fails with the oversized attachment error", async () => {
  // 0 字节正文 + 声明的真实大小：序列化在读取/编码前按 sizeBytes 拦截。
  const { attachment } = makeAttachment({
    filename: "huge.zip",
    sizeBytes: PROTOCOL_V4_LIMITS.attachmentMaxBytes + 1,
    bytes: new Uint8Array(0),
  });
  await assert.rejects(serializeChatComposerAttachment(attachment), (error: unknown) => {
    assert.ok(error instanceof OversizedInlineFileAttachmentError);
    assert.equal(error.filename, "huge.zip");
    assert.equal(error.maxSizeBytes, PROTOCOL_V4_LIMITS.attachmentMaxBytes);
    return true;
  });
});

test("desktop file with a local path stays a zero-copy path reference", async () => {
  const { attachment } = makeAttachment({
    filename: "archive.zip",
    localPath: "/Users/me/archive.zip",
    // 超过 20 MiB 也仍然只发路径：桌面零拷贝由 agent 侧按路径读取。
    sizeBytes: PROTOCOL_V4_LIMITS.attachmentMaxBytes * 2,
    bytes: new Uint8Array(0),
  });
  const serialized = await serializeChatComposerAttachment(attachment);
  assert.equal(serialized.kind, "file");
  assert.equal(
    "localPath" in serialized ? serialized.localPath : undefined,
    "/Users/me/archive.zip",
  );
  assert.equal("dataBase64" in serialized, false);

  const puts: unknown[] = [];
  const ref = await uploadComposerAttachment(
    async (params) => {
      puts.push(params);
      return { ref: "unused" };
    },
    "session-1",
    serialized,
  );
  assert.equal(puts.length, 0, "localPath refs must not upload bytes");
  assert.equal(ref?.ref, "/Users/me/archive.zip");
});
