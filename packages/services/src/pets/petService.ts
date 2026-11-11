/**
 * 桌面宠物目录服务：扫描 {nexDataRoot}/pets/<id>/pet.json + 精灵图，
 * 按 Codex 兼容格式校验（docs/specs/desktop-pets.md）。
 *
 * 目录解析统一走 paths.ts 的 getNexDataRootDir()（尊重 NEX_DATA_BASE_DIR 与
 * setDataBaseDir 注入的自定义数据根），不复用各 service 自建的 resolveUserHomeDir。
 */
import { open, readdir, readFile, stat } from "node:fs/promises";
import { isAbsolute, join, resolve, sep } from "node:path";
import {
  isGridTilingImage,
  parsePetManifest,
  PET_DEFAULT_FRAME_GRID,
  PET_DEFAULT_SPRITESHEET_FILE_NAME,
  PET_MANIFEST_FILE_NAME,
  type PetListResult,
  type PetLoadError,
  type PetSummary,
} from "@nex/shared";

import { getNexDataRootDir } from "../paths.js";
import type { IPetService } from "./contract.js";

interface PetServiceOptions {
  /** 测试注入点：覆盖 pets 根目录。 */
  petsRootDir?: string;
  /** 桌面端注入：nex-media 授权 + URL 构建（与 mediaPreviewService 同一通路）。 */
  authorizeLocalMediaPreviewPath?: (path: string) => Promise<string>;
  createLocalMediaPreviewUrl?: (path: string) => string;
}

interface CachedScan {
  /** 根目录内容签名（name:mtimeMs 列表），变化才重扫。 */
  signature: string;
  result: PetListResult;
}

const PNG_SIGNATURE = [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a];

function getPetsRootDir(options: PetServiceOptions): string {
  return options.petsRootDir ?? join(getNexDataRootDir(), "pets");
}

function readPngSize(header: Buffer): { width: number; height: number } | null {
  // PNG: 8 字节签名 + IHDR（宽/高各 4 字节大端，偏移 16/20）。
  if (!PNG_SIGNATURE.every((byte, index) => header[index] === byte)) return null;
  return { width: header.readUInt32BE(16), height: header.readUInt32BE(20) };
}

function readWebpSize(header: Buffer): { width: number; height: number } | null {
  // WebP: "RIFF" .... "WEBP" + VP8/VP8L/VP8X 子块。
  if (header.toString("ascii", 0, 4) !== "RIFF" || header.toString("ascii", 8, 12) !== "WEBP") {
    return null;
  }
  const chunkType = header.toString("ascii", 12, 16);
  if (chunkType === "VP8X") {
    // VP8X：宽高为 24 位小端减 1，偏移 24/27。
    return { width: 1 + header.readUIntLE(24, 3), height: 1 + header.readUIntLE(27, 3) };
  }
  if (chunkType === "VP8 ") {
    // 有损：帧头偏移 26 起，宽高各 16 位小端（低 14 位有效）。
    return { width: header.readUInt16LE(26) & 0x3fff, height: header.readUInt16LE(28) & 0x3fff };
  }
  if (chunkType === "VP8L") {
    // 无损：签名 0x2f 后 4 字节打包宽高（各 14 位减 1）。
    if (header[20] !== 0x2f) return null;
    const bits = header.readUInt32LE(21);
    return { width: (bits & 0x3fff) + 1, height: ((bits >> 14) & 0x3fff) + 1 };
  }
  return null;
}

/** 读取图片尺寸：WebP（VP8/VP8L/VP8X）与 PNG 头解析，不引入解码依赖。 */
async function readImageSize(filePath: string): Promise<{ width: number; height: number } | null> {
  const handle = await open(filePath, "r");
  try {
    const header = Buffer.alloc(30);
    const { bytesRead } = await handle.read(header, 0, header.length, 0);
    if (bytesRead < 30) return null;
    return readPngSize(header) ?? readWebpSize(header);
  } catch {
    return null;
  } finally {
    await handle.close();
  }
}

