import type { Logger } from "../../logger.js";
import type { AttachmentRegistry } from "../../attachments/registry.js";
import { AttachmentPromptTable } from "./piAttachmentOps.js";

/**
 * `AttachmentPromptTable` bound to one engine (spec A): the per-session prompt input table,
 * `nex.attachments` markers and the claim that turns a prompt into the user row's facts.
 */
export function createAttachmentOps(deps: {
  host: AttachmentRegistry;
  logger: Logger;
}): AttachmentPromptTable {
  return new AttachmentPromptTable({ host: deps.host, logger: deps.logger });
}
