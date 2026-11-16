!include nsDialogs.nsh
!include FileFunc.nsh

!ifndef NEX_INSTALLER_DEFAULT_LOG_PATH
  !define NEX_INSTALLER_DEFAULT_LOG_PATH "$TEMP\Nex-installer.log"
!endif
!ifndef NEX_INSTALLER_ELEVATED_LOG_PATH
  !define NEX_INSTALLER_ELEVATED_LOG_PATH "$WINDIR\Logs\Nex-installer.log"
!endif
!ifndef NEX_INSTALLER_IS_ELEVATED_INNER
  ; 修复原因：来源只在测试夹具模拟内层，正式默认恒假会让提权进程继续使用调用方 /LOG。
  ; 使用 electron-builder 同一 UAC 判据；隔离夹具仍可显式替换，不改变真正的提权流程。
  !include UAC.nsh
  !define NEX_INSTALLER_IS_ELEVATED_INNER `${UAC_IsInnerInstance}`
!endif

!ifndef NEX_INSTALLER_PROCESS_HELPER
  ; CR-01 修复原因：普通 NSIS 只有 APP_64/APP_ARM64（压缩包路径），没有 APP_DIR_*；
  ; 正式构建由 afterPack 落盘按架构的 helper define 文件（见 electron-builder.config.js），
  ; 编译时按目标架构 include；缺失不在此报错，宏内在真正使用处明确失败。
  !ifdef APP_ARM64
    !include /NONFATAL "${__FILEDIR__}\generated\nex-process-helper-arm64.nsh"
  !else
    !include /NONFATAL "${__FILEDIR__}\generated\nex-process-helper-x64.nsh"
  !endif
!endif

!ifndef NEX_INSTALL_MANIFEST_NAME
  !define NEX_INSTALL_MANIFEST_NAME ".nex-install-manifest"
!endif

!ifndef NEX_UNINSTALLER_LOG_PATH
  !define NEX_UNINSTALLER_LOG_PATH "$TEMP\Nex-uninstaller.log"
!endif
!ifndef NEX_UNINSTALLER_FUNCTION_PREFIX
  !define NEX_UNINSTALLER_FUNCTION_PREFIX "un."
!endif

!ifdef BUILD_UNINSTALLER
  Var NexUninstallerLogUnavailable

  ; 卸载器只在更新时删除旧文件；单独记录清理阶段，避免外层把权限/空间错误误报成应用仍在运行。
  !macro NexReportUninstallerStage MESSAGE
    DetailPrint "Nex: ${MESSAGE}"
    Push "${MESSAGE}"
    Call ${NEX_UNINSTALLER_FUNCTION_PREFIX}NexWriteUninstallerLog
  !macroend

  Function ${NEX_UNINSTALLER_FUNCTION_PREFIX}NexWriteUninstallerLog
    Exch $R9
    Push $R0
    Push $R1
    Push $R2

    StrCmp $NexUninstallerLogUnavailable "1" nexUninstallerLogDone
    ClearErrors
    FileOpen $R1 "${NEX_UNINSTALLER_LOG_PATH}" a
    IfErrors nexUninstallerLogFailed nexUninstallerLogWrite
    nexUninstallerLogWrite:
      System::Call "kernel32::GetCurrentProcessId() i.R0"
      FileSeek $R1 0 END
      FileWrite $R1 "[pid=$R0] $R9$\r$\n"
      FileClose $R1
      Goto nexUninstallerLogDone
    nexUninstallerLogFailed:
      ; 日志不可写不应改变卸载结果，保留原始清理错误供外层处理。
      StrCpy $NexUninstallerLogUnavailable "1"
      ClearErrors
    nexUninstallerLogDone:
      Pop $R2
      Pop $R1
      Pop $R0
      Pop $R9
  FunctionEnd

  !macro customRemoveFilesDiagnosticsStart
    !insertmacro NexReportUninstallerStage "cleanup-started"
  !macroend

  !macro customRemoveFilesDiagnosticsComplete
    !insertmacro NexReportUninstallerStage "cleanup-completed"
  !macroend
!endif