async function scanOnePetDir(rootDir: string, dirName: string): Promise<PetSummary | PetLoadError> {
  const dirPath = join(rootDir, dirName);
  const manifestPath = join(dirPath, PET_MANIFEST_FILE_NAME);

  let manifestText: string;
  try {
    manifestText = await readFile(manifestPath, "utf-8");
  } catch {
    return { dirName, reason: `缺少 ${PET_MANIFEST_FILE_NAME}` };
  }

  const parsed = parsePetManifest(manifestText, dirName);
  if (!parsed.ok) {
    return { dirName, reason: parsed.reason };
  }
  const manifest = parsed.manifest;
  const spritesheetFileName = manifest.spritesheetPath ?? PET_DEFAULT_SPRITESHEET_FILE_NAME;

  // 防御：parsePetManifest 已拒绝绝对路径与 ..，这里再校验最终路径仍在目录内。
  const spritesheetPath = resolve(dirPath, spritesheetFileName);
  if (isAbsolute(spritesheetFileName) || !spritesheetPath.startsWith(resolve(dirPath) + sep)) {
    return { dirName, reason: "spritesheetPath 越界" };
  }

  let size: { width: number; height: number } | null;
  try {
    const fileStat = await stat(spritesheetPath);
    if (!fileStat.isFile()) {
      return { dirName, reason: "spritesheet 不是文件" };
    }
    size = await readImageSize(spritesheetPath);
  } catch {
    return { dirName, reason: `精灵图缺失: ${spritesheetFileName}` };
  }
  if (!size) {
    return { dirName, reason: "精灵图不是可识别的 PNG/WebP" };
  }

  const grid = manifest.frame ?? PET_DEFAULT_FRAME_GRID;
  if (!isGridTilingImage(grid, size.width, size.height)) {
    return {
      dirName,
      reason: `网格 ${grid.width}×${grid.height} × ${grid.columns}×${grid.rows} 未铺满图片 ${size.width}×${size.height}`,
    };
  }

  return {
    id: manifest.id ?? dirName,
    displayName: manifest.displayName?.trim() || manifest.id || dirName,
    description: manifest.description ?? "",
    dirPath,
    spritesheetFileName,
    spriteRows: Math.floor(size.height / grid.height),
    manifest,
  };
}

function isLoadError(entry: PetSummary | PetLoadError): entry is PetLoadError {
  return "reason" in entry;
}

async function scanRootDir(rootDir: string): Promise<{ signature: string; dirNames: string[] }> {
  let entries;
  try {
    entries = await readdir(rootDir, { withFileTypes: true });
  } catch {
    // 目录不存在/不可读：视为空（非错误）。
    return { signature: "", dirNames: [] };
  }
  const dirNames: string[] = [];
  const signatureParts: string[] = [];
  for (const entry of entries) {
    if (!entry.isDirectory()) continue;
    dirNames.push(entry.name);
    try {
      const entryStat = await stat(join(rootDir, entry.name));
      signatureParts.push(`${entry.name}:${entryStat.mtimeMs}`);
    } catch {
      signatureParts.push(`${entry.name}:?`);
    }
  }
  dirNames.sort();
  return { signature: signatureParts.sort().join("|"), dirNames };
}

export function createPetService(options: PetServiceOptions = {}): IPetService {
  const rootDir = getPetsRootDir(options);
  let cache: CachedScan | null = null;

  async function scan(): Promise<PetListResult> {
    const { signature, dirNames } = await scanRootDir(rootDir);
    if (cache && cache.signature === signature) {
      // 目录 mtime 不感知深层文件变化；manifest/精灵图级别的改动由
      // refreshPets() 显式清缓存覆盖（设置页「刷新」按钮）。
      return cache.result;
    }
    const pets: PetSummary[] = [];
    const errors: PetLoadError[] = [];
    for (const dirName of dirNames) {
      const entry = await scanOnePetDir(rootDir, dirName);
      if (isLoadError(entry)) {
        errors.push(entry);
      } else {
        pets.push(entry);
      }
    }
    const result: PetListResult = { pets, errors };
    cache = { signature, result };
    return result;
  }

  async function resolveSpritesheetPath(petId: string): Promise<{ path: string } | null> {
    const { pets } = await scan();
    const pet = pets.find((candidate) => candidate.id === petId);
    if (!pet) return null;
    const spritesheetPath = resolve(pet.dirPath, pet.spritesheetFileName);
    if (!spritesheetPath.startsWith(resolve(rootDir) + sep)) return null;
    return { path: spritesheetPath };
  }

  return {
    listPets: () => scan(),
    refreshPets: async () => {
      cache = null;
      return scan();
    },
    getPetSpritesheetPath: ({ petId }) => resolveSpritesheetPath(petId),
    getPetSpritesheetUrl: async ({ petId }) => {
      // 未注入授权通路的宿主（web server）返回 null，由调用方回落静态路由。
      if (!options.authorizeLocalMediaPreviewPath || !options.createLocalMediaPreviewUrl) {
        return null;
      }
      const result = await resolveSpritesheetPath(petId);
      if (!result) return null;
      const canonicalPath = await options.authorizeLocalMediaPreviewPath(result.path);
      return { url: options.createLocalMediaPreviewUrl(canonicalPath) };
    },
  };
}
