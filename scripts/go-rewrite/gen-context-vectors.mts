/* eslint-disable max-lines -- 向量生成脚本：一条脚本驱动一组真实 TS 类并顺序产出全部向量，拆开会让用例与其期望值分居两个文件。 */
// 用真实的 ContextBuilder（apps/nex-cli/packages/core）生成 Go 侧 system prompt 对照向量。
// 输入覆盖：有/无 git、有/无 skills 与 Skill 工具、memory、AGENTS.md、output style、桌面/终端 surface、自定义 system prompt。
// 输出是 OpenAI-compatible 序列化后的合并 system 文本 + meta-user 文本 + 每个 section 的 (source, chars, tokens)。
import { writeFileSync } from "node:fs";
import { createContextBuilder } from "../../apps/nex-cli/packages/core/src/context/builder.ts";
import { normalizeOpenAiCompatibleSystemMessages } from "../../apps/nex-cli/packages/adapters/src/model/system-message-compat.ts";

const env = (over: object = {}) => ({
  cwd: "/work/proj",
  platform: "linux",
  shell: "zsh",
  osVersion: "linux 6.8.0 x64",
  nodeVersion: "v24.0.0",
  isGitRepository: false,
  gitStatus: "not_repo",
  ...over,
});
const model = { providerId: "mock", modelId: "mock-model" };
const sk = (name: string, description: string, extra: object = {}) => ({
  name,
  description,
  path: `/s/${name}/SKILL.md`,
  directory: `/s/${name}`,
  rootPath: "/s",
  scope: "user",
  source: "user",
  safeToAutoLoad: true,
  frontmatterKeys: [],
  ...extra,
});
const skills = { skills: [sk("review", "Review a change")], errors: [], diagnostics: [] };
const manySkills = {
  skills: [
    sk("zeta", "z".repeat(300), { whenToUse: "when z" }),
    sk("alpha", "Alpha skill", { whenToUse: "when a" }),
    sk("plug-x", "Plugin skill", { qualifiedName: "my-plugin:plug-x", pluginName: "my-plugin" }),
    sk("same", "Same", { qualifiedName: "same" }),
  ],
  errors: [],
  diagnostics: [],
};
const bigSkills = {
  skills: Array.from({ length: 120 }, (_, i) =>
    sk(`skill-${String(i).padStart(3, "0")}`, "d".repeat(240)),
  ),
  errors: [],
  diagnostics: [],
};
const instr = (scope: string, content: string) => ({
  filePath: `/work/${scope}/AGENTS.md`,
  fileName: "AGENTS.md",
  content,
  bytesRead: content.length,
  sizeBytes: content.length,
  truncated: false,
  scope,
});

