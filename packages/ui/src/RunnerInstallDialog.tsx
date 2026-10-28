/**
 * SSH 安装 runner 向导（docs/next/runner-install.md）。
 *
 * SSH 只作为安装渠道：填一次 SSH 凭据 → server 拨 SSH 在目标机装好并启动
 * runner → runner 之后直连 server。凭据只驻安装会话内存，不落盘。
 *
 * 认证方式参照桌面版 SSHDialog：密码 / 粘贴私钥内容（web 主路径——浏览器里的
 * key 文件不在 server 上，server 只能收内容）/ server 侧私钥路径；私钥可带口令。
 *
 * 两步：表单（host/port/user/认证/name）→ 进度（轮询 install 会话的日志环形
 * 缓冲）。安装成功后不关闭，进入「模型」步骤（docs/next/model-onboarding.md）。
 */
import { useCallback, useEffect, useRef, useState } from "react";
import type { SSHInstallStatus } from "@nex/shared";
import { Cloud } from "lucide-react";
import { Button } from "@/components/ui/button.js";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog.js";
import { Input } from "@/components/ui/input.js";
import { Label } from "@/components/ui/label.js";
import { RunnerInstallModelsStep } from "@/RunnerInstallModelsStep.js";
import { useNexIntl } from "@/i18n/IntlProvider.js";
import { usePlatform } from "@/hooks/usePlatform.js";
import { logger } from "@/logger.js";
import { cn } from "@/components/lib/utils.js";

interface RunnerInstallDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** 安装成功后刷新机器列表（机器选择器数据源）。 */
  onInstalled?: () => void;
  /**
   * 「Open workspace」/「Later」/「Open model settings」：Root 关闭对话框、打开新机器的
   * home workspace（docs/next/runner-folders.md 决策 5）。未提供时退化为仅关闭。
   * 返回是否真的打开了；Root 用它决定是否提示（hello 可能 5s 内才到）。
   */
  onOpenRunnerWorkspace?: (runnerId: string) => Promise<boolean>;
  /** 「Choose folder…」：Root 关闭对话框并在该机器上打开目录浏览器。 */
  onChooseRunnerFolder?: (runnerId: string) => void;
  /**
   * 「打开模型设置」：由 Root 注入（关闭对话框 + 定位 Settings → Models）。
   * 不注入时该按钮退化为仅关闭对话框。
   */
  onOpenModelSettings?: () => void;
}

type Phase = "form" | "installing" | "models";

/** 认证方式（与桌面版 SSHDialog 的 password/privateKey 对齐，web 增加粘贴内容）。 */
type AuthMethod = "password" | "privateKey" | "privateKeyPath";

const AUTH_METHODS: ReadonlyArray<AuthMethod> = ["password", "privateKey", "privateKeyPath"];

const POLL_INTERVAL_MS = 1_000;

