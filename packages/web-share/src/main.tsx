import { createRoot } from "react-dom/client";
import "../../share-ui/src/styles.css";
import { applyTheme, type Theme } from "../../ui/src/useTheme.js";
import {
  ConversationShareLandingLoader,
  ConversationShareLandingStatus,
} from "../../web/src/share/ConversationShareLandingPage.js";
import {
  ConversationSharePreviewClient,
  resolveConversationShareRouteLocale,
} from "../../web/src/share/conversationSharePreviewClient.js";
import {
  isConversationSharePath,
  resolveConversationShareCodeFromPath,
} from "../../web/src/share/conversationShareRoute.js";
import { resolveWebRemoteControlInitialTheme } from "../../web/src/web-remote-control/adapters/webRemoteControlThemeSeed.js";

const root = createRoot(document.getElementById("root")!);

function resolveThemePreference(): Theme {
  const saved = localStorage.getItem("nex-theme");
  return resolveWebRemoteControlInitialTheme({ storedTheme: saved, defaultTheme: "zai-light" });
}

// Share 是独立入口，不会经过完整 Web 的 bootstrap；必须在渲染前同步根主题 class，
// 否则页面外壳使用默认浅色 token，而 MessageResponse 会按 zai-dark 选择 github-dark，
// 最终产生浅色页面套深色 Markdown 代码块的混合样式。
const INITIAL_THEME = resolveThemePreference();
applyTheme(INITIAL_THEME);

// Nex 无账号体系：分享页不再有 OAuth 回调/登录流程，只匿名预览公开分享。

async function renderShare(): Promise<void> {
  const locale = resolveConversationShareRouteLocale(window.location.pathname);
  document.documentElement.lang = locale;
  document.title = locale === "zh-CN" ? "Nex 会话分享" : "Nex Conversation Share";

  const shareCode = resolveConversationShareCodeFromPath(window.location.pathname);
  if (!shareCode) {
    root.render(
      <ConversationShareLandingStatus
        state={{ kind: "error", error: "invalid_contract" }}
        locale={locale}
      />,
    );
    return;
  }

  const endpointOrigin =
    import.meta.env.VITE_NEX_BASE_URL?.trim().replace(/\/+$/u, "") || window.location.origin;
  const mockMode =
    import.meta.env.DEV && import.meta.env.VITE_CONVERSATION_SHARE_PREVIEW_MOCK === "true";
  const client = mockMode
    ? new (
        await import("../../web/src/share/mockConversationSharePreviewClient.js")
      ).MockConversationSharePreviewClient()
    : new ConversationSharePreviewClient({ baseUrl: `${endpointOrigin}/api/v1` });
  const getMockToken = () =>
    mockMode && window.sessionStorage.getItem("nex:share:mock-auth") === "owner"
      ? "mock-owner-token"
      : null;

  root.render(
    <ConversationShareLandingLoader
      shareCode={shareCode}
      client={client}
      getAccessToken={() => getMockToken()}
      onLogin={() => {
        if (!mockMode) return;
        window.sessionStorage.setItem("nex:share:mock-auth", "owner");
        window.location.reload();
      }}
      onLogout={() => {
        if (!mockMode) return;
        window.sessionStorage.removeItem("nex:share:mock-auth");
        window.location.reload();
      }}
      locale={locale}
      theme={INITIAL_THEME}
    />,
  );
}

async function main(): Promise<void> {
  if (isConversationSharePath(window.location.pathname)) {
    await renderShare();
    return;
  }
  root.render(
    <ConversationShareLandingStatus state={{ kind: "error", error: "invalid_contract" }} />,
  );
}

void main();
