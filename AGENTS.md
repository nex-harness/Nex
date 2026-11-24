# Nex-v3 Project Conventions

## Architecture & Build
- Pure Go backend (`CGO_ENABLED=0`) + React/TypeScript Web UI (`clients/web`, embedded into `pkg/webui/dist`).
- Every new user-facing feature must be designed and implemented end-to-end, including Server persistence, Runner `ConfigSnapshot` sync (when applicable), Settings UI, and tests.

## Web UI & i18n (`clients/web/src/`)
- **NEVER** use hardcoded binary locale checks such as `const isZh = ...`, `locale === 'zh-CN'`, `locale.startsWith('zh')`, or inline `isZh ? '...' : '...'` ternaries outside `clients/web/src/i18n/`.
- Always add user-visible strings to both `clients/web/src/i18n/locales/en-US.ts` and `clients/web/src/i18n/locales/zh-CN.ts` with 100% key and `{placeholder}` parity, and render them via `formatMsg(locale, 'key', params)` or `useT()`.
- Use `nex-*` prefixes for custom CSS classes, keyframes, and CSS variables (never legacy prefixes).
- Always run `npm --prefix clients/web test` (which enforces `i18n.test.ts` parity and zero hardcoded locale checks) and `npm --prefix clients/web run build` before committing frontend changes.

## Git & Release
- Run relevant Go (`go test ./...`) and Web tests before committing.
- Push commits and release tags to both `gitea` (`git@gitea.tylinux.cn:tylinux/Nex-v3.git`) and `origin` (`git@github.com:nex-harness/Nex.git`).
- Update `CHANGELOG.md` on every release.
