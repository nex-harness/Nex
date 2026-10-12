// 生成 Go 侧 envelope 解析的对照向量：每行输入由 TypeScript 的 nexProtocolMessageSchema 判定。
// Go 测试读取本文件并要求 accept/reject 判定一致（见 go/internal/protocol/envelope_test.go）。
import { writeFileSync } from "node:fs";
import { nexProtocolMessageSchema } from "../../packages/shared/src/nex-protocol/index.ts";

const cases: string[] = [
  // 合法四种形态
  '{"id":1,"method":"session/create"}',
  '{"id":"abc","method":"session/create","params":{"a":1}}',
  '{"method":"v4/conversation/frame","params":{"x":[1,2,3]}}',
  '{"id":7,"result":{"ok":true}}',
  '{"id":7,"result":null}',
  '{"id":"s","error":{"code":-32601,"message":"Method not found: x"}}',
  '{"id":2,"error":{"code":1,"message":"m","data":{"k":"v"}}}',
  // id 的数值边界
  '{"id":1.0,"method":"a"}',
  '{"id":1e3,"method":"a"}',
  '{"id":-5,"method":"a"}',
  '{"id":0,"method":"a"}',
  '{"id":9007199254740991,"method":"a"}',
  '{"id":9007199254740992,"method":"a"}',
  '{"id":1.5,"method":"a"}',
  '{"id":true,"method":"a"}',
  '{"id":null,"method":"a"}',
  '{"id":[1],"method":"a"}',
  '{"id":"","method":"a"}',
  // method 非法
  '{"method":""}',
  '{"method":"   "}',
  '{"method":1}',
  '{"id":1,"method":null}',
  // 严格对象：多余字段
  '{"id":1,"method":"a","extra":1}',
  '{"method":"a","id2":1}',
  '{"id":1,"result":1,"extra":1}',
  '{"id":1,"error":{"code":1,"message":"m"},"extra":1}',
  '{"id":1,"error":{"code":1,"message":"m","extra":1}}',
  // 互斥
  '{"id":1,"result":1,"error":{"code":1,"message":"m"}}',
  '{"id":1,"method":"a","result":1}',
  '{"id":1}',
  "{}",
  "[]",
  "null",
  "1",
  '"s"',
  // error 细节
  '{"id":1,"error":{"code":1.5,"message":"m"}}',
  '{"id":1,"error":{"code":"1","message":"m"}}',
  '{"id":1,"error":{"code":1,"message":""}}',
  '{"id":1,"error":{"code":1,"message":" "}}',
  '{"id":1,"error":{"code":1}}',
  '{"id":1,"error":{"message":"m"}}',
  '{"id":1,"error":null}',
  '{"id":1,"error":"x"}',
  // trace
  '{"id":1,"method":"a","trace":{"traceId":"t"}}',
  '{"id":1,"method":"a","trace":{"traceparent":"00-abc-def-01","traceId":"t","parentId":"p","spanId":"s"}}',
  '{"id":1,"method":"a","trace":{"traceId":""}}',
  '{"id":1,"method":"a","trace":{"traceId":" "}}',
  '{"id":1,"method":"a","trace":{"unknown":"t"}}',
  '{"id":1,"method":"a","trace":null}',
  '{"id":1,"method":"a","trace":[]}',
  '{"id":1,"method":"a","trace":{}}',
  // params 的任意形态
  '{"id":1,"method":"a","params":null}',
  '{"id":1,"method":"a","params":[]}',
  '{"id":1,"method":"a","params":"s"}',
  // 键重复：JS 取最后一个
  '{"id":1,"id":2,"method":"a"}',
  '{"id":1,"method":"a","method":""}',
  // 字符串里的行分隔符与转义
  '{"id":1,"method":"a","params":{"t":"\\u2028\\u2029"}}',
  '{"id":1,"method":"a b"}',
  '{"id":1,"method":"\\u00e9"}',
  // 追加：null / 嵌套 / 数值形态的边界（Go 解析器曾在 id:null 上与 zod 不一致）
  '{"id":1,"result":null,"x":null}',
  '{"id":null,"result":1}',
  '{"id":null,"error":{"code":1,"message":"m"}}',
  '{"id":1,"error":{"code":null,"message":"m"}}',
  '{"id":1,"error":{"code":1,"message":null}}',
  '{"id":1,"error":{"code":1,"message":"m","data":null}}',
  '{"id":1,"error":{"code":-1e2,"message":"m"}}',
  '{"id":1,"error":{"code":1e400,"message":"m"}}',
  '{"id":1e400,"method":"a"}',
  '{"id":-0,"method":"a"}',
  '{"id":"1","method":"a"}',
  '{"id":1,"method":"a","params":{"deep":{"deeper":[null,true,1.5,"s"]}}}',
  '{"method":"a","params":undefined}',
  '{"id":1,"method":"a",}',
  '{"id":1 "method":"a"}',
  "",
  " ",
  '{"id":1,"method":"a"} trailing',
  '{"id":1,"method":"\\ud800"}',
  '{"id":1,"method":"a","trace":{"traceId":1}}',
  '{"id":1,"method":"a","trace":{"traceId":null}}',
  '{"__proto__":1,"id":1,"method":"a"}',
  '{"id":1,"method":"a","constructor":1}',
  '{"id":1,"method":"toString"}',
];

const rows = cases.map((input) => {
  let ok = false;
  let normalizedId: unknown = null;
  try {
    const parsed = nexProtocolMessageSchema.safeParse(JSON.parse(input));
    ok = parsed.success;
    if (parsed.success && "id" in parsed.data) normalizedId = parsed.data.id;
  } catch {
    ok = false;
  }
  return { input, accept: ok, normalizedId };
});
writeFileSync(
  new URL("../../go/testdata/vectors/envelope.json", import.meta.url),
  JSON.stringify(rows, null, 1) + "\n",
);
console.log(`wrote ${rows.length} vectors; accept=${rows.filter((r) => r.accept).length}`);