!macro customRemoveFiles
  ; Bugfix：electron-builder 默认在更新时递归删除整个 $INSTDIR，用户放入的无关文件也会被清掉。
  ; 只按上一版本随包生成的所有权清单删除，清单缺失时迁移旧版本采用 fail-open 保留策略。
  ${if} ${isUpdated}
    !ifdef BUILD_UNINSTALLER
      !insertmacro customRemoveFilesDiagnosticsStart
    !endif
    ClearErrors
    FileOpen $R0 "$INSTDIR\${NEX_INSTALL_MANIFEST_NAME}" r
    IfErrors nexManifestMissing

    nexManifestRead:
      ClearErrors
      FileRead $R0 $R1
      IfErrors nexManifestClose
      ; NSIS FileRead 保留行尾 CRLF；打包清单统一使用换行结尾，先去掉两个行尾字符。
      StrCpy $R1 $R1 -2
      StrCmp $R1 "" nexManifestRead

      ; 拒绝绝对路径和 .. 前缀，避免损坏或篡改清单越界删除。
      StrCpy $R2 $R1 1
      StrCmp $R2 "\\" nexManifestRead
      StrCmp $R2 "/" nexManifestRead
      StrCpy $R2 $R1 2
      StrCmp $R2 ".." nexManifestRead
      StrCmp $R1 "${UNINSTALL_FILENAME}" nexManifestRead
      ; 清单是当前打开的元数据，自引用条目不能当作普通载荷删除。
      StrCmp $R1 "${NEX_INSTALL_MANIFEST_NAME}" nexManifestRead
      GetFullPathName $R2 "$INSTDIR\$R1"
      StrCmp $R2 "$INSTDIR\$R1" 0 nexManifestRead

      ; 当前版本卸载器与外层安装器是两个进程；逐项记录到卸载器日志，便于核对真正尝试删除的文件。
      !ifdef BUILD_UNINSTALLER
        !insertmacro NexReportUninstallerStage "cleanup-file path=$R1"
      !endif
      ClearErrors
      Delete "$INSTDIR\$R1"
      IfErrors nexManifestDeleteFailed
      Goto nexManifestRead

    nexManifestDeleteFailed:
      System::Call 'kernel32::GetLastError() i.R2'
      FileClose $R0
      !ifdef BUILD_UNINSTALLER
        !insertmacro NexReportUninstallerStage "cleanup-failed reason=delete-failed path=$R1 win32-error=$R2"
      !endif
      Abort "无法删除旧版本文件：$INSTDIR\$R1（Win32 错误 $R2）"

    nexManifestClose:
      FileClose $R0
      Goto nexManifestDone

    nexManifestMissing:
      ; 首次从旧版本升级时没有清单，不能猜测所有权并删除用户文件。
      !ifdef BUILD_UNINSTALLER
        !insertmacro NexReportUninstallerStage "cleanup-skipped reason=manifest-missing action=preserve"
      !endif
      ClearErrors

    nexManifestDone:
      !ifdef BUILD_UNINSTALLER
        !insertmacro customRemoveFilesDiagnosticsComplete
      !endif
  ${else}
    ; 普通卸载仍保持 electron-builder 的全量删除语义；ownership 清单只约束覆盖更新。
    SetOutPath $TEMP
    RMDir /r $INSTDIR
  ${endIf}
!macroend