const cases: Array<{ name: string; config: any }> = [
  {
    name: "minimal",
    config: {
      workingDirectory: "/work/proj",
      envInfo: env(),
      model,
      currentDate: "2026-10-09",
      guidanceToolNames: ["Read", "Write"],
    },
  },
  { name: "no-date", config: { workingDirectory: "/w", envInfo: env(), model } },
  {
    name: "no-model",
    config: { workingDirectory: "/w", envInfo: env(), currentDate: "2026-01-02" },
  },
  {
    name: "git-clean",
    config: {
      workingDirectory: "/w",
      envInfo: env({
        isGitRepository: true,
        gitStatus: "clean",
        gitBranch: "main",
        gitMainBranch: "main",
        gitUser: "dev",
        recentCommits: ["abc1234 first", "def5678 second"],
      }),
      model,
      currentDate: "2026-10-09",
    },
  },
  {
    name: "git-dirty-lines",
    config: {
      workingDirectory: "/w",
      envInfo: env({
        isGitRepository: true,
        gitStatus: "dirty",
        gitBranch: "feat/x",
        gitStatusLines: [" M a.go", "?? b.txt"],
        recentCommits: [],
      }),
      model,
      currentDate: "2026-10-09",
    },
  },
  {
    name: "git-unknown-status",
    config: {
      workingDirectory: "/w",
      envInfo: env({ isGitRepository: true, gitBranch: "dev" }),
      model,
    },
  },
  {
    name: "skills-with-tool",
    config: {
      workingDirectory: "/w",
      envInfo: env(),
      model,
      currentDate: "2026-10-09",
      skills,
      guidanceToolNames: ["Skill", "Read"],
    },
  },
  {
    name: "skills-tool-missing",
    config: {
      workingDirectory: "/w",
      envInfo: env(),
      model,
      currentDate: "2026-10-09",
      skills,
      guidanceToolNames: ["Read"],
    },
  },
  {
    name: "skills-sorted-aliases-trimmed",
    config: {
      workingDirectory: "/w",
      envInfo: env(),
      model,
      currentDate: "2026-10-09",
      skills: manySkills,
      guidanceToolNames: ["Skill"],
    },
  },
  {
    name: "skills-over-budget-names-only",
    config: {
      workingDirectory: "/w",
      envInfo: env(),
      model,
      currentDate: "2026-10-09",
      skills: bigSkills,
      guidanceToolNames: ["Skill"],
    },
  },
  {
    name: "skills-custom-budget",
    config: {
      workingDirectory: "/w",
      envInfo: env(),
      model,
      currentDate: "2026-10-09",
      skills: manySkills,
      skillMetadataBudget: 100,
      guidanceToolNames: ["Skill"],
    },
  },
  {
    name: "skills-no-tool-list",
    config: { workingDirectory: "/w", envInfo: env(), model, currentDate: "2026-10-09", skills },
  },
  {
    name: "memory-frontmatter-stripped",
    config: {
      workingDirectory: "/w",
      envInfo: env(),
      model,
      currentDate: "2026-10-09",
      memoryRoot: "/mem",
      memoryIndexContent: "---\nname: idx\n---\n- [a](a.md) — one\n",
    },
  },
  {
    name: "memory-only-whitespace",
    config: {
      workingDirectory: "/w",
      envInfo: env(),
      model,
      currentDate: "2026-10-09",
      memoryRoot: "/mem",
      memoryIndexContent: "  \n\n ",
    },
  },
  {
    name: "memory-line-truncated",
    config: {
      workingDirectory: "/w",
      envInfo: env(),
      model,
      currentDate: "2026-10-09",
      memoryRoot: "/mem",
      memoryIndexContent: Array.from(
        { length: 230 },
        (_, i) => `- [m${i}](m${i}.md) — hook ${i}`,
      ).join("\n"),
    },
  },
  {
    name: "memory-char-truncated-tie-rounding",
    config: {
      workingDirectory: "/w",
      envInfo: env(),
      model,
      currentDate: "2026-10-09",
      memoryRoot: "/mem",
      memoryIndexContent: "x".repeat(25856),
    },
  },
  {
    name: "memory-both-truncated",
    config: {
      workingDirectory: "/w",
      envInfo: env(),
      model,
      currentDate: "2026-10-09",
      memoryRoot: "/mem",
      memoryIndexContent: Array.from(
        { length: 400 },
        (_, i) => `- [m${i}](m${i}.md) — ${"h".repeat(90)}`,
      ).join("\n"),
    },
  },
  {
    name: "memory-no-newline-cut",
    config: {
      workingDirectory: "/w",
      envInfo: env(),
      model,
      currentDate: "2026-10-09",
      memoryRoot: "/mem",
      memoryIndexContent: "y".repeat(40000),
    },
  },
  // 排序：ICU en-US 与字节序不同（大小写交错、标点、重音、CJK、数字）。
  {
    name: "skills-collation-order",
    config: {
      workingDirectory: "/w",
      envInfo: env(),
      model,
      currentDate: "2026-10-09",
      skills: {
        skills: [
          "b",
          "B",
          "a",
          "A",
          "a-b",
          "a_b",
          "ab",
          "a1",
          "a10",
          "a2",
          "Éa",
          "ea",
          "eb",
          "x y",
          "x.y",
          "zed",
          "Zed",
          "中文",
          "abc",
          "aBc",
          "ABC",
        ].map((n) => sk(n, "d")),
        errors: [],
        diagnostics: [],
      },
      guidanceToolNames: ["Skill"],
    },
  },
  // 预算：中文每字 1 个 UTF-16 单元但 3 个 UTF-8 字节；预算恰在两者之间时，按字节算会走另一条分支。
  {
    name: "skills-budget-utf16-vs-bytes",
    config: {
      workingDirectory: "/w",
      envInfo: env(),
      model,
      currentDate: "2026-10-09",
      skillMetadataBudget: 190,
      skills: { skills: [sk("cjk", "说明".repeat(30))], errors: [], diagnostics: [] },
      guidanceToolNames: ["Skill"],
    },
  },
  // 描述 100 个汉字：UTF-16 长度 100（不截断），UTF-8 字节 300（按字节判断会误截断）。
  {
    name: "skills-description-cjk-not-cut",
    config: {
      workingDirectory: "/w",
      envInfo: env(),
      model,
      currentDate: "2026-10-09",
      skills: { skills: [sk("cjk100", "字".repeat(100))], errors: [], diagnostics: [] },
      guidanceToolNames: ["Skill"],
    },
  },
  {
    name: "skills-description-cjk-cut",
    config: {
      workingDirectory: "/w",
      envInfo: env(),
      model,
      currentDate: "2026-10-09",
      skills: { skills: [sk("cjk300", "字".repeat(300))], errors: [], diagnostics: [] },
      guidanceToolNames: ["Skill"],
    },
  },
  {
    name: "skills-description-cut-astral",
    config: {
      workingDirectory: "/w",
      envInfo: env(),
      model,
      currentDate: "2026-10-09",
      skills: { skills: [sk("emoji", "ab" + "😀".repeat(200))], errors: [], diagnostics: [] },
      guidanceToolNames: ["Skill"],
    },
  },
  // trim：U+FEFF（BOM）与 U+00A0 被 JS trim 视为空白，Go 的 TrimSpace 不是同一集合。
  {
    name: "agents-md-bom-and-nbsp",
    config: {
      workingDirectory: "/w",
      envInfo: env(),
      model,
      currentDate: "2026-10-09",
      userInstructions: instr("workspace", "\uFEFF\u00A0\u3000rules\u00A0\uFEFF"),
    },
  },
  {
    name: "agents-md-only-bom",
    config: {
      workingDirectory: "/w",
      envInfo: env(),
      model,
      currentDate: "2026-10-09",
      userInstructions: instr("workspace", "\uFEFF\u00A0"),
    },
  },
  {
    name: "output-style-nbsp-prompt",
    config: {
      workingDirectory: "/w",
      envInfo: env(),
      model,
      currentDate: "2026-10-09",
      outputStyle: { name: "S", prompt: "\u00A0\uFEFF" },
    },
  },
  {
    name: "custom-prompt-bom",
    config: {
      workingDirectory: "/w",
      envInfo: env(),
      model,
      currentDate: "2026-10-09",
      customSystemPrompt: "\uFEFF hi \u00A0",
    },
  },
  {
    name: "memory-index-bom-edges",
    config: {
      workingDirectory: "/w",
      envInfo: env(),
      model,
      currentDate: "2026-10-09",
      memoryRoot: "/mem",
      memoryIndexContent: "\uFEFF- [a](a.md) — x\u00A0",
    },
  },
  {
    name: "agents-md-workspace",
    config: {
      workingDirectory: "/w",
      envInfo: env(),
      model,
      currentDate: "2026-10-09",
      userInstructions: { ...instr("workspace", "Use tabs.\nNo emojis.") },
    },
  },
  {
    name: "agents-md-user-and-workspace",
    config: {
      workingDirectory: "/w",
      envInfo: env(),
      model,
      currentDate: "2026-10-09",
      userInstructions: {
        ...instr("workspace", "WS rules"),
        sources: [
          {
            scope: "user",
            filePath: "/etc/example-user/AGENTS.md",
            fileName: "AGENTS.md",
            content: "user rules",
            bytesRead: 10,
            sizeBytes: 10,
            truncated: false,
          },
          {
            scope: "workspace",
            filePath: "/w/AGENTS.md",
            fileName: "AGENTS.md",
            content: "ws rules",
            bytesRead: 8,
            sizeBytes: 8,
            truncated: true,
          },
        ],
      },
    },
  },
  {
    name: "agents-md-blank",
    config: {
      workingDirectory: "/w",
      envInfo: env(),
      model,
      currentDate: "2026-10-09",
      userInstructions: instr("workspace", "   \n  "),
    },
  },
  {
    name: "memory-index",
    config: {
      workingDirectory: "/w",
      envInfo: env(),
      model,
      currentDate: "2026-10-09",
      memoryRoot: "/mem/root",
      memoryIndexContent: "- [a](a.md) — note\n- [b](b.md) — other\n",
    },
  },
  {
    name: "memory-no-index",
    config: {
      workingDirectory: "/w",
      envInfo: env(),
      model,
      currentDate: "2026-10-09",
      memoryRoot: "/mem/root",
    },
  },
  {
    name: "output-style",
    config: {
      workingDirectory: "/w",
      envInfo: env(),
      model,
      currentDate: "2026-10-09",
      outputStyle: { name: "Terse", prompt: "Answer in one line." },
    },
  },
  {
    name: "output-style-blank",
    config: {
      workingDirectory: "/w",
      envInfo: env(),
      model,
      currentDate: "2026-10-09",
      outputStyle: { name: "Empty", prompt: "   " },
    },
  },
  {
    name: "desktop-surface",
    config: {
      workingDirectory: "/w",
      envInfo: env(),
      model,
      currentDate: "2026-10-09",
      presentationSurface: "nex_desktop",
    },
  },
  {
    name: "terminal-surface",
    config: {
      workingDirectory: "/w",
      envInfo: env(),
      model,
      currentDate: "2026-10-09",
      presentationSurface: "terminal",
    },
  },
  {
    name: "custom-system-prompt",
    config: {
      workingDirectory: "/w",
      envInfo: env(),
      model,
      currentDate: "2026-10-09",
      customSystemPrompt: "You are a poet.",
    },
  },
  {
    name: "windows-env",
    config: {
      workingDirectory: "C:\\w",
      envInfo: env({
        cwd: "C:\\w",
        platform: "win32",
        shell: "powershell.exe",
        osVersion: "win32 10.0.22631 x64",
      }),
      model,
      currentDate: "2026-10-09",
    },
  },
  {
    name: "unicode",
    config: {
      workingDirectory: "/w",
      envInfo: env({ cwd: "/工作/项目 ü" }),
      model,
      currentDate: "2026-10-09",
      userInstructions: instr("workspace", "用中文回答。\u2028行分隔符 😀"),
    },
  },
];

const out = cases.map(({ name, config }) => {
  const built = createContextBuilder(config).build();
  const wire = normalizeOpenAiCompatibleSystemMessages(built.systemMessages);
  return {
    name,
    config,
    systemMessageCount: built.systemMessages.length,
    mergedSystem: wire[0]?.role === "system" ? (wire[0].content as string) : null,
    metaUser: built.metaUserAttachments,
    sections: built.sections.map((s) => ({
      name: s.name,
      source: s.source,
      injectionTarget: s.injectionTarget,
      cacheHint: s.cacheHint,
      chars: s.chars,
      tokens: s.tokens,
    })),
    totalChars: built.totalChars,
    totalTokens: built.totalTokens,
  };
});
writeFileSync(
  new URL("../../go/testdata/vectors/context.json", import.meta.url),
  JSON.stringify(out, null, 1) + "\n",
);
console.log(
  `wrote ${out.length} context cases; sizes: ${out.map((o) => o.mergedSystem?.length).join(",")}`,
);
