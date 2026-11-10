/**
 * Nex 无账号体系：Coding Plan 额度横幅已移除，原先的 quota 一致性断言不再适用。
 * 保留同名导出让遥测 parity 调用方继续编译，返回空断言集。
 */
export function runQuotaParityStateAssertions(): string[] {
  return [];
}

/** Nex 无账号体系：请求验证一致性断言同样随账号能力移除。 */
export function runRequestVerificationParityAssertions(): string[] {
  return [];
}
