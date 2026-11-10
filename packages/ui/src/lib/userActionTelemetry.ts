/**
 * 用户操作观测的无副作用占位实现。
 *
 * Nex 不上报遥测：这里不再构造 renderer action trace span、不排队、不发 batch。
 * 调用点保留 `startUserAction` / `runUserAction` 形状，避免业务代码为了观测层反复改签名。
 */

export type UserActionTrigger = string;
export type UserActionResultSource = string;

export interface StartUserActionInput {
  featureId: string;
  action: string;
  trigger: UserActionTrigger;
  surface?: string;
  timeoutMs?: number;
  workspaceKind?: string;
  remoteKind?: string;
  automationKind?: string;
}

export interface UserActionResult {
  resultSource?: UserActionResultSource;
  failureStage?: string;
  stateAfter?: string;
  configured?: boolean;
  requiresRestart?: boolean;
  sectionId?: string;
  valueAfter?: string;
  admissionResult?: string;
}

export interface UserActionFailure extends UserActionResult {
  failureStage: string;
}

export interface UserActionHandle {
  complete(result?: UserActionResult): void;
  fail(failure: UserActionFailure): void;
  reject(result?: UserActionResult): void;
  cancel(): void;
  noop(): void;
}

const NOOP_ACTION_HANDLE: UserActionHandle = {
  complete() {},
  fail() {},
  reject() {},
  cancel() {},
  noop() {},
};

export function startUserAction(_input: StartUserActionInput): UserActionHandle {
  return NOOP_ACTION_HANDLE;
}

export async function runUserActionAsync<T>(options: {
  input: StartUserActionInput;
  operation: () => Promise<T>;
  completed?: UserActionResult | ((value: T) => UserActionResult);
  failureStage: string;
}): Promise<T> {
  return await options.operation();
}

export function runUserAction<T>(options: {
  input: StartUserActionInput;
  operation: () => T;
  completed?: UserActionResult | ((value: T) => UserActionResult);
  failureStage: string;
}): T {
  return options.operation();
}
