import { useState, type FormEvent } from "react";
import { Button, Input, NexAboutLogo, useNexIntl } from "@nex/ui";
import { sanitizeNextPath, submitWebLogin, type WebLoginResult } from "./webAuthSession.js";

type LoginFailure = Exclude<WebLoginResult, { kind: "ok" }>;

export function WebLoginPage({ nextPath }: { nextPath: string }) {
  const { intl } = useNexIntl();
  const [token, setToken] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [failure, setFailure] = useState<LoginFailure | null>(null);

  const errorMessage = (() => {
    if (!failure) return null;
    if (failure.kind === "invalid") return intl.formatMessage({ id: "webLogin.error.invalid" });
    if (failure.kind === "rateLimited") {
      return failure.retryAfterSeconds
        ? intl.formatMessage(
            { id: "webLogin.error.rateLimited" },
            { seconds: failure.retryAfterSeconds },
          )
        : intl.formatMessage({ id: "webLogin.error.rateLimitedGeneric" });
    }
    return intl.formatMessage({ id: "webLogin.error.network" });
  })();

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const trimmed = token.trim();
    if (!trimmed || submitting) return;
    setSubmitting(true);
    setFailure(null);
    const result = await submitWebLogin(trimmed);
    if (result.kind === "ok") {
      // 整页跳转：登录前建立失败的 WebSocket/服务状态不能带进登录后的应用。
      window.location.replace(sanitizeNextPath(nextPath));
      return;
    }
    setFailure(result);
    setSubmitting(false);
  };

  return (
    <div className="flex h-dvh min-h-dvh w-screen items-center justify-center bg-background px-4 text-foreground">
      <form
        className="w-full max-w-sm rounded-xl border border-card-border bg-card p-6"
        onSubmit={handleSubmit}
      >
        <div className="flex items-center gap-3">
          <div
            className="relative flex size-10 items-center justify-center rounded-lg bg-[linear-gradient(180deg,#000000_0%,#151718_100%)] text-[#ffffff]"
            aria-hidden="true"
          >
            <NexAboutLogo className="h-auto w-5" />
          </div>
          <h1 className="text-ui-lg font-medium">{intl.formatMessage({ id: "webLogin.title" })}</h1>
        </div>
        <p className="mt-3 text-ui-sm text-foreground-subtle">
          {intl.formatMessage({ id: "webLogin.description" })}
        </p>
        <label className="mt-5 block text-ui-sm text-foreground-subtle" htmlFor="web-login-token">
          {intl.formatMessage({ id: "webLogin.tokenLabel" })}
        </label>
        <Input
          id="web-login-token"
          name="token"
          type="password"
          size="lg"
          autoFocus
          autoComplete="current-password"
          autoCapitalize="off"
          autoCorrect="off"
          spellCheck={false}
          className="mt-1.5 text-mobile-input-safe md:text-ui-base"
          placeholder={intl.formatMessage({ id: "webLogin.tokenPlaceholder" })}
          value={token}
          aria-invalid={failure ? true : undefined}
          aria-describedby={errorMessage ? "web-login-error" : undefined}
          disabled={submitting}
          onChange={(event) => {
            setToken(event.target.value);
            if (failure) setFailure(null);
          }}
        />
        {errorMessage ? (
          <p id="web-login-error" role="alert" className="mt-2 text-ui-sm text-destructive">
            {errorMessage}
          </p>
        ) : null}
        <p className="mt-3 text-ui-xs text-foreground-subtle">
          {intl.formatMessage({ id: "webLogin.hint" })}
        </p>
        <Button
          type="submit"
          size="lg"
          className="mt-5 h-9 w-full"
          disabled={submitting || token.trim().length === 0}
        >
          {intl.formatMessage({ id: submitting ? "webLogin.submitting" : "webLogin.submit" })}
        </Button>
      </form>
    </div>
  );
}