!ifndef BUILD_UNINSTALLER
  Var NexInstallerLogPath
  Var NexInstallerLogUnavailable
  Var NexInstallerProcessRole
  Var NexUninstallerDetailsUnavailable
  Var NexPreviousUninstallerSupportsManifest

  ; 详情面板和文件日志共用同一条阶段事件，避免静默安装丢失关键上下文。
  !macro NexReportInstallerStage MESSAGE
    SetDetailsPrint listonly
    DetailPrint "Nex: ${MESSAGE}"
    Push "${MESSAGE}"
    Call NexWriteInstallerLog
  !macroend

  Function NexWriteInstallerLog
    Exch $R9
    Push $R0
    Push $R1
    Push $R2

    StrCmp $NexInstallerLogPath "" nexInstallerLogDone
    StrCmp $NexInstallerLogUnavailable "1" nexInstallerLogDone
    StrCpy $R2 0
    nexInstallerLogOpen:
      ClearErrors
      FileOpen $R1 $NexInstallerLogPath a
      IfErrors nexInstallerLogRetry nexInstallerLogWrite
    nexInstallerLogRetry:
      IntOp $R2 $R2 + 1
      IntCmp $R2 3 nexInstallerLogFailed nexInstallerLogWait nexInstallerLogFailed
    nexInstallerLogWait:
      Sleep 50
      Goto nexInstallerLogOpen
    nexInstallerLogWrite:
      System::Call "kernel32::GetCurrentProcessId() i.R0"
      FileSeek $R1 0 END
      FileWrite $R1 "[pid=$R0] $R9$\r$\n"
      FileClose $R1
      Goto nexInstallerLogDone
    nexInstallerLogFailed:
      StrCpy $NexInstallerLogUnavailable "1"
      ClearErrors
    nexInstallerLogDone:
      Pop $R2
      Pop $R1
      Pop $R0
      Pop $R9
  FunctionEnd

  Function NexResetUninstallerLog
    StrCpy $NexUninstallerDetailsUnavailable ""
    ClearErrors
    FileOpen $R0 "${NEX_UNINSTALLER_LOG_PATH}" w
    IfErrors nexUninstallerDetailsResetFailed nexUninstallerDetailsResetSucceeded
    nexUninstallerDetailsResetSucceeded:
      FileClose $R0
      Goto nexUninstallerDetailsResetDone
    nexUninstallerDetailsResetFailed:
      ; 外层详情不能读取旧卸载器日志时仍继续安装，文件日志和退出码仍是最终依据。
      StrCpy $NexUninstallerDetailsUnavailable "1"
      ClearErrors
    nexUninstallerDetailsResetDone:
  FunctionEnd

  Function NexShowUninstallerCleanupDetails
    Push $R0
    Push $R1
    Push $R2

    StrCmp $NexUninstallerDetailsUnavailable "1" nexShowUninstallerDetailsDone
    ClearErrors
    FileOpen $R0 "${NEX_UNINSTALLER_LOG_PATH}" r
    IfErrors nexShowUninstallerDetailsDone
    nexShowUninstallerDetailsRead:
      ClearErrors
      FileRead $R0 $R1
      IfErrors nexShowUninstallerDetailsClose
      StrCmp $R1 "" nexShowUninstallerDetailsRead
      SetDetailsPrint listonly
      DetailPrint "Nex: cleanup-log $R1"
      Goto nexShowUninstallerDetailsRead
    nexShowUninstallerDetailsClose:
      FileClose $R0
    nexShowUninstallerDetailsDone:
      Pop $R2
      Pop $R1
      Pop $R0
  FunctionEnd

  !macro preInit
    Call NexInitializeInstallerLog
  !macroend

  !macro customInit
    IfSilent nexInstallerInitSilent nexInstallerInitInteractive
    nexInstallerInitSilent:
      !insertmacro NexReportInstallerStage "installer-initialized mode=silent"
      Goto nexInstallerInitDone
    nexInstallerInitInteractive:
      !insertmacro NexReportInstallerStage "installer-initialized mode=interactive"
    nexInstallerInitDone:
  !macroend

  ; 这些宏由打包时的 electron-builder installSection.nsh 补丁按安装顺序调用。
  ; 只有阶段 marker 写入详情和日志，解压文件明细由 NSIS 的 File 命令在 listonly 模式输出。
  !macro customInstallSectionStarted
    !insertmacro NexReportInstallerStage "install-started"
  !macroend

  !macro customInstallCleanupStarted
    Call NexResetUninstallerLog
    !insertmacro NexReportInstallerStage "cleanup-started"
  !macroend

  !macro customInstallCleanupCompleted
    !insertmacro NexReportInstallerStage "cleanup-completed"
    Call NexShowUninstallerCleanupDetails
  !macroend

  !macro customInstallExtractStarted
    !insertmacro NexReportInstallerStage "extract-started"
  !macroend

  !macro customInstallExtractCompleted
    !insertmacro NexReportInstallerStage "extract-completed"
  !macroend

  !macro customInstallShortcutsStarted
    !insertmacro NexReportInstallerStage "shortcuts-started"
  !macroend

  !macro customInstallShortcutsCompleted
    !insertmacro NexReportInstallerStage "shortcuts-completed"
  !macroend

  Function NexDetectPreviousUninstallerCapabilities
    StrCpy $NexPreviousUninstallerSupportsManifest "0"
    ; manifest 是卸载器能力标记：存在即表示旧卸载器会按清单选择性删除。
    IfFileExists "$INSTDIR\${NEX_INSTALL_MANIFEST_NAME}" 0 nexPreviousUninstallerCapabilityCheckNested
      StrCpy $NexPreviousUninstallerSupportsManifest "1"
      Return

    nexPreviousUninstallerCapabilityCheckNested:
      ; assisted installer 的目录页会在后续 instfilesPre 才补上 APP_FILENAME 子目录，提前兼容两种形态。
      IfFileExists "$INSTDIR\${APP_FILENAME}\${NEX_INSTALL_MANIFEST_NAME}" 0 nexPreviousUninstallerCapabilityDone
        StrCpy $NexPreviousUninstallerSupportsManifest "1"

    nexPreviousUninstallerCapabilityDone:
  FunctionEnd

  !macro NexCleanupErrorMessage CHINESE ENGLISH
    ${If} $LANGUAGE == 2052
      MessageBox MB_OK|MB_ICONSTOP "${CHINESE}" /SD IDOK
    ${Else}
      MessageBox MB_OK|MB_ICONSTOP "${ENGLISH}" /SD IDOK
    ${EndIf}
  !macroend

  Var NexCleanupSource
  Var NexCleanupBackup
  Var NexCleanupMode
  Var NexCleanupFailed
  Var NexCleanupManifest
  Var NexCleanupManifestHasSelf

  ; 修复原因：PowerShell 或 Add-Type 被禁用时不能阻断已退出应用的更新；随包 helper 按路径验证句柄。
  !macro NexStopPreviousInstallProcesses
    InitPluginsDir
    SetOutPath "$PLUGINSDIR"
    ; CR-01 修复原因：普通 NSIS 只有 APP_64/APP_ARM64（压缩包路径），没有 APP_DIR_*；
    ; helper 来源由文件顶层的生成 define 提供，缺失在此明确失败，不得回退压缩包宏。
    !ifndef NEX_INSTALLER_PROCESS_HELPER
      !error "Installer process helper requires NEX_INSTALLER_PROCESS_HELPER or generated define"
    !endif
    File /oname=nex-installer-process-helper.exe "${NEX_INSTALLER_PROCESS_HELPER}"
    nsExec::ExecToStack /TIMEOUT=30000 `"$PLUGINSDIR\nex-installer-process-helper.exe" "$installationDir\${APP_EXECUTABLE_FILENAME}"`
    Pop $R1
    Pop $R2
    StrCmp $R1 "0" nexPreviousProcessesStopped
    !insertmacro NexReportInstallerStage "process-cleanup-failed exit-code=$R1 install-dir=$installationDir details=$R2"
    !insertmacro NexCleanupErrorMessage "无法验证或关闭旧版本进程，安装已停止。请关闭 Nex 后重试。进程信息：$R2" "Cannot verify or close previous-version processes. Setup stopped. Close Nex and retry. Process details: $R2"
    SetErrorLevel 2
    Quit
    nexPreviousProcessesStopped:
      !insertmacro NexReportInstallerStage "process-cleanup-completed install-dir=$installationDir details=$R2"
  !macroend

  ; 旧卸载器已删除的文件不会自行恢复。新安装器在调用旧代码前尽力保存受管文件，
  ; 只有完整备份才能用于清理失败后的恢复；备份失败不阻断安装。
  ; 自定义 include 先于 common.nsh；延迟展开，确保卸载器名称已定义，避免正式打包 warning 6000。
  !macro NexDefineCleanupSnapshot
  Function NexCleanupSnapshot
    Push $R0
    Push $R1
    Push $R2
    Push $R3
    Push $R4
    Push $R5
    StrCpy $R5 ""
    StrCpy $NexCleanupFailed "0"
    StrCmp $NexCleanupMode "restore" nexSnapshotRead
    StrCpy $NexCleanupBackup ""
    StrCpy $NexCleanupManifestHasSelf "0"
    IfFileExists "$NexCleanupSource\${NEX_INSTALL_MANIFEST_NAME}" 0 nexSnapshotDone
    ${GetParent} $NexCleanupSource $R0
    ClearErrors
    GetTempFileName $NexCleanupBackup $R0
    IfErrors nexSnapshotFailed
    Delete "$NexCleanupBackup"
    CreateDirectory "$NexCleanupBackup"
    IfErrors nexSnapshotFailed
    System::Call 'kernel32::CopyFileW(w "$NexCleanupSource\${NEX_INSTALL_MANIFEST_NAME}", w "$NexCleanupBackup\${NEX_INSTALL_MANIFEST_NAME}", i 1) i.R3 ?e'
    Pop $R4
    StrCmp $R3 "0" nexSnapshotFailed
    GetTempFileName $NexCleanupManifest "$NexCleanupBackup"
    IfErrors nexSnapshotFailed
    FileOpen $R5 "$NexCleanupManifest" w
    IfErrors nexSnapshotFailed

    nexSnapshotRead:
      StrCmp $NexCleanupBackup "" nexSnapshotDone
      ClearErrors
      FileOpen $R0 "$NexCleanupBackup\${NEX_INSTALL_MANIFEST_NAME}" r
      IfErrors nexSnapshotFailed
    nexSnapshotNext:
      ClearErrors
      FileRead $R0 $R1
      IfErrors nexSnapshotClose
      StrCpy $R1 $R1 -2
      StrCmp $R1 "" nexSnapshotNext
      StrCmp $NexCleanupMode "restore" nexSnapshotValidateEntry
      StrCmp $R1 "${NEX_INSTALL_MANIFEST_NAME}" nexSnapshotSelfEntry
      FileWrite $R5 "$R1$\r$\n"
      IfErrors nexSnapshotFailedClose
      Goto nexSnapshotValidateEntry
    nexSnapshotSelfEntry:
      StrCpy $NexCleanupManifestHasSelf "1"
      Goto nexSnapshotNext
    nexSnapshotFailedClose:
      StrCpy $NexCleanupFailed "1"
      Goto nexSnapshotClose
    nexSnapshotValidateEntry:
      ; 只接受清单中规范的相对路径，拒绝越界、通配符和卸载器自身。
      StrCpy $R2 $R1 1
      StrCmp $R2 "\" nexSnapshotNext
      StrCmp $R2 "/" nexSnapshotNext
      StrCpy $R2 $R1 2
      StrCmp $R2 ".." nexSnapshotNext
      StrCmp $R1 "${UNINSTALL_FILENAME}" nexSnapshotNext
      ; 清单已在循环前单独备份；自引用时再次排他复制会因目标存在而中止更新。
      StrCmp $R1 "${NEX_INSTALL_MANIFEST_NAME}" nexSnapshotNext
      ; Windows NSIS 对已删除的目标返回空路径；恢复时校验仍存在的备份，不能跳过待补回文件。
      StrCpy $R3 "$NexCleanupSource"
      StrCmp $NexCleanupMode "restore" 0 +2
        StrCpy $R3 "$NexCleanupBackup"
      GetFullPathName $R2 "$R3\$R1"
      StrCmp $R2 "$R3\$R1" 0 nexSnapshotNext
      StrCpy $R2 0
    nexSnapshotValidate:
      StrCpy $R3 $R1 1 $R2
      StrCmp $R3 "" nexSnapshotCopy
      StrCmp $R3 "*" nexSnapshotInvalidPath
      StrCmp $R3 "?" nexSnapshotInvalidPath
      StrCmp $R3 ":" nexSnapshotInvalidPath
      IntOp $R2 $R2 + 1
      Goto nexSnapshotValidate
    nexSnapshotInvalidPath:
      StrCpy $NexCleanupFailed "1"
      Goto nexSnapshotClose
    nexSnapshotCopy:
      StrCmp $NexCleanupMode "restore" nexSnapshotRestore
      IfFileExists "$NexCleanupSource\$R1" 0 nexSnapshotNext
      ${GetParent} "$NexCleanupBackup\$R1" $R2
      CreateDirectory "$R2"
      System::Call 'kernel32::CopyFileW(w "$NexCleanupSource\$R1", w "$NexCleanupBackup\$R1", i 1) i.R3 ?e'
      Pop $R4
      Goto nexSnapshotCopyResult
    nexSnapshotRestore:
      IfFileExists "$NexCleanupBackup\$R1" 0 nexSnapshotNext
      IfFileExists "$NexCleanupSource\$R1" 0 nexSnapshotRestoreMissing
      ; 同名目录不能充当恢复后的文件，否则会误报已恢复主程序。
      System::Call 'kernel32::GetFileAttributesW(w "$NexCleanupSource\$R1") i.R3'
      IntOp $R3 $R3 & 0x10
      StrCmp $R3 0 nexSnapshotNext
      StrCpy $NexCleanupFailed "1"
      Goto nexSnapshotNext
    nexSnapshotRestoreMissing:
      ${GetParent} "$NexCleanupSource\$R1" $R2
      CreateDirectory "$R2"
      System::Call 'kernel32::CopyFileW(w "$NexCleanupBackup\$R1", w "$NexCleanupSource\$R1", i 1) i.R3 ?e'
      Pop $R4
    nexSnapshotCopyResult:
      ; System 的 ?e 在 CopyFileW 返回时保存 Win32 错误，避免插件后续调用覆盖 GetLastError。
      StrCmp $R3 "0" 0 nexSnapshotNext
      !insertmacro NexReportInstallerStage "cleanup-$NexCleanupMode-file-failed path=$R1 win32-error=$R4"
      StrCpy $NexCleanupFailed "1"
      StrCmp $NexCleanupMode "restore" nexSnapshotNext
    nexSnapshotClose:
      FileClose $R0
      StrCmp $NexCleanupMode "restore" nexSnapshotRestoreManifest
      FileClose $R5
      StrCpy $R5 ""
      StrCmp $NexCleanupFailed "1" nexSnapshotDone
      StrCmp $NexCleanupManifestHasSelf "1" 0 nexSnapshotDone
      ; 旧卸载器也会删除打开中的清单。完整备份后，仅原子移除自引用项供旧代码读取。
      System::Call 'kernel32::MoveFileExW(w "$NexCleanupManifest", w "$NexCleanupSource\${NEX_INSTALL_MANIFEST_NAME}", i 9) i.R3 ?e'
      Pop $R4
      StrCmp $R3 "0" nexSnapshotFailed nexSnapshotDone
    nexSnapshotRestoreManifest:
      ; 旧清单可能仍存在但已去掉自引用；失败回滚须恢复原始字节，而非仅补不存在的文件。
      System::Call 'kernel32::CopyFileW(w "$NexCleanupBackup\${NEX_INSTALL_MANIFEST_NAME}", w "$NexCleanupSource\${NEX_INSTALL_MANIFEST_NAME}", i 0) i.R3 ?e'
      Pop $R4
      StrCmp $R3 "0" nexSnapshotFailed nexSnapshotDone
    nexSnapshotFailed:
      StrCpy $NexCleanupFailed "1"
    nexSnapshotDone:
      StrCmp $R5 "" +2
        FileClose $R5
      !insertmacro NexReportInstallerStage "cleanup-$NexCleanupMode-completed failed=$NexCleanupFailed backup=$NexCleanupBackup"
      Pop $R5
      Pop $R4
      Pop $R3
      Pop $R2
      Pop $R1
      Pop $R0
  FunctionEnd
  !macroend

  !macro customOldUninstallPrepare
    StrCpy $NexCleanupSource "$installationDir"
    !insertmacro NexStopPreviousInstallProcesses
    StrCpy $NexCleanupMode "backup"
    Call NexCleanupSnapshot
    ${If} $NexCleanupFailed == "1"
      ; 修复原因：备份是可选保护；失败时弃用半份备份，避免旧卸载失败后误用它恢复。
      !insertmacro NexReportInstallerStage "cleanup-backup-skipped path=$NexCleanupBackup"
      StrCmp $NexCleanupBackup "" nexPreviousBackupCleanupDone
      ClearErrors
      RMDir /r "$NexCleanupBackup"
      IfErrors nexPreviousBackupCleanupFailed
      Goto nexPreviousBackupCleanupDone
      nexPreviousBackupCleanupFailed:
        !insertmacro NexReportInstallerStage "cleanup-backup-delete-failed path=$NexCleanupBackup"
      nexPreviousBackupCleanupDone:
      StrCpy $NexCleanupBackup ""
    ${EndIf}
  !macroend

  !macro customOldUninstallRetryFailed
    ; 上游把任意卸载非零都显示成应用未关闭；实际进程检查仍由 CHECK_APP_RUNNING 负责。
    ; 备份失败被弃用后没有可恢复的完整备份；提示必须与能否恢复一致，不能在无备份时承诺恢复。
    StrCmp $NexCleanupBackup "" nexRetryExhaustedNoBackup
    !insertmacro NexReportInstallerStage "cleanup-retries-exhausted exit-code=$R0 recoverable=1"
    ${If} $LANGUAGE == 2052
      MessageBox MB_RETRYCANCEL|MB_ICONEXCLAMATION "旧版本清理失败（错误码 $R0）。请排除文件占用或权限问题后重试，或取消以恢复旧版本。日志：$NexInstallerLogPath" /SD IDCANCEL IDRETRY OneMoreAttempt
    ${Else}
      MessageBox MB_RETRYCANCEL|MB_ICONEXCLAMATION "Previous version cleanup failed (exit code $R0). Resolve file locks or permissions and retry, or cancel to restore the previous version. Log: $NexInstallerLogPath" /SD IDCANCEL IDRETRY OneMoreAttempt
    ${EndIf}
    Goto nexRetryExhaustedDone
    nexRetryExhaustedNoBackup:
    !insertmacro NexReportInstallerStage "cleanup-retries-exhausted exit-code=$R0 recoverable=0"
    ${If} $LANGUAGE == 2052
      MessageBox MB_RETRYCANCEL|MB_ICONEXCLAMATION "旧版本清理失败（错误码 $R0）。本次更新没有完整备份：排除占用后可重试，取消将退出安装且无法自动恢复旧版本文件。日志：$NexInstallerLogPath" /SD IDCANCEL IDRETRY OneMoreAttempt
    ${Else}
      MessageBox MB_RETRYCANCEL|MB_ICONEXCLAMATION "Previous version cleanup failed (exit code $R0). No complete backup exists for this update: retry after resolving file locks, or cancel to exit setup without automatic restore. Log: $NexInstallerLogPath" /SD IDCANCEL IDRETRY OneMoreAttempt
    ${EndIf}
    nexRetryExhaustedDone:
  !macroend

  Function NexFinishOldUninstall
    ${If} $R0 != 0
      ; 修复原因：没有完整备份时不能用半份文件恢复，旧卸载失败仍由原流程报告。
      StrCmp $NexCleanupBackup "" nexFinishOldUninstallDone
      StrCpy $NexCleanupMode "restore"
      Call NexCleanupSnapshot
      ${If} $NexCleanupFailed == "1"
        !insertmacro NexCleanupErrorMessage "旧版本文件恢复失败，备份已保留：$NexCleanupBackup。详细日志：$NexInstallerLogPath" "Cannot restore the previous version. Backup retained: $NexCleanupBackup. Log: $NexInstallerLogPath"
        Return
      ${EndIf}
    ${EndIf}
    StrCmp $NexCleanupBackup "" nexFinishOldUninstallDone
    ClearErrors
    RMDir /r "$NexCleanupBackup"
    IfErrors nexFinishOldUninstallBackupDeleteFailed
    StrCpy $NexCleanupBackup ""
    Goto nexFinishOldUninstallDone
    nexFinishOldUninstallBackupDeleteFailed:
      ; 修复原因：备份清理故障不影响旧卸载结果及后续新版本安装，只保留路径供排查。
      !insertmacro NexReportInstallerStage "cleanup-backup-delete-failed path=$NexCleanupBackup"
    nexFinishOldUninstallDone:
  FunctionEnd

  !macro customUnInstallCheck
    Call NexFinishOldUninstall
    ; handleUninstallResult 会把旧卸载器的退出码放在 $R0；失败时显示清理诊断，
    ; 不再复用 appCannotBeClosed（该文案只适用于进程占用）。
    ${if} $R0 != 0
      ; 修复原因：静默自动更新无人值守，未设置 /SD 的模态框会一直等待用户点击，
      ; 使明确的退出码无法返回 electron-updater。静默时自动采用 IDOK，交互时仍显示提示。
      SetDetailsPrint listonly
      DetailPrint "Nex: cleanup-failed exit-code=$R0"
      Call NexShowUninstallerCleanupDetails
      ; 最终错误也沿用安装器语言，避免英文 Retry/Cancel 之后固定切回中文。
      !insertmacro NexCleanupErrorMessage "旧版本清理失败（错误码 $R0）。可能是文件被占用、权限不足或磁盘空间不足。详细日志：${NEX_UNINSTALLER_LOG_PATH}" "Previous version cleanup failed (exit code $R0). Files may be in use, permissions may be insufficient, or disk space may be low. Log: ${NEX_UNINSTALLER_LOG_PATH}"
      SetErrorLevel 2
      Quit
    ${endif}
  !macroend

  !macro customUnInstallCheckCurrentUser
    ; per-machine 安装切换到 HKCU 旧版本时，electron-builder 会走另一条 hook；
    ; 复用同一诊断，避免同一个清理失败因注册表根键不同又退回默认文案。
    !insertmacro customUnInstallCheck
  !macroend
