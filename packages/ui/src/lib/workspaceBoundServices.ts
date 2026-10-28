import type { IServiceAccessor } from "@nex/services";

/**
 * workspace 绑定的服务访问器（docs/next/workspace-tools.md Decision 1）。
 *
 * 多 runner 拓扑下 `file` / `file-watcher` / `media-preview` / `git` / `git-checkpoint` /
 * `terminal` 的 path-bearing 方法只带路径，server 无法区分两台机器上的同一路径。
 * 这里用一个包装层把所属 workspace 的 `{workspacePath, workspaceIdentity}` 注入到参数对象里，
 * server（按身份 → runner、唯一路径 → runner）据此路由，无需在 server 维护“当前激活 tab”状态。
 *
 * 约定：
 * - 只处理下面列出的频道/方法；其他方法与事件一律直接透传到原始 accessor（同一引用）。
 * - 只在字段**缺失**时注入；调用方显式传入（即使为空字符串）时不覆盖——服务层对
 *   “显式清空”保持原语义。
 * - 桌面/local host 的实现忽略这些可选字段，行为不变。
 * - 结果按「原始 accessor + 目标」记忆化：同一 function 组件在多次渲染间拿到同一对象引用，
 *   下游 hook 的依赖数组不会因每次渲染新建包装而失效。
 */

/** 可绑定目标：workspace 绝对路径与（可选的）server 路由身份。 */
export interface WorkspaceServiceTarget {
  workspacePath: string;
  workspaceIdentity?: string | null;
}

/**
 * 每个频道需要注入目标的方法。
 *
 * 当前所有频道都按“参数对象的顶层 `workspacePath`/`workspaceIdentity`”读目标，与服务端
 * `workspaceTarget` 的读取方式一致；因此单条规则即可覆盖，不引入不可达的参数形态分支。
 */
export interface WorkspaceChannelBinding {
  /** IServiceAccessor 上的属性名。 */
  accessor: WorkspaceServiceAccessorKey;
  /** 运行时频道名（@nex/shared ServiceChannels 的值），供测试锁定契约。 */
  channel: string;
  /** 需要注入目标的方法名；其余方法（包括 onDynamic* 事件）透传。 */
  methods: readonly string[];
  /**
   * 无参方法是否也要注入目标。
   *
   * `system.info()` 不带参数，但多 runner 下 server 需要目标才能把「这台机器的
   * homedir」问对地方（docs/next/runner-folders.md 决策 2/4）。桌面/local 实现
   * 忽略多余参数，行为不变。
   */
  injectTargetIntoZeroArgCalls?: boolean;
}

export type WorkspaceServiceAccessorKey =
  | "fileService"
  | "fileWatcherService"
  | "mediaPreviewService"
  | "gitService"
  | "gitCheckpointService"
  | "terminalService"
  | "systemService";

export const WORKSPACE_BOUND_CHANNELS: readonly WorkspaceChannelBinding[] = [
  {
    accessor: "fileService",
    channel: "file",
    methods: [
      "searchWorkspaceFiles",
      "readdir",
      "stat",
      "checkFilesExist",
      "resolvePath",
      "readTextFile",
      "readMediaPreview",
      "readFileRange",
      "readBinaryPreview",
      "listWorkspaceFilesLength",
      "listWorkspaceFilesRange",
      "readWorkspaceFileSearchIgnore",
      "applyWorkspaceFileSearchIgnoreTransform",
      "writeWorkspaceFileSearchIgnore",
    ],
  },
  {
    accessor: "fileWatcherService",
    channel: "file-watcher",
    // onDynamicChange 的事件 id 在 server 侧继承 watch 的绑定，不在这里注入。
    methods: ["watch"],
  },
  {
    accessor: "mediaPreviewService",
    channel: "media-preview",
    methods: ["prepare"],
  },
  {
    accessor: "gitService",
    channel: "git",
    methods: [
      "getRepositorySummary",
      "getWorkspaceRepositoryInfo",
      "getLocalBranches",
      "getCommitGraph",
      "switchBranch",
      "createBranchAndSwitch",
      "getChanges",
      "getIgnoredPaths",
      "getDiff",
      "getBranchComparison",
      "stagePaths",
      "unstagePaths",
      "discardPaths",
      "generateCommitMessage",
      "commit",
      "push",
      "getIdentity",
      "refresh",
    ],
  },
  {
    accessor: "gitCheckpointService",
    channel: "git-checkpoint",
    methods: [
      "createCheckpoint",
      "diffCheckpoints",
      "restoreBetweenCheckpoints",
      "deleteCheckpoint",
    ],
  },
  {
    accessor: "terminalService",
    channel: "terminal",
    // write/resize/dispose 用的是 create 分配的 id，server 按 id 找到所属 runner；
    // onDynamic* 是事件监听，沿同一 id 路由。
    methods: ["create"],
  },
  {
    accessor: "systemService",
    channel: "system",
    // info() 无参：机器级浏览需要一个目标才能问对“哪台机器的 homedir”。
    methods: ["info"],
    injectTargetIntoZeroArgCalls: true,
  },
];

const BINDING_BY_ACCESSOR = new Map<string, WorkspaceChannelBinding>(
  WORKSPACE_BOUND_CHANNELS.map((binding) => [binding.accessor, binding]),
);

/** 规范化的注入值：空白身份视为“未提供”（与服务层 `identity?.trim() || path` 一致）。 */
export function normalizeWorkspaceServiceTarget(target: WorkspaceServiceTarget): {
  workspacePath: string;
  workspaceIdentity?: string;
} {
  const workspacePath = target.workspacePath.trim();
  const workspaceIdentity = target.workspaceIdentity?.trim();
  return workspaceIdentity ? { workspacePath, workspaceIdentity } : { workspacePath };
}