export function RunnerInstallDialog({
  open,
  onOpenChange,
  onInstalled,
  onOpenRunnerWorkspace,
  onChooseRunnerFolder,
  onOpenModelSettings,
}: RunnerInstallDialogProps) {
  const { intl } = useNexIntl();
  const platform = usePlatform();
  const [phase, setPhase] = useState<Phase>("form");
  const [host, setHost] = useState("");
  const [port, setPort] = useState("");
  const [username, setUsername] = useState("");
  const [name, setName] = useState("");
  const [authMethod, setAuthMethod] = useState<AuthMethod>("password");
  const [password, setPassword] = useState("");
  const [privateKey, setPrivateKey] = useState("");
  const [privateKeyPath, setPrivateKeyPath] = useState("");
  const [privateKeyPassphrase, setPrivateKeyPassphrase] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [status, setStatus] = useState<SSHInstallStatus | null>(null);
  // 安装成功后的「模型」步骤只记录新机器 id；轮询与判定都在
  // RunnerInstallModelsStep 内完成，向导本身不持有模型事实。
  const [modelRunnerId, setModelRunnerId] = useState<string | null>(null);
  const installIdRef = useRef<string | null>(null);
  const logTailRef = useRef<HTMLDivElement | null>(null);

  const credentialReady =
    authMethod === "password"
      ? password !== ""
      : authMethod === "privateKey"
        ? privateKey.trim() !== ""
        : privateKeyPath.trim() !== "";
  const submitDisabled = phase !== "form" || !host.trim() || !username.trim() || !credentialReady;

  const reset = useCallback(() => {
    setPhase("form");
    setError(null);
    setStatus(null);
    setModelRunnerId(null);
    installIdRef.current = null;
    // 凭据立刻清空：表单提交或取消后不留驻组件状态。
    setPassword("");
    setPrivateKey("");
    setPrivateKeyPassphrase("");
  }, []);

  useEffect(() => {
    if (!open) {
      reset();
    }
  }, [open, reset]);

  // 轮询安装进度（running 期间），终态停轮询。
  useEffect(() => {
    if (phase !== "installing" || !installIdRef.current) return;
    const installId = installIdRef.current;
    let cancelled = false;
    const poll = async () => {
      try {
        const next = await platform.getRunnerInstallStatus?.(installId);
        if (cancelled || !next) return;
        setStatus(next);
        if (next.status === "succeeded") {
          onInstalled?.();
          // 装好后不关闭：进入「模型」步骤（spec 决策 1）。平台没有模型状态
          // 能力时（desktop），或状态里没有 runnerId（无法查询）时保持旧行为直接关闭。
          if (!platform.listRunnerModelStatus || !next.runnerId) {
            setPhase("form");
            onOpenChange(false);
            return;
          }
          setModelRunnerId(next.runnerId);
          setPhase("models");
        } else if (next.status !== "running") {
          setError(next.error ?? next.status);
          setPhase("form");
        }
      } catch (pollError) {
        logger.warn("[runner-install] 轮询安装状态失败", {
          error: pollError instanceof Error ? pollError.message : String(pollError),
        });
      }
    };
    const timer = window.setInterval(poll, POLL_INTERVAL_MS);
    void poll();
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [phase, platform, onInstalled, onOpenChange]);

  // 日志自动滚到底部。
  useEffect(() => {
    const tail = logTailRef.current;
    if (tail) tail.scrollTop = tail.scrollHeight;
  }, [status]);

  const handleSubmit = useCallback(async () => {
    setError(null);
    setStatus(null);
    if (!platform.installRunnerOverSSH) {
      setError("not supported on this platform");
      return;
    }
    const portNumber = Number.parseInt(port.trim(), 10);
    try {
      const { installId } = await platform.installRunnerOverSSH({
        kind: "ssh",
        host: host.trim(),
        ...(Number.isFinite(portNumber) && portNumber > 0 ? { port: portNumber } : {}),
        username: username.trim(),
        ...(authMethod === "password" ? { password } : {}),
        ...(authMethod === "privateKey" ? { privateKey } : {}),
        ...(authMethod === "privateKeyPath" ? { privateKeyPath: privateKeyPath.trim() } : {}),
        ...(authMethod !== "password" && privateKeyPassphrase ? { privateKeyPassphrase } : {}),
        ...(name.trim() ? { name: name.trim() } : {}),
      });
      installIdRef.current = installId;
      // 凭据已随请求交给 server（内存中单次使用），组件不再持有。
      setPassword("");
      setPrivateKey("");
      setPrivateKeyPassphrase("");
      setPhase("installing");
    } catch (submitError) {
      setError(submitError instanceof Error ? submitError.message : String(submitError));
    }
  }, [
    platform,
    host,
    port,
    username,
    name,
    authMethod,
    password,
    privateKey,
    privateKeyPath,
    privateKeyPassphrase,
  ]);

  return (
    <Dialog open={open} onOpenChange={(next) => onOpenChange(next)}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Cloud className="size-4" />
            {intl.formatMessage({ id: "runnerInstall.title" })}
          </DialogTitle>
          <DialogDescription>
            {intl.formatMessage({ id: "runnerInstall.description" })}
          </DialogDescription>
        </DialogHeader>

        {phase === "form" ? (
          <div className="flex flex-col gap-3">
            <div className="grid grid-cols-3 gap-3">
              <div className="col-span-2 flex flex-col gap-1.5">
                <Label htmlFor="runner-install-host">
                  {intl.formatMessage({ id: "runnerInstall.host" })}
                </Label>
                <Input
                  id="runner-install-host"
                  value={host}
                  autoComplete="off"
                  placeholder="192.168.1.10"
                  onChange={(event) => setHost(event.target.value)}
                />
              </div>
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="runner-install-port">
                  {intl.formatMessage({ id: "runnerInstall.port" })}
                </Label>
                <Input
                  id="runner-install-port"
                  value={port}
                  placeholder="22"
                  inputMode="numeric"
                  onChange={(event) => setPort(event.target.value)}
                />
              </div>
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="runner-install-user">
                {intl.formatMessage({ id: "runnerInstall.username" })}
              </Label>
              <Input
                id="runner-install-user"
                value={username}
                autoComplete="off"
                onChange={(event) => setUsername(event.target.value)}
              />
            </div>

            {/* 认证方式（与桌面版 SSHDialog 相同的三选项 + web 场景说明）。 */}
            <div className="flex flex-col gap-1.5">
              <Label>{intl.formatMessage({ id: "runnerInstall.authMethod" })}</Label>
              <div className="grid grid-cols-3 gap-1.5">
                {AUTH_METHODS.map((method) => (
                  <button
                    key={method}
                    type="button"
                    data-testid={`runner-install-auth-${method}`}
                    className={cn(
                      "h-9 rounded-lg border px-2 text-ui-sm transition-colors",
                      authMethod === method
                        ? "border-primary bg-primary/10 text-foreground"
                        : "border-border text-foreground-subtle hover:bg-surface-hover",
                    )}
                    onClick={() => setAuthMethod(method)}
                  >
                    {intl.formatMessage({ id: `runnerInstall.auth.${method}` })}
                  </button>
                ))}
              </div>
            </div>

            {authMethod === "password" ? (
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="runner-install-password">
                  {intl.formatMessage({ id: "runnerInstall.password" })}
                </Label>
                <Input
                  id="runner-install-password"
                  type="password"
                  value={password}
                  autoComplete="new-password"
                  onChange={(event) => setPassword(event.target.value)}
                />
              </div>
            ) : null}
            {authMethod === "privateKey" ? (
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="runner-install-key">
                  {intl.formatMessage({ id: "runnerInstall.privateKey" })}
                </Label>
                <textarea
                  id="runner-install-key"
                  data-testid="runner-install-private-key"
                  value={privateKey}
                  rows={4}
                  spellCheck={false}
                  autoComplete="off"
                  className="w-full resize-y rounded-lg border border-border bg-input px-2.5 py-2 font-mono text-ui-sm text-foreground placeholder:text-foreground-subtlest focus-visible:border-input-border-focused focus-visible:outline-none"
                  placeholder={intl.formatMessage({ id: "runnerInstall.privateKeyPlaceholder" })}
                  onChange={(event) => setPrivateKey(event.target.value)}
                />
              </div>
            ) : null}
            {authMethod === "privateKeyPath" ? (
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="runner-install-key-path">
                  {intl.formatMessage({ id: "runnerInstall.privateKeyPath" })}
                </Label>
                <Input
                  id="runner-install-key-path"
                  value={privateKeyPath}
                  autoComplete="off"
                  spellCheck={false}
                  placeholder={intl.formatMessage({
                    id: "runnerInstall.privateKeyPathPlaceholder",
                  })}
                  onChange={(event) => setPrivateKeyPath(event.target.value)}
                />
              </div>
            ) : null}
            {authMethod !== "password" ? (
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="runner-install-passphrase">
                  {intl.formatMessage({ id: "runnerInstall.passphrase" })}
                </Label>
                <Input
                  id="runner-install-passphrase"
                  type="password"
                  value={privateKeyPassphrase}
                  autoComplete="new-password"
                  onChange={(event) => setPrivateKeyPassphrase(event.target.value)}
                />
              </div>
            ) : null}

            <div className="flex flex-col gap-1.5">
              <Label htmlFor="runner-install-name">
                {intl.formatMessage({ id: "runnerInstall.name" })}
              </Label>
              <Input
                id="runner-install-name"
                value={name}
                placeholder={host || undefined}
                onChange={(event) => setName(event.target.value)}
              />
            </div>
            {error ? (
              <p
                className="text-ui-base text-red-500"
                role="alert"
                data-testid="runner-install-error"
              >
                {error}
              </p>
            ) : null}
            <div className="flex justify-end gap-2 pt-1">
              <Button type="button" variant="ghost" onClick={() => onOpenChange(false)}>
                {intl.formatMessage({ id: "common.cancel" })}
              </Button>
              <Button
                type="button"
                disabled={submitDisabled}
                onClick={() => void handleSubmit()}
                data-testid="runner-install-submit"
              >
                {intl.formatMessage({ id: "runnerInstall.submit" })}
              </Button>
            </div>
          </div>
        ) : phase === "installing" ? (
          <div className="flex flex-col gap-3" data-testid="runner-install-progress">
            <p className="text-ui-base text-foreground-subtle">
              {intl.formatMessage({ id: "runnerInstall.installing" }, { host })}
            </p>
            <div
              ref={logTailRef}
              className="max-h-72 overflow-y-auto rounded-lg bg-surface px-3 py-2 font-mono text-ui-sm leading-relaxed"
            >
              {(status?.logs ?? []).map((line, index) => (
                <div
                  key={`${line.ts}-${index}`}
                  className={
                    line.level === "error"
                      ? "text-red-500"
                      : line.level === "warn"
                        ? "text-amber-500"
                        : "text-foreground-subtle"
                  }
                >
                  {line.message}
                </div>
              ))}
            </div>
            {error ? <p className="text-ui-base text-red-500">{error}</p> : null}
          </div>
        ) : modelRunnerId ? (
          <RunnerInstallModelsStep
            runnerId={modelRunnerId}
            onOpenWorkspace={() => {
              // 先关闭向导再打开 workspace：两个覆盖层同时存在会让新 tab 被对话框遮住。
              onOpenChange(false);
              void onOpenRunnerWorkspace?.(modelRunnerId);
            }}
            onChooseFolder={
              onChooseRunnerFolder
                ? () => {
                    onOpenChange(false);
                    onChooseRunnerFolder(modelRunnerId);
                  }
                : undefined
            }
            onOpenModelSettings={
              onOpenModelSettings
                ? () => {
                    // 「Open model settings」也属于新机器收尾动作（决策 5）：
                    // 先打开机器 home 的 workspace tab，再切到设置 tab；顺序反了
                    // 会让 addTab 把设置页盖掉。
                    onOpenChange(false);
                    void Promise.resolve(onOpenRunnerWorkspace?.(modelRunnerId))
                      .catch(() => false)
                      .then(() => onOpenModelSettings());
                  }
                : undefined
            }
          />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}
