import { useCallback, useEffect, useRef, useState } from "react";
import {
  buildRewardsUrl,
  createRewardsInjectionScript,
  isTrustedRewardsUrl,
  REWARDS_PARTITION,
  resolveRewardsOrigin,
  NEX_ENV,
  type RewardsContext,
} from "@nex/shared";
import { usePlatform } from "@/hooks/usePlatform.js";
import { useNexIntl } from "@/i18n/IntlProvider.js";
import { useNexStore } from "@/store/StoreProvider.js";
import { resolveTheme } from "@/useTheme.js";
import { logger } from "@/logger.js";
import { Button } from "@/components/ui/button.js";
import { EmbeddedWebsiteHeader } from "@/components/EmbeddedWebsiteHeader.js";

export function RewardsWebview({ onClose }: { onClose: () => void }) {
  const platform = usePlatform();
  const { intl, locale } = useNexIntl();
  const themePreference = useNexStore((s) => s.theme);
  const [systemDark, setSystemDark] = useState(
    () => matchMedia("(prefers-color-scheme: dark)").matches,
  );
  useEffect(() => {
    const media = matchMedia("(prefers-color-scheme: dark)");
    const changed = () => setSystemDark(media.matches);
    media.addEventListener("change", changed);
    return () => media.removeEventListener("change", changed);
  }, []);
  const theme: RewardsContext["theme"] = (
    themePreference === "system" ? systemDark : resolveTheme(themePreference) === "dark"
  )
    ? "zai-dark"
    : "zai-light";
  const env = import.meta.env;
  const policy = { dev: env.DEV, e2e: env.VITE_NEX_E2E_STORE_BRIDGE === "1" };
  const [url] = useState(() =>
    buildRewardsUrl(
      resolveRewardsOrigin({
        ...policy,
        env: NEX_ENV,
        override:
          typeof env.VITE_REWARDS_WEBVIEW_ORIGIN === "string"
            ? env.VITE_REWARDS_WEBVIEW_ORIGIN
            : undefined,
      }),
      locale,
      theme,
    ),
  );
  const view = useRef<ElectronWebviewTag | null>(null);
  const cleanup = useRef<(() => void) | null>(null);
  const generation = useRef(0);
  const ready = useRef(false);
  const authRevision = useRef(0);
  const latest = useRef({ theme, locale });
  latest.current = { theme, locale };
  const [error, setError] = useState(false);
  const [navigation, setNavigation] = useState({ loading: true, back: false, forward: false });
  const inject = useCallback(async () => {
    const element = view.current;
    if (!element || !ready.current) return;
    const epoch = ++generation.current;
    try {
      const href = element.getURL();
      if (!isTrustedRewardsUrl(href, policy)) return;
      // Nex 无账号体系：邀请页恒为匿名，只注入主题与语言。
      const snapshot: RewardsContext = {
        theme: latest.current.theme,
        locale: latest.current.locale,
        auth: {
          provider: null,
          status: "anonymous",
          revision: ++authRevision.current,
        },
      };
      await element.executeJavaScript(createRewardsInjectionScript(snapshot, href));
      if (epoch === generation.current) setError(false);
    } catch {
      if (epoch === generation.current) {
        setError(true);
        logger.warn("[rewards] context injection failed");
      }
    }
  }, [policy.dev, policy.e2e]);
  const injectRef = useRef(inject);
  injectRef.current = inject;
  useEffect(() => {
    void inject();
  }, [inject, locale, theme]);
  const attach = useCallback((element: ElectronWebviewTag | null) => {
    cleanup.current?.();
    view.current = element;
    if (!element) return;
    const sync = () =>
      setNavigation((s) => ({ ...s, back: element.canGoBack(), forward: element.canGoForward() }));
    const start = () => {
      ++generation.current;
      ready.current = false;
      setNavigation((s) => ({ ...s, loading: true }));
    };
    const stop = () => {
      sync();
      setNavigation((s) => ({ ...s, loading: false }));
      // Next 语言路由会触发 loading，但同文档导航没有新的 dom-ready；恢复桥接就绪。
      ready.current = true;
      void injectRef.current();
    };
    const loaded = () => {
      ready.current = true;
      sync();
      void injectRef.current();
    };
    const failed = (event: ElectronWebviewDidFailLoadEvent) => {
      if (event.isMainFrame && event.errorCode !== -3) {
        setError(true);
        logger.warn("[rewards] webview load failed", { code: event.errorCode });
      }
    };
    element.addEventListener("dom-ready", loaded);
    element.addEventListener("did-start-loading", start);
    element.addEventListener("did-stop-loading", stop);
    element.addEventListener("did-fail-load", failed);
    cleanup.current = () => {
      ++generation.current;
      // 除显式关闭外，Root 卸载也要撤销凭据；销毁中的 guest 失败可忽略。
      if (ready.current) {
        try {
          void element
            .executeJavaScript(
              createRewardsInjectionScript(
                {
                  theme: latest.current.theme,
                  locale: latest.current.locale,
                  auth: {
                    status: "anonymous",
                    provider: null,
                    revision: ++authRevision.current,
                  },
                },
                element.getURL(),
              ),
            )
            .catch(() => {});
        } catch {
          /* guest 已销毁。 */
        }
      }
      ready.current = false;
      element.removeEventListener("dom-ready", loaded);
      element.removeEventListener("did-start-loading", start);
      element.removeEventListener("did-stop-loading", stop);
      element.removeEventListener("did-fail-load", failed);
    };
  }, []);
  useEffect(
    () => () => {
      cleanup.current?.();
      view.current = null;
    },
    [],
  );
  const close = async () => {
    ++generation.current;
    const element = view.current;
    try {
      if (element && ready.current)
        await element.executeJavaScript(
          createRewardsInjectionScript(
            {
              theme,
              locale,
              auth: { status: "anonymous", provider: null, revision: generation.current },
            },
            element.getURL(),
          ),
        );
    } catch {
      /* WebView 已销毁时不阻塞关闭。 */
    }
    onClose();
  };
  return (
    <section
      data-testid="rewards-surface"
      className="fixed inset-0 z-50 flex flex-col overflow-hidden bg-background pt-12 text-foreground"
    >
      <EmbeddedWebsiteHeader
        title={intl.formatMessage({ id: "rewards.title" })}
        loading={navigation.loading}
        canGoBack={navigation.back}
        canGoForward={navigation.forward}
        onBack={() => view.current?.goBack()}
        onForward={() => view.current?.goForward()}
        onReload={() => view.current?.reload()}
        onClose={() => void close()}
      />
      {error && (
        <div
          role="alert"
          className="mx-auto flex w-full max-w-5xl items-center gap-3 px-6 text-ui-base"
        >
          <span>{intl.formatMessage({ id: "rewards.loadFailed" })}</span>
          <Button
            variant="ghost"
            onClick={() => {
              setError(false);
              view.current?.reload();
            }}
          >
            {intl.formatMessage({ id: "common.refresh" })}
          </Button>
          <Button variant="ghost" onClick={() => void platform.openExternal(url)}>
            {intl.formatMessage({ id: "rewards.openWebsite" })}
          </Button>
        </div>
      )}
      {/* 网页视口必须铺满窗口；限宽交给网站内容，否则滚动条也会被居中内缩。 */}
      <webview
        ref={attach}
        src={url}
        partition={REWARDS_PARTITION}
        className="min-h-0 w-full flex-1 bg-background"
        data-testid="rewards-webview"
      />
    </section>
  );
}
