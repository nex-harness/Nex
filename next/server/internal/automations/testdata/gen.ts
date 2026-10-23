/**
 * Golden fixture generator for next/server/internal/automations.
 *
 * The Go calendar math must produce exactly the times the old TypeScript
 * implementation produced, so every expectation in nextrun.json is computed
 * by the real code in packages/services/src/session/automationCron.ts — not
 * by a re-implementation here.
 *
 * Regenerate from the repo root (Node 24 + tsx):
 *
 *   PATH=/Users/tylinux/.nvm/versions/node/v24.21.0/bin:$PATH \
 *     node_modules/.bin/tsx next/server/internal/automations/testdata/gen.ts
 *
 * Output: next/server/internal/automations/testdata/nextrun.json
 *
 * Cron cases additionally include croner's own set (parse-only) so the
 * `cron` block carries the old client's answers side by side.
 */

import { mkdirSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { Cron } from "croner";
import {
  computeAutomationNextRunAt,
  computeNextRunAt,
  computeScheduleRuleNextRunAt,
} from "../../../../../packages/services/src/session/automationCron.js";
import type { NexAutomationScheduleRule } from "../../../../../packages/shared/src/automation-types.js";

const OUT_DIR = dirname(fileURLToPath(import.meta.url));

/** A "from" instant + the zone both old and new implementations evaluate in. */
interface FromCase {
  /** IANA zone the old code ran its local calendar math in. */
  timezone: string;
  /** ISO instant; the old functions were called with Date.now() = this. */
  from: string;
}

const FROMS: FromCase[] = [
  // Beijing has no DST; covers ordinary calendar days and weekday math.
  { timezone: "Asia/Shanghai", from: "2026-03-10T02:34:56.789Z" },
  { timezone: "Asia/Shanghai", from: "2026-06-28T17:00:00.000Z" },
  // US spring forward (2026-03-08 02:00 -> 03:00) and fall back (2026-11-01).
  { timezone: "America/New_York", from: "2026-03-08T01:30:00.000Z" },
  { timezone: "America/New_York", from: "2026-03-08T06:30:00.000Z" },
  { timezone: "America/New_York", from: "2026-11-01T04:30:00.000Z" },
  { timezone: "America/New_York", from: "2026-11-01T06:30:00.000Z" },
  // Europe/Berlin spring forward (2026-03-29 02:00 -> 03:00).
  { timezone: "Europe/Berlin", from: "2026-03-29T00:30:00.000Z" },
  { timezone: "Europe/Berlin", from: "2026-10-25T00:30:00.000Z" },
  // UTC baseline.
  { timezone: "UTC", from: "2026-01-31T23:59:30.000Z" },
];

const CRONS = [
  "* * * * *",
  "*/5 * * * *",
  "*/15 * * * *",
  "0 * * * *",
  "30 * * * *",
  "0 9 * * *",
  "45 6 * * 1-5",
  "0 0 * * 0",
  "15 10 * * 1,3,5",
  "0 8 1 * *",
  "0 8 29 * *",
  "0 12 * * 6,0",
  "0 2 * * *",
  "30 2 * * *",
  "0 3 * * *",
  "0 1 * * *",
  "5 0 1 */3 *",
  "0 9 * JAN-MAR *",
  "*/20 9-17 * * 1-5",
  "0 0 30 2 *",
];

/** Fixed anchor instants used by scheduleRule cases. */
const ANCHORS = [
  "2026-03-10T02:34:56.789Z",
  "2026-06-30T15:00:00.000Z",
  "2026-01-15T09:05:00.000Z",
  "2026-11-01T04:30:00.000Z",
];

function rule(overrides: Partial<NexAutomationScheduleRule>): NexAutomationScheduleRule {
  return {
    unit: "daily",
    interval: 1,
    hour: 9,
    minute: 0,
    anchorAt: Date.parse(ANCHORS[0]!),
    ...overrides,
  };
}

function rules(): NexAutomationScheduleRule[] {
  const base = (index: number): number => Date.parse(ANCHORS[index % ANCHORS.length]!);
  return [
    rule({ unit: "minute", interval: 10, anchorAt: base(0) }),
    rule({ unit: "minute", interval: 1, anchorAt: base(1) }),
    rule({ unit: "minute", interval: 61, anchorAt: base(2) }),
    rule({ unit: "hourly", interval: 1, minute: 15, anchorAt: base(0) }),
    rule({ unit: "hourly", interval: 6, minute: 0, anchorAt: base(1) }),
    rule({ unit: "hourly", interval: 5, minute: 30, anchorAt: base(3) }),
    rule({ unit: "daily", interval: 1, hour: 9, minute: 0, anchorAt: base(0) }),
    rule({ unit: "daily", interval: 1, hour: 2, minute: 30, anchorAt: base(0) }),
    rule({ unit: "daily", interval: 2, hour: 1, minute: 0, anchorAt: base(1) }),
    rule({ unit: "daily", interval: 1, hour: 6, minute: 45, anchorAt: base(2) }),
    rule({ unit: "weekly", interval: 1, hour: 8, minute: 0, weekdays: [1], anchorAt: base(0) }),
    rule({ unit: "weekly", interval: 1, hour: 8, minute: 0, weekdays: [0, 6], anchorAt: base(1) }),
    rule({ unit: "weekly", interval: 2, hour: 18, minute: 30, weekdays: [2, 4], anchorAt: base(2) }),
    rule({ unit: "monthly", interval: 1, hour: 7, minute: 0, monthDays: [1], monthlyMode: "date", anchorAt: base(0) }),
    rule({ unit: "monthly", interval: 1, hour: 7, minute: 0, monthDays: [13, 31], monthlyMode: "date", anchorAt: base(1) }),
    rule({ unit: "monthly", interval: 3, hour: 7, minute: 0, monthDays: [15], monthlyMode: "date", anchorAt: base(2) }),
    rule({ unit: "monthly", interval: 1, hour: 7, minute: 0, weekdays: [5], monthlyMode: "weekday", anchorAt: base(0) }),
    rule({ unit: "yearly", interval: 1, hour: 9, minute: 0, months: [3], monthDays: [10], anchorAt: base(0) }),
    rule({ unit: "yearly", interval: 1, hour: 9, minute: 0, months: [2], monthDays: [29], anchorAt: base(2) }),
  ];
}

/**
 * Evaluate `fn` as if the process ran in `timezone`. The old implementation
 * reads local calendar fields; changing TZ affects the `Date` accessors in
 * Node, so this is exactly the behaviour the old code had on a machine in
 * that zone (this is how the Go side evaluates them too).
 */
function inZone<T>(timezone: string, fn: () => T): T {
  const previous = process.env.TZ;
  process.env.TZ = timezone;
  try {
    return fn();
  } finally {
    if (previous === undefined) delete process.env.TZ;
    else process.env.TZ = previous;
  }
}

interface CronCase {
  timezone: string;
  from: string;
  cronExpr: string;
  /** computeNextRunAt (croner, process local zone = timezone). */
  nextRunAt: number | null;
}

const cronCases: CronCase[] = [];
for (const fromCase of FROMS) {
  for (const expr of CRONS) {
    const from = Date.parse(fromCase.from);
    cronCases.push({
      timezone: fromCase.timezone,
      from: fromCase.from,
      cronExpr: expr,
      nextRunAt: inZone(fromCase.timezone, () => computeNextRunAt(expr, from)),
    });
  }
}

interface RuleCase {
  timezone: string;
  from: string;
  rule: NexAutomationScheduleRule;
  /** computeScheduleRuleNextRunAt evaluated with the process zone = timezone. */
  nextRunAt: number | null;
  /** computeAutomationNextRunAt on the same rule (scheduleRule wins). */
  automationNextRunAt: number | null;
}

const ruleCases: RuleCase[] = [];
for (const fromCase of FROMS) {
  for (const candidate of rules()) {
    const from = Date.parse(fromCase.from);
    ruleCases.push({
      timezone: fromCase.timezone,
      from: fromCase.from,
      rule: candidate,
      nextRunAt: inZone(fromCase.timezone, () => computeScheduleRuleNextRunAt(candidate, from)),
      automationNextRunAt: inZone(fromCase.timezone, () =>
        computeAutomationNextRunAt({ cronExpr: "* * * * *", scheduleRule: candidate }, from),
      ),
    });
  }
}

/** Previous croner match (computeInitialAutomationNextRunAt's stale window). */
interface PreviousCase {
  timezone: string;
  from: string;
  cronExpr: string;
  previousRunAt: number | null;
}

const previousCases: PreviousCase[] = [];
for (const fromCase of FROMS) {
  for (const expr of CRONS.filter((value) => /^\d+ \d+ \d+ \d+ \*$/.test(value))) {
    const from = Date.parse(fromCase.from);
    previousCases.push({
      timezone: fromCase.timezone,
      from: fromCase.from,
      cronExpr: expr,
      previousRunAt: inZone(
        fromCase.timezone,
        () => {
          try {
            return new Cron(expr).previousRuns(1, new Date(from))[0]?.getTime() ?? null;
          } catch {
            return null;
          }
        },
      ),
    });
  }
}

const payload = {
  generatedBy: "next/server/internal/automations/testdata/gen.ts",
  cronerVersion: "10.x",
  cron: cronCases,
  scheduleRule: ruleCases,
  previous: previousCases,
};

mkdirSync(OUT_DIR, { recursive: true });
writeFileSync(join(OUT_DIR, "nextrun.json"), `${JSON.stringify(payload, null, 2)}\n`);
console.log(
  `wrote ${cronCases.length} cron, ${ruleCases.length} scheduleRule and ${previousCases.length} previous cases`,
);
