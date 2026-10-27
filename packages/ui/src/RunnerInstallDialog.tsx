/**
 * SSH 安装 runner 向导（docs/next/runner-install.md）。
 *
 * SSH 只作为安装渠道：填一次 SSH 凭据 → server 拨 SSH 在目标机装好并启动
 * runner → runner 之后直连 server。凭据只驻安装会话内存，不落盘。
 *
 * 两步：表单（host/port/user/password/name）→ 进度（轮询 install 会话的
 * 日志环形缓冲，成功后提示机器已出现在机器选择器）。
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
import { useNexIntl } from "@/i18n/IntlProvider.js";
import { usePlatform } from "@/hooks/usePlatform.js";
import { logger } from "@/logger.js";

interface RunnerInstallDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** 安装成功后刷新机器列表（机器选择器数据源）。 */
  onInstalled?: () => void;
}

type Phase = "form" | "installing";

const POLL_INTERVAL_MS = 1_000;

export function RunnerInstallDialog({ open, onOpenChange, onInstalled }: RunnerInstallDialogProps) {
  const { intl } = useNexIntl();
  const platform = usePlatform();
  const [phase, setPhase] = useState<Phase>("form");
  const [host, setHost] = useState("");
  const [port, setPort] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [name, setName] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [status, setStatus] = useState<SSHInstallStatus | null>(null);
  const installIdRef = useRef<string | null>(null);
  const logTailRef = useRef<HTMLDivElement | null>(null);

  const submitDisabled =
    phase === "installing" || !host.trim() || !username.trim() || !password;

  const reset = useCallback(() => {
    setPhase("form");
    setError(null);
    setStatus(null);
    installIdRef.current = null;
    // 密码立刻清空：表单提交或取消后不留驻组件状态。
    setPassword("");
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
          setPhase("form");
          onInstalled?.();
          onOpenChange(false);
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
        password,
        ...(name.trim() ? { name: name.trim() } : {}),
      });
      installIdRef.current = installId;
      // 密码已随请求交给 server（内存中单次使用），组件不再持有。
      setPassword("");
      setPhase("installing");
    } catch (submitError) {
      setError(submitError instanceof Error ? submitError.message : String(submitError));
    }
  }, [platform, host, port, username, password, name]);

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
            <div className="grid grid-cols-2 gap-3">
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
            </div>
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
              <p className="text-ui-base text-red-500" role="alert" data-testid="runner-install-error">
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
        ) : (
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
        )}
      </DialogContent>
    </Dialog>
  );
}
