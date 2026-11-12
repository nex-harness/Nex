import { NEX_GITHUB_ISSUES_NEW_URL, type IPlatformService } from "@nex/shared";
import type { IntlInstance } from "@/i18n/IntlProvider.js";
import { runExportLogsAction } from "@/lib/exportLogsAction.js";
import { NEX_PRODUCT_DOCS_URL } from "@/lib/productDocs.js";

export interface HelpMenuActionHandlers {
  openIssueReport: () => Promise<void>;
  openProductDocs: () => void;
  exportLogs: () => void;
}

export function createHelpMenuActionHandlers({
  platform,
  intl,
}: {
  platform: Pick<IPlatformService, "captureWindowScreenshot" | "exportLogs" | "openExternal">;
  intl: IntlInstance;
}): HelpMenuActionHandlers {
  return {
    openIssueReport: async () => {
      // 产品决定：不再内置工单系统，Help → Feedback 直接打开 GitHub Issues 新建页。
      platform.openExternal(NEX_GITHUB_ISSUES_NEW_URL);
    },
    openProductDocs: () => {
      platform.openExternal(NEX_PRODUCT_DOCS_URL);
    },
    exportLogs: () => {
      void runExportLogsAction(platform, intl);
    },
  };
}