!endif

!define NEX_INSTALL_DIR_BACK_BUTTON_WIDTH 180

!macro customHeader
  !ifndef BUILD_UNINSTALLER
    !insertmacro NexDefineCleanupSnapshot
    ; 修复原因：异步生成的 header 可能先 include 本文件，再注册 UAC 插件目录。
    ; 在 customHeader 展开函数，确保插件已注册；preInit 仍调用同一函数和真实 UAC 判据。
    Function NexInitializeInstallerLog
      Push $R0
      Push $R1
      Push $R2
      StrCpy $NexInstallerLogUnavailable ""
      ${If} ${NEX_INSTALLER_IS_ELEVATED_INNER}
        StrCpy $NexInstallerProcessRole "elevated-inner"
        StrCpy $NexInstallerLogPath "${NEX_INSTALLER_ELEVATED_LOG_PATH}"
      ${Else}
        StrCpy $NexInstallerProcessRole "outer"
        StrCpy $R0 $CMDLINE
        ClearErrors
        ${GetOptions} $R0 "/LOG=" $R1
        IfErrors nexInstallerLogUseDefault
        StrCmp $R1 "" nexInstallerLogUseDefault
        StrCpy $NexInstallerLogPath $R1
        Goto nexInstallerLogPathReady
        nexInstallerLogUseDefault:
          StrCpy $NexInstallerLogPath "${NEX_INSTALLER_DEFAULT_LOG_PATH}"
        nexInstallerLogPathReady:
          ${GetParent} $NexInstallerLogPath $R2
          StrCmp $R2 "" nexInstallerLogInitialized
          CreateDirectory "$R2"
      ${EndIf}
      nexInstallerLogInitialized:
        !insertmacro NexReportInstallerStage "installer-process-started role=$NexInstallerProcessRole"
      Pop $R2
      Pop $R1
      Pop $R0
    FunctionEnd

    ; 修复原因：electron-builder 的 common.nsh 先设置 ShowInstDetails nevershow；
    ; hide 在该模板组合下仍可能留下空白列表且没有可展开入口，因此直接常显阶段详情。
    ShowInstDetails show
    !ifdef allowToChangeInstallationDirectory
      ; 修复原因：electron-builder 已在 assistedInstaller.nsh 中用该开关生成安装目录页面，
      ; 但 installUtil.nsh 随后还会用它禁止无 --updated 的手动覆盖保留快捷方式，导致旧卸载器
      ; 调用 UninstShortcut 注销开始菜单固定项。页面生成后撤掉开关，让自动更新和手动覆盖
      ; 在同一安装目录覆盖时都通过 KeepShortcuts 保留同一个 .lnk。
      !undef allowToChangeInstallationDirectory
    !endif
  !endif
