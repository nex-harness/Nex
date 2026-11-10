/**
 * 会话创建来源的业务枚举。
 *
 * Nex 不上报遥测：这里只保留 app/UI 需要传给 createSession 的入参标识，
 * 原 session_create 上报 schema 与 payload 类型已随上报链路删除。
 */
export type SessionCreateSource = "group" | "project" | "session";
export type SessionCreateClientKind = "desktop" | "mobile" | "web";
