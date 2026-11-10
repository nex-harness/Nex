import type { Event, IDisposable } from "@nex/rpc";
import type { NexProtocolMessage } from "@nex/shared";

export type NexProtocolTransportKind = "stdio" | "websocket" | "memory";

export interface NexProtocolTransportClosedEvent {
  code?: number | null;
  signal?: NodeJS.Signals | null;
  reason?: string;
}

export interface NexProtocolTransport extends IDisposable {
  readonly kind: NexProtocolTransportKind;
  readonly onMessage: Event<NexProtocolMessage>;
  readonly onClose: Event<NexProtocolTransportClosedEvent>;
  send(message: NexProtocolMessage): Promise<void>;
  disposeAndWait?(): Promise<void>;
}