!macroend

!ifndef BUILD_UNINSTALLER
  ; 修复原因：electron-builder 会先编译卸载器，但快捷方式目标读取只在安装更新流程中调用。
  ; 若把函数带入卸载器，NSIS 会产生 6010 未引用告警，并在 /WX 下直接中断 Windows CI。
  Function NexReadShortcutTarget
    Exch $R9
    Push $R1
    Push $R2

    StrCpy $R2 ""
    System::Call 'Kernel32::SetEnvironmentVariableW(w "NEX_SHORTCUT_PATH", w "$R9") i.R1'
    StrCmp $R1 "0" nexReadShortcutTargetDone 0

    nsExec::ExecToStack /TIMEOUT=5000 `"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -NonInteractive -Command "[Console]::Out.Write(([Activator]::CreateInstance([type]::GetTypeFromProgID('WScript.Shell'))).CreateShortcut([Environment]::GetEnvironmentVariable('NEX_SHORTCUT_PATH')).TargetPath)"`
    Pop $R1
    Pop $R2
    StrCmp $R1 "0" nexReadShortcutTargetDone 0
    StrCpy $R2 ""

    nexReadShortcutTargetDone:
      System::Call 'Kernel32::SetEnvironmentVariableW(w "NEX_SHORTCUT_PATH", p 0) i.R1'
      StrCpy $R9 "$R2"
      Pop $R2
      Pop $R1
      Exch $R9
  FunctionEnd
