export interface HelloMessage {
  type: "nex-hello";
  version: string;
  platform: string;
  arch: string;
  pid: number;
}

export interface HelloAckMessage {
  type: "nex-hello-ack";
  version: string;
  clientId: string;
}
