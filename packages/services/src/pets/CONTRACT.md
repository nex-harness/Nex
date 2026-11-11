# pets 模块契约

桌面宠物（Codex 兼容格式）的目录扫描与校验服务。

- **唯一公开入口**：`contract.ts`（`IPetService` 接口 + RPC 描述符）。
- **数据类型**（`PetSummary` / `PetLoadError` / `PetListResult` / `PetWindowState` …）的唯一事实源是 `@nex/shared` 的 `pets/`，本模块只消费。
- **目录**：`{getNexDataRootDir()}/pets/<id>/{pet.json, spritesheet.webp}`；不读 `~/.codex`。
- **所有者**：本模块是宠物目录的唯一读写者；UI 经 RPC 访问，不直接碰文件系统。

详见 `docs/specs/desktop-pets.md`。

## 示例

```ts
import { IPetService } from "./pets/contract.js";
import { createPetService } from "./pets/petService.js";

// host / server 装配
registry.register(IPetService, createPetService());

// 消费方
const { pets, errors } = await petService.listPets();
```