!endif

!macro NexRepairShortcutIfNeeded SHORTCUT_PATH LABEL_PREFIX
  ${if} ${FileExists} "${SHORTCUT_PATH}"
    Push "${SHORTCUT_PATH}"
    Call NexReadShortcutTarget
    Pop $R0
    StrCmp $R0 "$appExe" ${LABEL_PREFIX}Done 0

    ; 修复原因：历史版本可能留下指向已移动 exe 的 .lnk，但无条件覆盖正确快捷方式会让
    ; 部分 Windows 11 丢失“所有应用”索引或用户固定关系，因此只修复目标不一致的项。
    ClearErrors
    CreateShortCut "${SHORTCUT_PATH}" "$appExe" "" "$appExe" 0 "" "" "${APP_DESCRIPTION}"
    IfErrors ${LABEL_PREFIX}Failed ${LABEL_PREFIX}Succeeded
    ${LABEL_PREFIX}Failed:
      DetailPrint "Unable to repair shortcut: ${SHORTCUT_PATH}"
      ClearErrors
      Goto ${LABEL_PREFIX}Done
    ${LABEL_PREFIX}Succeeded:
      WinShell::SetLnkAUMI "${SHORTCUT_PATH}" "${APP_ID}"
      ; 修复后的 .lnk 必须在最后一次写入后通知 Shell，避免开始菜单继续使用旧索引。
      System::Call 'Shell32::SHChangeNotify(i 0x00002000, i 0x0005, w "${SHORTCUT_PATH}", p 0)'
    ${LABEL_PREFIX}Done:
  ${endIf}
