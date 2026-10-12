# Oracle coverage (generated)

Scenario runs measured: 0. Vector scripts measured: 14. Threshold: 80% of executable lines. Below threshold: 4 of 18.

A file is satisfied by either source: scenario coverage (agent bundle) or value-level vector coverage (direct source).

| File                                       | Owner  | Lines hit        | %    | Status    |
| ------------------------------------------ | ------ | ---------------- | ---- | --------- |
| nex-protocol-v4/wire-codec.ts              | phase1 | 235/240 (vector) | 98%  | ok        |
| nex-protocol-v4/apply.ts                   | phase1 | 143/218 (vector) | 66%  | **below** |
| nex-protocol-v4/coalesce.ts                | phase1 | 170/173 (vector) | 98%  | ok        |
| agent/turn-machine.js                      | phase1 | 189/344 (vector) | 55%  | **below** |
| tool/edit-matchers.js                      | phase1 | 394/411 (vector) | 96%  | ok        |
| handlers/edit.js                           | phase1 | 640/656 (vector) | 98%  | ok        |
| handlers/write.js                          | phase1 | 369/377 (vector) | 98%  | ok        |
| handlers/read.js                           | phase1 | 454/526 (vector) | 86%  | ok        |
| handlers/bash.js                           | phase1 | 329/569 (vector) | 58%  | **below** |
| model/failure-classifier.js                | phase1 | 574/633 (vector) | 91%  | ok        |
| model/streaming-tool-call-assembler.js     | phase1 | 113/117 (vector) | 97%  | ok        |
| model/retry-policy.js                      | phase1 | 101/101 (vector) | 100% | ok        |
| session-store/migration-runner.js          | phase1 | 253/336 (vector) | 75%  | **below** |
| nex-protocol-v4/command-inbox.js           | phase1 | not measured     | -    | ok        |
| nex-protocol/server.js                     | phase1 | not measured     | -    | ok        |
| handlers/bash-command-parser.js            | phase4 | not measured     | -    | ok        |
| handlers/bash-readonly-policy-argv.js      | phase4 | not measured     | -    | ok        |
| handlers/bash-command-permission-policy.js | phase4 | not measured     | -    | ok        |
