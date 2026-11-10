import { logger } from "./logger.js";
import { initializeCrashCapture, type CrashCapturePaths } from "./desktopCrashCapture.js";

// 须在窗口创建前完成：先由 desktopEarlyDataBaseDirBootstrap 注入 dataBaseDir，再配置 crashDumps。
// 无远端 crash 上报，remoteCrashReporterEnabled=false，启动本地 crashReporter 留档。
export const crashCapturePaths: CrashCapturePaths = initializeCrashCapture(logger, false);
