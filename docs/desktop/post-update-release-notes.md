# Post-Update Release Notes

更新说明来自 GitHub Release：electron-updater 先读取 Release 资产中的 channel yml
（`latest-mac.yml` / `latest.yml` / `latest-linux.yml`）里的 `releaseName` / `releaseNotes`；
若 yml 没带说明，GitHub provider 会用 Release body（`No content.` 视为空）。
发布流程不再需要由 infra 单独编辑多语言 `latest.yml`。

自动更新下载完成后，main 进程把更新说明转换为 `PostUpdateReleaseNotesPayload`
（`version` / `title` / `markdown`，可选 `releaseDate`）并持久化到 `pendingPostUpdateReleaseNotes`，
同一份 payload 随 `update-downloaded` 状态广播给 renderer。左上角更新按钮 hover 与安装后的更新说明弹窗都读取
`markdown`；`releaseNotesByLocale` 字段保留在协议里，但 GitHub provider 不再产生多语言版本，
renderer 会回退到默认 `markdown`（`packages/ui/src/updateReleaseNotes.ts`）。

如果用户尚未安装就重启应用，且 pending 版本仍高于当前应用版本，main 会用这份 payload 恢复待安装状态，
但不会提前把它作为安装后的说明发给 renderer 确认。

若用户未通过本次自动更新链路安装（例如官网安装包跳级升级），磁盘里可能仍残留**更低版本**的待展示说明；
启动时若 semver 判定该版本低于当前 `app.getVersion()`，会丢弃该 pending，避免弹出旧版更新日志。

当前 Root 收到安装后待展示说明时会静默 ack，避免弹窗打断用户。Markdown 渲染沿用现有消息渲染能力。
