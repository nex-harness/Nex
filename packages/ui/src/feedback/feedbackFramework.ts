import {
  DEFAULT_FEEDBACK_TICKET_FRAMEWORK,
  type NexProvider,
  type FeedbackTicketFramework,
} from "@nex/shared";

export function inferFeedbackFrameworkFromAgentProvider(
  _provider: NexProvider | null | undefined,
): FeedbackTicketFramework {
  return DEFAULT_FEEDBACK_TICKET_FRAMEWORK;
}
