import type { ProjectRepositoryContract } from "../../../../services/src/project-storage/contract.js";
import { mkdir, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import {
  MutableAccountProviderConfigSource,
  ProviderConfig,
  ProviderConfigMap,
  ZhipuAccountAccessConfig,
  createAccountProviderConfigSnapshot,
  createFailClosedAccountProviderConfigSnapshot,
  type ModelSelectionViewInput,
} from "@nex/provider";
import { NodeProviderRegistryRuntime, createNodeModelSelectionFacade } from "@nex/provider-node";
import {
  createNexAgentService,
  createNexTaskServiceAdapter,
  getDataBaseDir,
  setDataBaseDir,
} from "@nex/services/node";
import { BUILTIN_MODEL_PROVIDER_IDS } from "@nex/shared";
import { TaskIndexRepo } from "../../../../services/src/session/taskIndexRepo.js";
import { createNexTaskIndexSyncer } from "../../../../services/src/nex-agent/nexTaskIndexSyncer.js";
import { getE2EAppDataPaths } from "./desktop-app.js";

/** 独立测试 Host + 真实 stdio Worker；仅账号上游和模型 HTTP 使用隔离 fixture。 */
export async function createWorkerSelectionIntegration(
  directory = "worker-selection-host",
  options: { workspacePath?: string; cliNodeArgs?: string[] } = {},
) {
  const paths = getE2EAppDataPaths();
  const home = join(paths.homeDir, directory);
  const workspacePath = options.workspacePath ?? join(home, "workspace");
  await mkdir(workspacePath, { recursive: true });
  const previousDataDir = getDataBaseDir();
  setDataBaseDir(home);
  const source = new MutableAccountProviderConfigSource();
  const deliverySource = new MutableAccountProviderConfigSource();
  let deliveryPaused = false;
  const runtimePaths = {
    nexBuiltinFilePath: resolve("../../config/provider/nex-builtin.json"),
    personalFilePath: join(home, ".nex/v2/provider_config.json"),
  };
  // 独立 Host 不继承 Electron 设置。保持内建 HTTPS endpoint，
  // 将显式网络出口接到本轮数据面代理，避免假凭据直连或 HTTP 被官方请求安全校验拒绝。
  const httpProxy = process.env.NEX_HTTP_PROXY?.trim();
  const caCertFile = process.env.NEX_AGENT_CA_CERT?.trim();
  if (!httpProxy || !caCertFile) throw new Error("Worker 选择回归缺少隔离 HTTPS 数据面代理");
  await mkdir(join(home, ".nex/cli"), { recursive: true });
  await writeFile(
    join(home, ".nex/cli/config.json"),
    JSON.stringify({
      network: { httpProxy, caCertFile, noProxy: process.env.NEX_NO_PROXY },
    }),
  );
  const runtime = new NodeProviderRegistryRuntime({
    ...runtimePaths,
    accountSource: source,
    watch: false,
    personalPollingIntervalMs: false,
  });
  await runtime.start();
  const config = await runtime.configService.read();
  const individual = BUILTIN_MODEL_PROVIDER_IDS.bigmodelIndividualCodingPlan;
  const team = BUILTIN_MODEL_PROVIDER_IDS.bigmodelTeamCodingPlan;
  const modelId = "GLM-5.3-Flash";
  async function selectAccount(providerId: string) {
    const base = createFailClosedAccountProviderConfigSnapshot(config);
    const providers = new Map(base.providers.entries());
    for (const id of [individual, team]) {
      providers.set(
        id,
        new ProviderConfig({
          access: new ZhipuAccountAccessConfig({ entitled: true }),
          builtinModelIds: [modelId],
        }),
      );
    }
    source.replace(
      createAccountProviderConfigSnapshot(
        config.nexBuiltinRevision,
        new ProviderConfigMap([...providers]),
        Object.fromEntries(
          [individual, team].map((id) => [
            id,
            {
              availability: "available" as const,
              entitled: true,
              current: id === providerId,
            },
          ]),
        ),
      ),
    );
    await runtime.registryService.refresh("test-account-change");
    if (!deliveryPaused) deliverySource.replace(await source.read());
  }
  await selectAccount(individual);
  const facade = createNodeModelSelectionFacade(runtime.registryService);
  const modelSelectionService = {
    getView: async (input?: ModelSelectionViewInput) => facade.getView(undefined, undefined, input),
    onDidChange: (listener: Parameters<typeof facade.onDidChange>[0]) => ({
      dispose: facade.onDidChange(listener),
    }),
  };
  const repo = new TaskIndexRepo();
  const projects: ProjectRepositoryContract = await repo.getProjectRepository();
  const agent = createNexAgentService({
    // 真实 Host 创建端口必须预留任务归属；旧夹具漏装配，Worker 启动前即报 creation-unavailable。
    prepareSessionCreation: async (target) => {
      const prepared = projects.reserveCreatedTask({
        task: { ...target, taskId: target.sessionId },
        projectId: target.projectId,
        creationCommand: target.creationCommand,
        additionalDirectories: target.additionalDirectories,
      });
      return { sessionId: prepared.taskId, additionalDirectories: prepared.additionalDirectories };
    },
    commandResolver: () => ({
      command: process.execPath,
      args: [
        ...(options.cliNodeArgs ?? []),
        resolve("../../apps/nex-cli/packages/cli/dist/nex.cjs"),
        "app-server",
        "--stdio",
      ],
      env: {
        HOME: home,
        USERPROFILE: home,
        NEX_DATA_BASE_DIR: home,
        NEX_RUNTIME_ENV: "development",
        NEX_BUILTIN_PROVIDER_CONFIG_FILE: runtimePaths.nexBuiltinFilePath,
        NEX_PERSONAL_PROVIDER_CONFIG_FILE: runtimePaths.personalFilePath,
      },
    }),
    modelSelectionReadinessSource: modelSelectionService,
  });
  const syncer = createNexTaskIndexSyncer({ agentService: agent, taskIndexRepo: repo });
  const task = createNexTaskServiceAdapter({
    nexAgentService: agent,
    taskIndexRepo: repo,
    taskIndexSyncer: syncer,
  });
  return {
    home,
    repo,
    projects,
    workspacePath,
    agent,
    task,
    modelSelectionService,
    selectAccount,
    async setDeliveryPaused(paused: boolean) {
      deliveryPaused = paused;
      if (!paused) deliverySource.replace(await source.read());
    },
    async dispose() {
      syncer.disposeAll();
      await agent.disposeWorkspace({ workspacePath });
      await agent.disposeAllAndWait();
      repo.close();
      runtime.dispose();
      setDataBaseDir(previousDataDir);
    },
  };
}
