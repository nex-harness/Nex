/**
 * `runner-workspaces` 的 web 传输层（docs/next/runner-folders.md 决策 3）。
 *
 * 为什么单独建一条连接：客户端 SDK（`@nex/client` 的 RemoteServiceAccess）只为固定
 * 服务清单生成 typed getter，没有 `runner-workspaces`；UI 又必须经 IPlatformService
 * 发起平台调用，不能自己拿连接。这里在 web 平台内复用同一个 `/ws` 端点与登录
 * cookie，按「一次调用」建立并释放 ChannelClient：
 * - 服务端 router 按 `workspaceIdentity`（`runner:<id>:~`）把请求转发给那台 runner，
 *   与主连接的路由语义完全一致；
 * - 不缓存连接：add 是用户显式动作，调用结束即释放，断线也不会留下半开 socket；
 * - 握手/转发失败或超时都会 reject，调用方据此提示并留在目录浏览器里重试。
 */
import {
  ChannelClient,
  Emitter,
  ProxyChannel,
  SocketProtocol,
  VSBuffer,
  type IChannel,
  type ISocket,
} from "@nex/rpc";
import type { RunnerWorkspaceAddResult } from "@nex/shared";

/** runner-workspaces 频道上 UI 需要的唯一方法（list/remove 本批次不暴露）。 */
interface RunnerWorkspacesChannel {
  add(params: {
    path: string;
    workspacePath: string;
    workspaceIdentity: string;
  }): Promise<RunnerWorkspaceAddResult>;
}

const RUNNER_WORKSPACES_CHANNEL = "runner-workspaces";
const ADD_TIMEOUT_MS = 15_000;

function wrapBrowserWebSocket(ws: WebSocket): ISocket {
  const onData = new Emitter<VSBuffer>();
  const onClose = new Emitter<void>();
  const onEnd = new Emitter<void>();
  let closed = false;
  const fireClose = () => {
    if (closed) {
      return;
    }
    closed = true;
    onClose.fire();
    onEnd.fire();
  };

  ws.binaryType = "arraybuffer";
  ws.addEventListener("message", (event) => {
    onData.fire(VSBuffer.wrap(new Uint8Array(event.data as ArrayBuffer)));
  });
  ws.addEventListener("close", fireClose);
  ws.addEventListener("error", fireClose);

  return {
    onData: onData.event,
    onClose: onClose.event,
    onEnd: onEnd.event,
    write(buffer: VSBuffer) {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(buffer.buffer as Uint8Array<ArrayBuffer>);
      }
    },
    end() {
      ws.close();
    },
    drain() {
      return Promise.resolve();
    },
    dispose() {
      ws.close();
    },
  };
}

function resolveRunnerWorkspacesWsUrl(): string {
  const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${protocol}//${window.location.host}/ws`;
}

/** 把某个绝对目录加入该 runner 的 served folder 列表。 */
export async function addRunnerWorkspaceOnServer(
  runnerId: string,
  path: string,
  machineIdentity: string,
): Promise<RunnerWorkspaceAddResult> {
  const ws = new WebSocket(resolveRunnerWorkspacesWsUrl());
  const client = new ChannelClient(new SocketProtocol(wrapBrowserWebSocket(ws)));
  const channel = ProxyChannel.toService<RunnerWorkspacesChannel>(
    client.getChannel(RUNNER_WORKSPACES_CHANNEL) as IChannel,
  );

  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    // 机器级目标：server 只按身份路由，runner 侧用 `~` 判定这是“加目录”调用。
    const add = channel.add({ path, workspacePath: "~", workspaceIdentity: machineIdentity });
    const timeout = new Promise<never>((_, reject) => {
      timer = setTimeout(
        () => reject(new Error(`runner workspace add timed out after ${ADD_TIMEOUT_MS}ms`)),
        ADD_TIMEOUT_MS,
      );
    });
    return await Promise.race([add, timeout]);
  } finally {
    if (timer !== undefined) {
      clearTimeout(timer);
    }
    // ChannelClient.dispose 会让挂起的 RPC fail-closed，随后关闭 socket。
    client.dispose();
    ws.close();
  }
}
