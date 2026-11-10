import type { SourceServerRunningTask } from "@nex/shared";
import { Emitter, type Event, type IDisposable } from "@nex/rpc";
import type {
  INexAgentService,
  NexAgentRuntimeLifecycleEvent,
  NexAgentWorkspaceTarget,
} from "@nex/services";
import type { ConversationTelemetryFact } from "@nex/shared/nex-protocol-v4";

export interface TaskActivityTracker extends IDisposable {
  readonly onDidChangeRunningTaskCount: Event<number>;
  readRunningTaskCount(): number;
  readRunningTasks(): SourceServerRunningTask[];
}

type AgentActivitySource = Pick<
  INexAgentService,
  "onAgentRuntimeLifecycle" | "onDynamicConversationTelemetryFact"
>;

interface WorkspaceActivity {
  target: NexAgentWorkspaceTarget;
  activeSessionIds: Set<string>;
  runtimeIdentity: string;
  telemetry: IDisposable;
}

function workspaceKey(target: NexAgentWorkspaceTarget): string {
  return target.workspaceIdentity?.trim() || target.workspacePath;
}

export function createTaskActivityTracker(
  source: AgentActivitySource | undefined,
): TaskActivityTracker {
  const changed = new Emitter<number>();
  const workspaces = new Map<string, WorkspaceActivity>();
  let runningTaskCount = 0;
  let disposed = false;

  const publishCount = (): void => {
    const next = [...workspaces.values()].reduce(
      (total, workspace) => total + workspace.activeSessionIds.size,
      0,
    );
    if (next === runningTaskCount) return;
    runningTaskCount = next;
    changed.fire(next);
  };

  const removeWorkspace = (key: string, runtimeIdentity?: string): void => {
    const current = workspaces.get(key);
    if (!current || (runtimeIdentity && current.runtimeIdentity !== runtimeIdentity)) return;
    current.telemetry.dispose();
    workspaces.delete(key);
    publishCount();
  };

  const acceptFact = (key: string, fact: ConversationTelemetryFact): void => {
    const workspace = workspaces.get(key);
    if (!workspace) return;
    if (fact.kind === "turn.started") {
      workspace.activeSessionIds.add(fact.sessionId);
    } else if (fact.kind === "turn.terminal") {
      workspace.activeSessionIds.delete(fact.sessionId);
    } else {
      return;
    }
    publishCount();
  };

  const acceptLifecycle = (event: NexAgentRuntimeLifecycleEvent): void => {
    if (disposed) return;
    const key = event.workspaceKey || workspaceKey(event);
    if (event.state === "unavailable") {
      removeWorkspace(key, event.runtimeIdentity.identity);
      return;
    }
    removeWorkspace(key);
    const activeSessionIds = new Set<string>();
    const telemetry = source?.onDynamicConversationTelemetryFact(event)((fact) => acceptFact(key, fact));
    if (!telemetry) return;
    workspaces.set(key, {
      activeSessionIds,
      target: {
        workspacePath: event.workspacePath,
        ...(event.workspaceIdentity ? { workspaceIdentity: event.workspaceIdentity } : {}),
      },
      runtimeIdentity: event.runtimeIdentity.identity,
      telemetry,
    });
  };

  const lifecycle = source?.onAgentRuntimeLifecycle?.(acceptLifecycle);
  return {
    onDidChangeRunningTaskCount: changed.event,
    readRunningTaskCount: () => runningTaskCount,
    readRunningTasks: () =>
      [...workspaces.values()].flatMap((workspace) =>
        [...workspace.activeSessionIds].map((sessionId) => ({ ...workspace.target, sessionId })),
      ),
    dispose: () => {
      if (disposed) return;
      disposed = true;
      lifecycle?.dispose();
      for (const workspace of workspaces.values()) workspace.telemetry.dispose();
      workspaces.clear();
      runningTaskCount = 0;
      changed.dispose();
    },
  };
}
