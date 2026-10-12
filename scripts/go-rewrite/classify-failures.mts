/* eslint-disable max-lines -- 向量生成脚本：一个文件里同时给出输入与真实分类结果，拆开会让用例与其期望值分居两个文件。 */
// 扩展 Go 侧 model client 的失败分类对照向量。
//
// gen-model-vectors.mts 已经用真实 classifyModelFailure 覆盖了 48 个输入；本脚本在其之上补充
// 更容易在重构中被改坏的分支：
//   1. ProviderBusinessError 直接抛出（模型执行层的业务错误，failure-classifier.ts 的第一条分支）；
//   2. APICallError.data.error / responseBody 里的业务码（AI SDK 解析后的结构）；
//   3. Retry-After 的各种写法（毫秒优先、小数、负数、HTTP header 大小写、x-should-retry=0）；
//   4. 超时码 / 网络码 / TLS / proxy / abort 在 cause 链上的传播；
//   5. 超窗文案短语（contextlimit.go 的每一句规则）。
//
// 每个输入都走真实的 classifyModelFailure，期望值完全来自 Node；Go 侧
// go/internal/modelclient/failure_test.go 逐字段比对两个向量文件。
import { writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { APICallError } from "ai";
import { classifyModelFailure } from "../../apps/nex-cli/packages/adapters/src/model/failure-classifier.ts";
import { ProviderBusinessError } from "../../apps/nex-cli/packages/adapters/src/model/model-execution.ts";

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO = resolve(HERE, "../..");
const OUT = join(REPO, "go/testdata/vectors/model-failure-extended.json");

type ApiCallInput = {
  kind: "apiCall";
  statusCode?: number;
  message?: string;
  responseBody?: string;
  responseHeaders?: Record<string, string>;
  isRetryable?: boolean;
  data?: unknown;
};
type PlainInput = {
  kind: "plain";
  name?: string;
  code?: string;
  message?: string;
};
type CauseInput = {
  kind: "cause";
  outer: { kind: "apiCall"; statusCode?: number; message?: string };
  code?: string;
  name?: string;
  message?: string;
};
type BusinessInput = {
  kind: "providerBusiness";
  providerCode?: string;
  providerMessage?: string;
  responseBodySummary?: Record<string, unknown>;
  statusCode?: number;
  responseStatus?: number;
  responseHeaders?: Record<string, string>;
};
type IdleInput = { kind: "idleTimeout"; idleMs?: number; timeoutMs?: number };

type FailureInput = {
  name: string;
  error: ApiCallInput | PlainInput | CauseInput | BusinessInput | IdleInput;
  abortSignal?: boolean;
};

const jsonBody = (obj: unknown) => JSON.stringify(obj);

function buildError(input: FailureInput["error"]): unknown {
  switch (input.kind) {
    case "apiCall":
      return new APICallError({
        message: input.message ?? "api call failed",
        url: "http://127.0.0.1/v1/chat/completions",
        requestBodyValues: {},
        statusCode: input.statusCode,
        responseBody: input.responseBody,
        responseHeaders: input.responseHeaders,
        isRetryable: input.isRetryable,
        data: input.data,
      });
    case "plain": {
      const e: any = new Error(input.message ?? "plain error");
      if (input.name) e.name = input.name;
      if (input.code) e.code = input.code;
      return e;
    }
    case "cause": {
      const inner = new APICallError({
        message: input.outer.message ?? "inner",
        url: "http://127.0.0.1/v1/chat/completions",
        requestBodyValues: {},
        statusCode: input.outer.statusCode,
      });
      const e: any = new Error(input.message ?? "outer");
      if (input.name) e.name = input.name;
      if (input.code) e.code = input.code;
      e.cause = inner;
      return e;
    }
    case "providerBusiness":
      // 模型执行层在拿到非 2xx body 后直接抛这个错误，它没有 statusCode 以外的字段来源。
      return new ProviderBusinessError({
        providerCode: input.providerCode,
        providerId: "test-provider",
        providerKind: "openai-compatible",
        providerMessage: input.providerMessage,
        responseBodySummary: input.responseBodySummary,
        responseHeaders: input.responseHeaders,
        responseStatus: input.responseStatus,
        statusCode: input.statusCode,
      });
    case "idleTimeout": {
      const timeoutMs = input.timeoutMs ?? 600000;
      const e: any = new Error(`Model stream stalled: no event received for ${timeoutMs}ms.`);
      e.name = "ModelStreamIdleTimeoutError";
      e.code = "MODEL_STREAM_IDLE_TIMEOUT";
      e.idleMs = input.idleMs ?? timeoutMs;
      e.timeoutMs = timeoutMs;
      return e;
    }
  }
}

const inputs: FailureInput[] = [
  // ── ProviderBusinessError：failure-classifier.ts 的业务错误主路径 ────────────
  {
    name: "provider-business-1312-overloaded",
    error: { kind: "providerBusiness", providerCode: "1312", statusCode: 500 },
  },
  {
    name: "provider-business-1005-quota-terminal",
    error: { kind: "providerBusiness", providerCode: "1005", statusCode: 429 },
  },
  {
    name: "provider-business-1234-internal-network",
    error: { kind: "providerBusiness", providerCode: "1234", statusCode: 500 },
  },
  {
    name: "provider-business-2007-server-error",
    error: {
      kind: "providerBusiness",
      providerCode: "2007",
      providerMessage: "upstream rejected",
      statusCode: 502,
    },
  },
  {
    name: "provider-business-1308-concurrency-terminal",
    error: { kind: "providerBusiness", providerCode: "1308", statusCode: 429 },
  },
  {
    name: "provider-business-1316-spend-limit-terminal",
    error: { kind: "providerBusiness", providerCode: "1316", statusCode: 429 },
  },
  {
    name: "provider-business-wrapper-code-ignored",
    error: { kind: "providerBusiness", providerCode: "PROVIDER_BUSINESS_ERROR", statusCode: 400 },
  },
  {
    name: "provider-business-nested-summary-code",
    error: {
      kind: "providerBusiness",
      responseBodySummary: { error: { code: "1261", message: "context" } },
      statusCode: 500,
    },
  },
  {
    name: "provider-business-context-provider-code",
    error: {
      kind: "providerBusiness",
      providerCode: "model_context_exceeded",
      statusCode: 500,
    },
  },
  {
    name: "provider-business-timeout-provider-code",
    error: {
      kind: "providerBusiness",
      providerCode: "UND_ERR_HEADERS_TIMEOUT",
      statusCode: 502,
    },
  },
  {
    name: "provider-business-bracketed-code",
    error: {
      kind: "providerBusiness",
      responseBodySummary: { message: "[1312][upstream] overloaded" },
      statusCode: 500,
    },
  },
  {
    name: "provider-business-response-status-only",
    error: { kind: "providerBusiness", responseStatus: 503, providerMessage: "down" },
  },

  // ── APICallError.data.error / responseBody 里的业务码 ──────────────────────
  {
    name: "body-success-false-code-2007",
    error: {
      kind: "apiCall",
      responseBody: jsonBody({ success: false, code: "2007", msg: "upstream failed" }),
    },
  },
  {
    name: "data-code-1312-overload",
    error: {
      kind: "apiCall",
      statusCode: 500,
      data: { error: { code: "1312", message: "overloaded" } },
    },
  },
  {
    name: "data-type-engine-overloaded",
    error: {
      kind: "apiCall",
      statusCode: 503,
      data: { error: { type: "engine_overloaded_error", message: "engine busy" } },
    },
  },
  {
    name: "data-code-1005-long-quota",
    error: {
      kind: "apiCall",
      statusCode: 429,
      data: { error: { code: "1005", message: "long term quota" } },
    },
  },
  {
    name: "data-code-3008-concurrency-terminal",
    error: { kind: "apiCall", statusCode: 429, data: { error: { code: "3008" } } },
  },
  {
    name: "data-code-unknown-429",
    error: {
      kind: "apiCall",
      statusCode: 429,
      data: { error: { code: "9999", message: "weird code" } },
    },
  },
  {
    name: "data-code-unknown-500",
    error: { kind: "apiCall", statusCode: 500, data: { error: { code: "9999" } } },
  },
  {
    name: "data-type-insufficient-quota-only",
    error: { kind: "apiCall", statusCode: 429, data: { error: { type: "insufficient_quota" } } },
  },
  {
    name: "502-context-exceeded-body",
    error: {
      kind: "apiCall",
      statusCode: 502,
      responseBody: jsonBody({ error: { code: "context_length_exceeded" } }),
    },
  },

  // ── Retry-After 解析 ─────────────────────────────────────────────────────
  {
    name: "502-retry-after-seconds",
    error: { kind: "apiCall", statusCode: 502, responseHeaders: { "retry-after": "3" } },
  },
  {
    name: "408-retry-after-seconds",
    error: { kind: "apiCall", statusCode: 408, responseHeaders: { "retry-after": "2" } },
  },
  {
    name: "429-retry-after-ms-overrides-seconds",
    error: {
      kind: "apiCall",
      statusCode: 429,
      responseHeaders: { "retry-after": "30", "retry-after-ms": "750" },
    },
  },
  {
    name: "429-retry-after-fractional-seconds",
    error: { kind: "apiCall", statusCode: 429, responseHeaders: { "retry-after": "0.5" } },
  },
  {
    name: "429-retry-after-negative-clamped",
    error: { kind: "apiCall", statusCode: 429, responseHeaders: { "retry-after": "-2" } },
  },
  {
    name: "503-retry-after-uppercase-header",
    error: { kind: "apiCall", statusCode: 503, responseHeaders: { "Retry-After": "3" } },
  },
  {
    name: "503-retry-after-padded",
    error: { kind: "apiCall", statusCode: 503, responseHeaders: { "retry-after": " 4 " } },
  },
  {
    name: "429-x-should-retry-zero",
    error: {
      kind: "apiCall",
      statusCode: 429,
      responseHeaders: { "retry-after": "30", "x-should-retry": "0" },
    },
  },
  {
    name: "429-isretryable-false-still-rate-limited",
    error: { kind: "apiCall", statusCode: 429, isRetryable: false },
  },

  // ── 状态码边界 ───────────────────────────────────────────────────────────
  { name: "599-server-error", error: { kind: "apiCall", statusCode: 599 } },
  { name: "402-payment-required", error: { kind: "apiCall", statusCode: 402 } },
  { name: "451-legal-restriction", error: { kind: "apiCall", statusCode: 451 } },
  { name: "418-teapot-unknown", error: { kind: "apiCall", statusCode: 418 } },

  // ── 网络码 / 超时码 / TLS / proxy ────────────────────────────────────────
  { name: "network-eai-again", error: { kind: "plain", code: "EAI_AGAIN", message: "dns" } },
  { name: "network-enotfound", error: { kind: "plain", code: "ENOTFOUND", message: "dns" } },
  { name: "network-enetunreach", error: { kind: "plain", code: "ENETUNREACH", message: "net" } },
  { name: "network-ehostunreach", error: { kind: "plain", code: "EHOSTUNREACH", message: "net" } },
  { name: "timeout-name-timeouterror", error: { kind: "plain", name: "TimeoutError" } },
  { name: "timeout-name-timeout-error", error: { kind: "plain", name: "timeout_error" } },
  {
    name: "timeout-und-body",
    error: { kind: "plain", code: "UND_ERR_BODY_TIMEOUT", message: "body timeout" },
  },
  {
    name: "timeout-und-connect-not-network",
    error: { kind: "plain", code: "UND_ERR_CONNECT_TIMEOUT", message: "connect timeout" },
  },
  { name: "timeout-code-etimeout", error: { kind: "plain", code: "ETIMEOUT", message: "timeout" } },
  {
    name: "tls-self-signed-chain",
    error: { kind: "plain", code: "SELF_SIGNED_CERT_IN_CHAIN", message: "tls" },
  },
  {
    name: "tls-altname-invalid",
    error: { kind: "plain", code: "ERR_TLS_CERT_ALTNAME_INVALID", message: "tls" },
  },
  {
    name: "proxy-auth-failed",
    error: { kind: "plain", code: "ERR_PROXY_AUTH_FAILED", message: "p" },
  },
  { name: "abort-code-abort-err", error: { kind: "plain", code: "ABORT_ERR", message: "aborted" } },

  // ── 超窗文案短语（contextlimit.go 的每条规则） ────────────────────────────
  {
    name: "ctx-code-context-window-exceeded",
    error: { kind: "plain", code: "context_window_exceeded", message: "too long" },
  },
  {
    name: "ctx-message-range-of-input-length",
    error: {
      kind: "plain",
      message: "range of input length should be [1, 100000] but got 200000",
    },
  },
  {
    name: "ctx-message-input-token-count",
    error: {
      kind: "plain",
      message:
        "This model's input token count (200000) exceeds the maximum number of tokens allowed (128000)",
    },
  },
  {
    name: "ctx-message-total-token-length",
    error: {
      kind: "plain",
      message: "The total message token length exceeds the model limit of 128000 tokens",
    },
  },
  {
    name: "ctx-message-model-context-window-exceeded",
    error: { kind: "plain", message: "model_context_window_exceeded" },
  },
  {
    name: "ctx-message-max-tokens-parameter-is-not-context",
    error: {
      kind: "apiCall",
      statusCode: 400,
      responseBody: jsonBody({
        error: { message: "max_tokens must be less than the model maximum" },
      }),
    },
  },

  // ── cause 链传播 ─────────────────────────────────────────────────────────
  {
    name: "cause-abort-over-500",
    error: { kind: "cause", code: "ABORT_ERR", message: "aborted", outer: { statusCode: 500 } },
  },
  {
    name: "cause-context-over-400",
    error: {
      kind: "cause",
      code: "EPIPE",
      message: "broken pipe",
      outer: {
        statusCode: 400,
        message: "This model's maximum context length is 1000 tokens, however you requested 5000",
      },
    },
  },
  {
    name: "cause-tls-with-200-inner",
    error: {
      kind: "cause",
      code: "CERT_HAS_EXPIRED",
      message: "cert",
      outer: { statusCode: 200 },
    },
  },

  // ── 流式超时 ─────────────────────────────────────────────────────────────
  { name: "idle-timeout-30s", error: { kind: "idleTimeout", idleMs: 30000, timeoutMs: 30000 } },
  {
    name: "idle-timeout-retry-widened",
    error: { kind: "idleTimeout", idleMs: 630000, timeoutMs: 630000 },
  },
];

const vectors = inputs.map((input) => {
  const error = buildError(input.error);
  const abortSignal = input.abortSignal ? ({ aborted: true } as AbortSignal) : undefined;
  const failure = classifyModelFailure(error, abortSignal);
  return {
    name: input.name,
    error: input.error,
    abortSignal: input.abortSignal ?? false,
    result: {
      code: failure.code,
      message: failure.message,
      reason: failure.reason,
      retryReason: failure.retryReason,
      retryable: failure.retryable,
      retryAfterMs: failure.retryAfterMs ?? null,
      statusCode: failure.statusCode ?? null,
    },
  };
});

const names = new Set(vectors.map((v) => v.name));
if (names.size !== vectors.length) {
  throw new Error("duplicate vector names");
}
if (vectors.length < 40) {
  throw new Error(`expected >= 40 extended vectors, got ${vectors.length}`);
}

writeFileSync(OUT, JSON.stringify(vectors, null, 1) + "\n");
console.log(
  `failure (extended): ${vectors.length} -> ${OUT.replace(`${REPO}/`, "")}; ` +
    `retryable: ${vectors.filter((v) => v.result.retryable).length}`,
);
