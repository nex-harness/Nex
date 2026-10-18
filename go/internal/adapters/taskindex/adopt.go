package taskindex

import (
	"context"
	"database/sql"
	"strings"
)

// adopt.go — the two code-level task-index migrations, ported as Go functions
// with the same results (the same approach Phase 1 took for session-store
// migrations 0020–0022). Both must be called inside the library-level migration
// transaction; per-step read-modify-write loops are not allowed.

// adoptSchema ports adoptSchema (0001): create the three frozen schema blocks,
// add the historical columns that predate this build, then create the indexes.
//
// 历史列声明冻结：不得以实时 Repo/schema 代替，否则新版构建会改变已应用 checksum。
func adoptSchema(ctx context.Context, conn *sql.Conn) error {
	fragments, err := Fragments()
	if err != nil {
		return err
	}
	if err := execScript(ctx, conn, fragments.TaskIndexSchema+fragments.AutomationSchema+fragments.OffPeakSchema); err != nil {
		return err
	}
	for _, col := range frozenColumns {
		exists, err := tableHasColumn(ctx, conn, col.Table, col.Column)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err := conn.ExecContext(ctx,
			"ALTER TABLE "+col.Table+" ADD COLUMN "+col.Column+" "+col.Definition); err != nil {
			return err
		}
		if col.Table == "automations" && col.Column == "scheduled_run_count" {
			// 首次加列时存量行必须回填，否则历史定时任务的 scheduled_run_count 为 0。
			if _, err := conn.ExecContext(ctx,
				"UPDATE automations SET scheduled_run_count=run_count"); err != nil {
				return err
			}
		}
	}
	if err := execScript(ctx, conn, fragments.Indexes); err != nil {
		return err
	}
	// 沿用已裁决的旧重复绑定保留策略，但不再吞掉权限/语法/磁盘等真实 SQL 错误：
	// 存量库若已有重复绑定就不建这个唯一索引，而不是把真实错误当重复吞掉。
	duplicate, err := hasDuplicateActiveBinding(ctx, conn)
	if err != nil {
		return err
	}
	if !duplicate {
		return execScript(ctx, conn, fragments.BoundIndex)
	}
	return nil
}

// importLegacyAutomationSelections ports importLegacyAutomationSelections
// (0002): one frozen conversion, run inside the migration transaction. Rows keep
// the old three columns untouched; the reader only looks at the new column.
//
// 有旧来源时允许重建未发布的目标值；无法确定身份留 SQL NULL，默认语义写 JSON null。
func importLegacyAutomationSelections(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx,
		"SELECT automation_id, model, provider, thought_level FROM automations WHERE model IS NOT NULL")
	if err != nil {
		return err
	}
	type legacyRow struct {
		id           string
		model        sql.NullString
		provider     sql.NullString
		thoughtLevel sql.NullString
	}
	var legacy []legacyRow
	for rows.Next() {
		var r legacyRow
		if err := rows.Scan(&r.id, &r.model, &r.provider, &r.thoughtLevel); err != nil {
			rows.Close()
			return err
		}
		legacy = append(legacy, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, row := range legacy {
		decoded, ok := decodeLegacySelection(row.model.String, row.provider.String, row.thoughtLevel.String)
		if !ok {
			// 有旧显式意图但无法确定身份，不保留未发布中间态的"默认"，避免静默换模型。
			if strings.TrimSpace(row.model.String) != "" {
				if _, err := conn.ExecContext(ctx,
					"UPDATE automations SET model_selection=NULL WHERE automation_id=?", row.id); err != nil {
					return err
				}
			}
			continue
		}
		// 外层 IMMEDIATE 事务保证旧来源和写入同一快照；旧列/时间戳都不修改。
		// IS 比较复刻 TypeScript 的 `IS ?`（NULL-safe）语义。
		if _, err := conn.ExecContext(ctx,
			`UPDATE automations SET model_selection = ?
       WHERE automation_id = ?
         AND model IS ? AND provider IS ? AND thought_level IS ?`,
			legacySelectionJSON(decoded), row.id, nullText(row.model), nullText(row.provider), nullText(row.thoughtLevel)); err != nil {
			return err
		}
	}
	_, err = conn.ExecContext(ctx, `UPDATE automations SET model_selection='null'
    WHERE model_selection IS NULL AND (model IS NULL OR trim(model)='')`)
	return err
}

func nullText(v sql.NullString) any {
	if !v.Valid {
		return nil
	}
	return v.String
}

func tableHasColumn(ctx context.Context, conn *sql.Conn, table, column string) (bool, error) {
	rows, err := conn.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid        int
			name       string
			ctype      sql.NullString
			notNull    sql.NullInt64
			dflt       sql.NullString
			primaryKey sql.NullInt64
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &primaryKey); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func hasDuplicateActiveBinding(ctx context.Context, conn *sql.Conn) (bool, error) {
	var found int
	err := conn.QueryRowContext(ctx,
		"SELECT count(*) FROM (SELECT 1 FROM off_peak_tasks WHERE "+activePredicate+
			" GROUP BY workspace_key, session_id HAVING count(*)>1 LIMIT 1)").Scan(&found)
	if err != nil {
		return false, err
	}
	return found > 0, nil
}