!macroend

!macro customInstall
  !ifndef BUILD_UNINSTALLER
    !insertmacro NexReportInstallerStage "install-finalization-started"
  !endif
  ${if} ${isUpdated}
  ${orIf} $keepShortcuts == "true"
    !ifndef DO_NOT_CREATE_START_MENU_SHORTCUT
      !insertmacro NexRepairShortcutIfNeeded "$newStartMenuLink" nexStartMenuShortcutRepair
    !endif

    !ifndef DO_NOT_CREATE_DESKTOP_SHORTCUT
      !insertmacro NexRepairShortcutIfNeeded "$newDesktopLink" nexDesktopShortcutRepair
    !endif
  ${endIf}

  ; 修复原因：手动覆盖没有 --updated，继承旧快捷方式时仍需检查目标；首次安装没有旧项，
  ; 不应额外启动 PowerShell。用户已删除的快捷方式也不会重建。
  ; assisted installer 完成页始终直接运行本次安装落盘的 exe。
  ; 修复原因：安装信息键的 InstallLocation 用于旧卸载器路径解析；卸载注册表键也写入相同值，
  ; 避免 Windows 应用列表中的卸载项长期为空，并为后续迁移提供确定目录。
  WriteRegStr SHELL_CONTEXT "${UNINSTALL_REGISTRY_KEY}" InstallLocation "$INSTDIR"
  !ifdef UNINSTALL_REGISTRY_KEY_2
    WriteRegStr SHELL_CONTEXT "${UNINSTALL_REGISTRY_KEY_2}" InstallLocation "$INSTDIR"
  !endif
  StrCpy $launchLink "$appExe"
  !ifndef BUILD_UNINSTALLER
    !insertmacro NexReportInstallerStage "install-completed"
  !endif
!macroend

