// 把 ContextBuilder 里的大段静态提示词，用**真实函数**导出成 Go 里 //go:embed 的文本文件，避免手工抄写出错。
// 组合逻辑（顺序、拼接、截断）在 Go 里重写，并由 gen-context-vectors.mts 的 30+ 个用例对照验证。
import { mkdirSync, writeFileSync } from "node:fs";
import { buildCliPrefixSection } from "../../apps/nex-cli/packages/core/src/context/sections/cli-prefix.ts";
import { buildIdentitySection } from "../../apps/nex-cli/packages/core/src/context/sections/identity.ts";
import { buildDesktopContextSection } from "../../apps/nex-cli/packages/core/src/context/sections/desktop.ts";
import { buildMemorySection } from "../../apps/nex-cli/packages/core/src/context/sections/memory.ts";
import {
  buildContextManagementSection,
  buildDynamicBehaviorSection,
  buildSessionGuidanceSection,
} from "../../apps/nex-cli/packages/core/src/context/dynamic-sections.ts";

const out = new URL("../../go/internal/promptctx/prompts/", import.meta.url);
mkdirSync(out, { recursive: true });
const SENTINEL = "\u0000MEMORY_ROOT\u0000";
const files: Record<string, string> = {
  "cli_prefix.txt": buildCliPrefixSection().content,
  "identity_default.txt": buildIdentitySection().content,
  // output style 只改 intro 一行；用真实函数导出整段，Go 侧直接选用。
  "identity_output_style.txt": buildIdentitySection({ name: "x", prompt: "y" }).content,
  "desktop.txt": buildDesktopContextSection().content,
  "dynamic_behavior.txt": buildDynamicBehaviorSection().content,
  "context_management.txt": buildContextManagementSection().content,
  "session_guidance_skill.txt": buildSessionGuidanceSection(["Skill"], true)!.content,
  "memory.tmpl": buildMemorySection(SENTINEL)!.content,
};
for (const [name, content] of Object.entries(files)) writeFileSync(new URL(name, out), content);
const memoryParts = files["memory.tmpl"].split(SENTINEL).length - 1;
console.log(
  `exported ${Object.keys(files).length} prompt files; memory root occurrences: ${memoryParts}`,
);
