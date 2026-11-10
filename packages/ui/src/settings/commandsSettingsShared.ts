import {
  NEX_AGENT_PROVIDER,
  NEX_COMMAND_AGENT_SOURCES,
  type NexProvider,
  type CommandAgentSource,
} from "@nex/shared";

export const COMMAND_AGENT_SOURCES = [
  ...NEX_COMMAND_AGENT_SOURCES,
] as const satisfies readonly CommandAgentSource[];

export const COMMAND_AGENT_SOURCE_META: Record<
  CommandAgentSource,
  {
    labelId: string;
    provider: NexProvider;
    pathHint: string;
  }
> = {
  nexAgent: {
    labelId: "settings.commands.source.nexAgent",
    pathHint: "~/.nex/commands",
    provider: NEX_AGENT_PROVIDER,
  },
};