!macro customPageAfterChangeDir
  Function NexResizeInstallDirBackButton
    GetDlgItem $1 $HWNDPARENT 3
    StrCmp $1 0 nexResizeInstallDirBackButtonDone 0

    System::Call "*(i 0, i 0, i 0, i 0) p.r2"
    StrCmp $2 0 nexResizeInstallDirBackButtonDone 0
    System::Call "user32::GetWindowRect(p r1, p r2)"
    System::Call "user32::MapWindowPoints(p 0, p $HWNDPARENT, p r2, i 2)"
    System::Call "*$2(i.r3,i.r4,i.r5,i.r6)"
    System::Free $2

    IntOp $7 $5 - $3
    IntOp $8 $6 - $4
    IntCmp $7 ${NEX_INSTALL_DIR_BACK_BUTTON_WIDTH} nexResizeInstallDirBackButtonDone nexResizeInstallDirBackButtonResize nexResizeInstallDirBackButtonDone

    nexResizeInstallDirBackButtonResize:
      ; 修复原因：阻断页把“上一步”改成中文动作文案，NSIS 默认按钮宽度可能裁掉文字。
      ; 保持右边缘不动向左扩宽，避免和右侧“安装/取消”按钮重叠。
      IntOp $3 $5 - ${NEX_INSTALL_DIR_BACK_BUTTON_WIDTH}
      System::Call "user32::MoveWindow(p r1, i r3, i r4, i ${NEX_INSTALL_DIR_BACK_BUTTON_WIDTH}, i r8, i 1)"

    nexResizeInstallDirBackButtonDone:
  FunctionEnd

  Function NexFindNestedDataDir
    Exch $R9
    Push $0
    Push $1

    StrCpy $R2 ""

    IfFileExists "$R9\.nex\*.*" 0 +2
      StrCpy $R2 "$R9\.nex"
    StrCmp $R2 "" 0 nexFindNestedDataDirDone
    IfFileExists "$R9\.nex" 0 nexFindNestedDataDirListChildren
      StrCpy $R2 "$R9\.nex"
    StrCmp $R2 "" 0 nexFindNestedDataDirDone

    nexFindNestedDataDirListChildren:
      FindFirst $0 $1 "$R9\*"
      IfErrors nexFindNestedDataDirDone

    nexFindNestedDataDirNext:
      StrCmp $1 "" nexFindNestedDataDirClose
      StrCmp $1 "." nexFindNestedDataDirContinue
      StrCmp $1 ".." nexFindNestedDataDirContinue
      IfFileExists "$R9\$1\*.*" 0 nexFindNestedDataDirContinue
        Push "$R9\$1"
        Call NexFindNestedDataDir
        StrCmp $R2 "" nexFindNestedDataDirContinue nexFindNestedDataDirClose

    nexFindNestedDataDirContinue:
      FindNext $0 $1
      IfErrors nexFindNestedDataDirClose
      Goto nexFindNestedDataDirNext

    nexFindNestedDataDirClose:
      FindClose $0

    nexFindNestedDataDirDone:
      Pop $1
      Pop $0
      Pop $R9
  FunctionEnd

  Function NexBlockInstallDirContainsData
    Call NexDetectPreviousUninstallerCapabilities
    StrCmp $NexPreviousUninstallerSupportsManifest "1" nexInstallDirDataBlockSkip

    ; Bugfix: 用户可能把数据存储目录放进安装目录，Windows 更新覆盖安装目录时会清掉 .nex。
    ; assisted installer 会把不含应用名的选择目录补成 "$INSTDIR\${APP_FILENAME}"，所以这里按相同规则计算最终安装目录。
    ${StrContains} $R1 "${APP_FILENAME}" "$INSTDIR"
    StrCmp $R1 "" 0 nexInstallDirDataBlockUseSelectedDir
    StrCpy $R0 "$INSTDIR\${APP_FILENAME}"
    Goto nexInstallDirDataBlockCheckDir

    nexInstallDirDataBlockUseSelectedDir:
      StrCpy $R0 "$INSTDIR"

    nexInstallDirDataBlockCheckDir:
      ; 修复原因：旧阻断只检查最终安装目录直属的 .nex，漏掉 data\.nex 等子目录数据。
      ; 安装器覆盖安装时会管理整个安装目录树，递归命中任意 .nex 都必须阻断。
      Push "$R0"
      Call NexFindNestedDataDir
      StrCmp $R2 "" nexInstallDirDataBlockSkip nexInstallDirDataBlockFound

    nexInstallDirDataBlockFound:
      IfSilent nexInstallDirDataBlockSilent

      !insertmacro MUI_HEADER_TEXT "需要修改安装目录" "当前安装目录或其子目录包含 Nex 数据目录"
      nsDialogs::Create 1018
      Pop $0
      StrCmp $0 error nexInstallDirDataBlockDialogFailed 0

      ${NSD_CreateLabel} 0u 0u 300u 44u "检测到该安装目录或其子目录中存在 .nex 数据目录：$\r$\n$R2"
      Pop $1
      ${NSD_CreateLabel} 0u 54u 300u 70u "为避免历史会话和配置被安装器清理，请返回上一步选择其他安装目录。$\r$\n$\r$\n当前目录不能继续安装。"
      Pop $1

      GetDlgItem $1 $HWNDPARENT 1
      EnableWindow $1 0
      GetDlgItem $1 $HWNDPARENT 3
      EnableWindow $1 1
      SendMessage $1 ${WM_SETTEXT} 0 "STR:重选目录"
      Call NexResizeInstallDirBackButton

      nsDialogs::Show
      Return

    nexInstallDirDataBlockDialogFailed:
      MessageBox MB_OK|MB_ICONSTOP "检测到安装目录或其子目录中存在 .nex 数据目录，安装已停止。请重新运行安装器并选择其他安装目录。"
      SetErrorLevel 1
      Quit

    nexInstallDirDataBlockSilent:
      SetErrorLevel 1
      Quit

    nexInstallDirDataBlockSkip:
      Abort
  FunctionEnd

  Function NexBlockInstallDirContainsDataLeave
    ; 修复原因：阻断页的下一步按钮已禁用，但自动化或系统快捷键仍可能触发下一页。
    ; leave 回调只处理继续前进的路径，这里强制留在当前页，确保用户只能返回修改安装目录。
    Abort
  FunctionEnd

  Page custom NexBlockInstallDirContainsData NexBlockInstallDirContainsDataLeave
!macroend
