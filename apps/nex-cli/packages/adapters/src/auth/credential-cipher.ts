import {
  createLegacyCredentialCipher,
  type CredentialCipher,
} from "@nex/shared/credential-security";
export type NexCredentialCipher = CredentialCipher;
export interface NexCredentialCipherOptions {
  env?: Record<string, string | undefined>;
}
/** 仅兼容旧格式；正式凭据仓库默认使用系统安全存储。 */
export function createNexCredentialCipher(options: NexCredentialCipherOptions = {}) {
  return createLegacyCredentialCipher(options.env);
}
export function isEncryptedNexCredentialValue(value: string): boolean {
  return value.startsWith("enc:");
}
