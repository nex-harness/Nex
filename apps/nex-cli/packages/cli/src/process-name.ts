export const CLI_COMMAND_NAME = "nex";
export const CLI_PROCESS_NAME = "nex-cli";

interface ProcessTitleTarget {
  title: string;
}

export const setCliProcessTitle = (
  target: ProcessTitleTarget = process,
): void => {
  target.title = CLI_PROCESS_NAME;
};