/**
 * 把目标注入到单个调用参数里；只补缺失字段。
 *
 * 返回新对象（不修改调用方参数），保证同一参数对象可被多个调用方复用。
 * 非对象参数原样返回：列出的方法都只接受参数对象，伪造/包裹其他形态会改变契约。
 */
export function injectWorkspaceTargetIntoParams(
  params: unknown,
  target: { workspacePath: string; workspaceIdentity?: string },
): unknown {
  if (!params || typeof params !== "object" || Array.isArray(params)) {
    return params;
  }
  const record = params as Record<string, unknown>;
  const result: Record<string, unknown> = { ...record };
  // 空白字符串等同于“未提供”：server 会 trim 后丢弃，桌面实现也不区分；
  // 按缺失注入可避免调用点传了空 topic 却静默丢掉路由。
  const isMissing = (value: unknown) =>
    value === undefined || (typeof value === "string" && value.trim() === "");
  if (isMissing(result.workspacePath) && target.workspacePath) {
    result.workspacePath = target.workspacePath;
  }
  if (isMissing(result.workspaceIdentity) && target.workspaceIdentity) {
    result.workspaceIdentity = target.workspaceIdentity;
  }
  return result;
}

function wrapBoundMethod(
  method: (...args: unknown[]) => unknown,
  target: { workspacePath: string; workspaceIdentity?: string },
  injectIntoZeroArgCalls: boolean,
): (...args: unknown[]) => unknown {
  return (...args: unknown[]) => {
    if (args.length === 0) {
      // system.info() 无参但有机器语义：只有声明了该行为且**带身份**的绑定才补一个
      // 目标对象。没有身份的 path-only workspace（例如 server 兜底创建的会话目录）
      // 保持原调用形态，让 server 走既有的“第一台 runner”回落，而不是按路径报错。
      if (injectIntoZeroArgCalls && target.workspaceIdentity) {
        return method(injectWorkspaceTargetIntoParams({}, target));
      }
      return method(...args);
    }
    const [first, ...rest] = args;
    return method(injectWorkspaceTargetIntoParams(first, target), ...rest);
  };
}

function createBoundService(
  service: object,
  binding: WorkspaceChannelBinding,
  target: { workspacePath: string; workspaceIdentity?: string },
): object {
  const boundMethods = new Map<string, (...args: unknown[]) => unknown>();
  return new Proxy(service, {
    get(source, property, receiver) {
      const value: unknown = Reflect.get(source, property, receiver);
      if (typeof property !== "string" || typeof value !== "function") {
        return value;
      }
      if (!binding.methods.includes(property)) {
        return value;
      }
      let bound = boundMethods.get(property);
      if (!bound) {
        bound = wrapBoundMethod(
          value as (...args: unknown[]) => unknown,
          target,
          binding.injectTargetIntoZeroArgCalls === true,
        );
        boundMethods.set(property, bound);
      }
      return bound;
    },
  });
}

function createBoundAccessor(
  services: IServiceAccessor,
  target: { workspacePath: string; workspaceIdentity?: string },
): IServiceAccessor {
  const boundServices = new Map<string, object>();
  return new Proxy(services, {
    get(source, property, receiver) {
      const value: unknown = Reflect.get(source, property, receiver);
      if (typeof property !== "string" || !value || typeof value !== "object") {
        return value;
      }
      const binding = BINDING_BY_ACCESSOR.get(property);
      if (!binding) {
        return value;
      }
      let bound = boundServices.get(property);
      if (!bound) {
        bound = createBoundService(value, binding, target);
        boundServices.set(property, bound);
      }
      return bound;
    },
  });
}

const boundAccessorCache = new WeakMap<IServiceAccessor, Map<string, IServiceAccessor>>();

/**
 * 返回把 path-bearing 调用绑定到 `target` 的访问器。
 *
 * - `services` 为 null（例如可选上下文）时返回 null。
 * - 没有有效 workspacePath 且没有 identity 时原样返回 `services`（host-global 调用）。
 * - 同一 accessor + 同一目标返回同一对象（记忆化）。
 */
export function bindServicesToWorkspace(
  services: IServiceAccessor | null | undefined,
  target: WorkspaceServiceTarget | null | undefined,
): IServiceAccessor | null {
  if (!services) {
    return null;
  }
  if (!target) {
    return services;
  }
  const normalized = normalizeWorkspaceServiceTarget(target);
  if (!normalized.workspacePath && !normalized.workspaceIdentity) {
    return services;
  }

  // WeakMap 已按 services 分桶；identity 优先作为分组 key：同一路径的两台机器
  // 必须得到不同的绑定访问器。
  const cacheKey = normalized.workspaceIdentity ?? normalized.workspacePath;
  let perServices = boundAccessorCache.get(services);
  if (!perServices) {
    perServices = new Map();
    boundAccessorCache.set(services, perServices);
  }
  const cached = perServices.get(cacheKey);
  if (cached) {
    return cached;
  }
  const bound = createBoundAccessor(services, normalized);
  perServices.set(cacheKey, bound);
  return bound;
}

/** 测试专用：清掉记忆化缓存（WeakMap 无法枚举，重建模块状态代价高，这里只清当前 accessor）。 */
export function clearWorkspaceBoundServicesCacheForTest(services: IServiceAccessor): void {
  boundAccessorCache.delete(services);
}
