# 17. Mobile Native Client Spec (React Native + Expo)

## 1. Background & Goals

Nex-v3 currently ships mobile builds via `mygo` WebView (`clients/app`), which maximizes code reuse but inherits WebView limitations on iOS and Android (keyboard avoidance jitter, scroll physics, native haptics, camera/notification integration, and cold-start overhead).

This spec defines a **React Native + Expo** native mobile client (`clients/mobile`) positioned as a **Companion App** for iOS and Android, reusing Nex-v3's existing React + TypeScript + Zustand core logic while delivering native rendering and mobile-first ergonomics.

### Design Principles
- **Core Reuse, Native UI**: Share protocol types, WebSocket client, streaming event reducer, Zustand store, and i18n dictionaries between Web (`clients/web`) and Mobile (`clients/mobile`) via a shared workspace package (`clients/shared`).
- **Companion Scope (80/20 Rule)**: Focus on high-frequency mobile workflows (chat, streaming observation, tool/permission approval, inline questions, image capture/upload, model & effort switching, notifications). Keep heavy administration (SSH/Runner deployment, Diff Review, PTY terminal, complex Settings) on Desktop/Web.
- **Expo CNG (Continuous Native Generation)**: Use Expo with `expo prebuild` (New Architecture / Fabric enabled) rather than maintaining hand-edited Xcode/Gradle projects.
- **Remote-First, Local-Optional**: Ship Phase 1–3 connecting to an existing `nex-server` (LAN / public HTTPS / Relay + QR pairing). Optional embedded local Go server (`gomobile` + Expo Module) is deferred to Phase 4.

---

## 2. Repository & Package Layout

```text
clients/
├── shared/                     # Platform-agnostic TypeScript core (shared by web & mobile)
│   ├── package.json
│   └── src/
│       ├── core/
│       │   ├── types.ts        # Session, Event, Stream, Telemetry, Model types
│       │   ├── reducer.ts      # Pure O(tail) streaming event reducer
│       │   ├── protocol.ts     # REST fetch wrapper (injected baseURL + token + fetch)
│       │   ├── wsClient.ts     # Auto-reconnecting WebSocket client
│       │   └── storage.ts      # PlatformStorage interface (localStorage vs SecureStore/MMKV)
│       └── i18n/
│           ├── index.ts        # formatMsg & duration/number formatters
│           └── locales/
│               ├── en-US.ts
│               └── zh-CN.ts
├── web/                        # Existing Desktop/Browser Web UI (imports from @nex/shared)
└── mobile/                     # New React Native + Expo app (iOS & Android)
    ├── app.json                # Expo config (bundleId, permissions, plugins)
    ├── package.json
    ├── metro.config.js         # Monorepo watchFolders for ../shared
    ├── app/                    # expo-router file-based routes
    │   ├── _layout.tsx         # Root provider, theme, SafeArea, KeyboardProvider
    │   ├── connect.tsx         # Server Connect / QR Pairing / Paired Hosts
    │   ├── index.tsx           # Main Conversation Screen + Drawer
    │   └── settings.tsx        # Lightweight mobile settings (Server, Appearance, Notifications, About)
    └── src/
        ├── bridge/             # Storage (expo-secure-store / MMKV), Notifications, Haptics, Clipboard
        ├── store/              # Mobile Zustand store composed over @nex/shared
        └── components/
            ├── SessionDrawer.tsx
            ├── MessageList.tsx         # FlashList-backed timeline
            ├── MarkdownBlock.tsx       # Native Markdown + syntax-highlighted code blocks + autolinks
            ├── WorkGroupCard.tsx       # Collapsible Thinking + Tool execution summary
            ├── ApprovalCard.tsx        # Native Allow / Deny / Always Allow card
            ├── InlineQuestionCard.tsx  # Native paginated question selector + custom input
            ├── StreamErrorBanner.tsx   # Compact error bar (Show details / Copy / Dismiss)
            ├── ComposerBar.tsx         # Keyboard-aware input, image picker/camera, model & effort sheet
            └── QRScannerModal.tsx      # expo-camera QR pairing scanner
```

---

## 3. Technology Stack

| Layer | Choice | Rationale |
| --- | --- | --- |
| Framework | **Expo SDK (New Architecture / Fabric)** | Official RN default; zero manual Podfile/Gradle drift; seamless native modules via `expo prebuild` |
| Navigation | **Expo Router** | File-based routing, native stack & drawer transitions, deep-link (`nex://pair`) support |
| Styling | **NativeWind v4** | Tailwind CSS syntax; reuses Web UI color tokens and dark/light theme semantics |
| State & Core | **Zustand + `@nex/shared`** | 100% reuse of `reducer.ts`, `wsClient.ts`, `types.ts`, and `i18n` |
| List Rendering | **`@shopify/flash-list`** | Smooth 60/120fps scrolling for long conversations with streaming updates |
| Keyboard | **`react-native-keyboard-controller`** | Native interactive keyboard avoidance and smooth composer tracking on iOS & Android |
| Native Capabilities | `expo-camera`, `expo-secure-store`, `expo-image-picker`, `expo-notifications`, `expo-haptics`, `expo-clipboard` | First-party Expo modules with aligned versions and clean permissions |

---

