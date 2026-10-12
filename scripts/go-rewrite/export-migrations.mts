// 把 TypeScript 里冻结的迁移 SQL 原样导出成 Go 里 //go:embed 的 .sql 文件，并写出 manifest。
// 校验和是对 `sql.trim()` 的 sha256，Node 把它写进 schema_migration 账本；Go 必须逐字节相同才能共享数据库。
import { createHash } from "node:crypto";
import { mkdirSync, writeFileSync, rmSync } from "node:fs";
import { SQLITE_MIGRATIONS } from "../../apps/nex-cli/packages/adapters/src/storage/session-store/migrations.ts";

const out = new URL("../../go/internal/adapters/store/migrations/", import.meta.url);
rmSync(out, { recursive: true, force: true });
mkdirSync(out, { recursive: true });
const manifest = SQLITE_MIGRATIONS.map((m) => {
  writeFileSync(new URL(`${m.id}.sql`, out), m.sql);
  return {
    id: m.id,
    appVersion: m.appVersion,
    checksum: createHash("sha256").update(m.sql.trim()).digest("hex"),
    bytes: Buffer.byteLength(m.sql),
  };
});
writeFileSync(new URL("manifest.json", out), JSON.stringify(manifest, null, 1) + "\n");
console.log(`exported ${manifest.length} migrations`);