## 4. Feature Scope

### 4.1 Included in Companion App (v1)
1. **Server Connection & Pairing**:
   - Connect via `http(s)://` URL or scan `nex://pair?...` QR code from Desktop/Web Settings → Remote Access.
   - Persist paired hosts and credentials securely in `expo-secure-store` (iOS Keychain / Android Keystore).
   - Switch between multiple saved servers.
2. **Session Management**:
   - Slide-out drawer with runner filter pills, active/unread badges, recent sessions grouped by project, search, and New Task creation.
3. **Conversation Timeline**:
   - Streaming Markdown rendering (headings, lists, tables, code blocks with copy button, clickable bare URLs, inline images with full-screen zoom viewer).
   - Collapsible `WorkGroup` (Thinking + Tool calls) collapsed by default during streaming with 1-line thinking preview.
   - Inline Compaction divider, Stream Error banner (`Show details` / `Copy` / `Dismiss`), and whole-second running duration indicator.
4. **Interactive Agent Controls**:
   - Permission approval card (Allow / Deny / Always Allow) with haptic feedback.
   - Inline Question Card (single/multi-select, paginated questions, custom text input).
   - Steering / Follow-up queueing and Abort button while running.
5. **Mobile Composer**:
   - Auto-growing text input with native keyboard avoidance.
   - Attach images from photo library or camera (`expo-image-picker`), isolated per session.
   - Bottom-sheet pickers for Runner/Workspace, Provider/Model, Thinking Effort, and Permission Mode.
6. **Lightweight Settings**:
   - Current Server / Switch Server, Theme (System/Light/Dark), Language (EN/ZH), Notifications toggle, About & Check for Updates (`/api/v1/updates/check`).

### 4.2 Explicitly Deferred / Out of Scope for v1
- Full SSH Target deployment & Runner management UI
- Interactive PTY Terminal & File Diff Review workbench
- Browser screencast / handoff viewer
- Skills / MCP / Provider API key editing (managed on Desktop/Web)
- Embedded local Go server (`gomobile` local mode — see Section 6)

---

## 5. Phased Implementation Plan

### Phase 0: Extract `@nex/shared` Core
- Introduce `PlatformStorage` and `PlatformEnv` adapters to decouple `localStorage` and `window.location` from `clients/web/src/core/`.
- Move `types.ts`, `reducer.ts`, `wsClient.ts`, `protocol.ts`, and `i18n/` into `clients/shared`.
- Update `clients/web` imports and verify all existing Web unit tests (`npm --prefix clients/web test`) and production build pass with zero regressions.

### Phase 1: Expo App Skeleton & Core Chat
- Initialize `clients/mobile` with Expo, Expo Router, NativeWind, and Metro monorepo config pointing to `clients/shared`.
- Implement `connect.tsx` (URL input, QR scanner via `expo-camera`, saved hosts via `expo-secure-store`, health check).
- Implement `SessionDrawer`, `MessageList` (`FlashList`), `MarkdownBlock`, `WorkGroupCard`, and basic `ComposerBar`.

### Phase 2: Interactive Cards & Mobile Polish
- Implement `ApprovalCard`, `InlineQuestionCard`, `StreamErrorBanner`, and inline compaction divider.
- Add camera/gallery image attachment, full-screen image viewer, model/effort bottom sheets, and haptic feedback.
- Add local notifications (`expo-notifications`) for `task_completed` and `question` events when app is backgrounded.
- Add lightweight `settings.tsx` (Server switch, Theme, Language, About & Check for Updates).

### Phase 3: CI/CD & Release Integration
- Add GitHub Actions jobs to `.github/workflows/build.yml`:
  - Android: `npx expo prebuild --platform android` + Gradle release APK build (uploaded to GitHub Release alongside existing artifacts).
  - iOS: `npx expo prebuild --platform ios` + Xcode archive/IPA (or Simulator `.app.zip` when signing secrets are absent).
- Sync app version via `scripts/set-app-version.sh` into `clients/mobile/app.json`.

---

## 6. Future Extension: Optional Local Mode (Phase 4)

If offline/standalone local execution on mobile is desired later:
1. Build a thin Go wrapper package exporting `StartLocalServer(dataDir string) (addr string, err error)` and `StopLocalServer()` via `gomobile bind` into `NexCore.xcframework` (iOS) and `nexcore.aar` (Android).
2. Wrap it in a local Expo Module (`modules/nex-local-server`).
3. On iOS, disable the `bash` tool in the embedded runner due to OS `fork`/`exec` sandbox restrictions; on Android, scope the workspace to app-private storage (`<AppData>/workspace`).
4. Expose a "Local Mode" option alongside "Connect to Server" in `connect.tsx`.

---

## 7. Verification Strategy

- **Shared Core**: Vitest unit tests covering `reducer.ts`, `i18n.test.ts` (100% key/placeholder parity), and `PlatformStorage` adapter.
- **Mobile**: Jest + `@testing-library/react-native` tests for `connect.tsx` pairing flow, `MessageList` streaming render, `ApprovalCard`, and `InlineQuestionCard`.
- **Device Smoke Test**: Verify on iOS Simulator / physical iPhone and Android Emulator / physical Android device against a live `nex-server` (LAN + Relay).
