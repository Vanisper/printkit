package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const schemaVersion = 1

func initialize(ctx context.Context, db *sql.DB) error {
	for _, pragma := range []string{"PRAGMA busy_timeout = 5000", "PRAGMA foreign_keys = ON", "PRAGMA journal_mode = WAL"} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			return fmt.Errorf("配置任务库: %w", err)
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version == schemaVersion {
		return nil
	}
	if version != 0 {
		return fmt.Errorf("不支持的任务库版本 %d，当前支持 %d", version, schemaVersion)
	}
	_, err = tx.ExecContext(ctx, `
		CREATE TABLE jobs (
			seq INTEGER PRIMARY KEY AUTOINCREMENT,
			id TEXT NOT NULL UNIQUE,
			caller_id TEXT NOT NULL,
			request_key TEXT NOT NULL,
			request_sha256 TEXT NOT NULL,
			kind TEXT NOT NULL CHECK (kind IN ('html', 'pdf')),
			title TEXT NOT NULL,
			state TEXT NOT NULL CHECK (state IN ('queued', 'rendering', 'output_ready', 'failed')),
			input_sha256 TEXT NOT NULL,
			document BLOB NOT NULL,
			settings TEXT NOT NULL,
			artifact BLOB,
			artifact_sha256 TEXT NOT NULL DEFAULT '',
			output_size INTEGER NOT NULL DEFAULT 0,
			engine TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			recovery_count INTEGER NOT NULL DEFAULT 0,
			attempt_count INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			UNIQUE (caller_id, request_key),
			CHECK (state != 'output_ready' OR (artifact IS NOT NULL AND artifact_sha256 != '' AND output_size > 0))
		);
		CREATE INDEX jobs_queue ON jobs (state, seq);
		CREATE TABLE attempts (
			job_id TEXT NOT NULL REFERENCES jobs(id),
			number INTEGER NOT NULL,
			started_at INTEGER NOT NULL,
			finished_at INTEGER,
			error TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (job_id, number)
		);
		PRAGMA user_version = 1;
	`)
	if err != nil {
		return fmt.Errorf("初始化任务库: %w", err)
	}
	return tx.Commit()
}

func readJob(ctx context.Context, tx *sql.Tx, callerID, id string) (Job, error) {
	var job Job
	var created, updated int64
	err := tx.QueryRowContext(ctx, `SELECT id, kind, title, state, input_sha256, artifact_sha256, output_size, engine, error, created_at, updated_at
		FROM jobs WHERE caller_id = ? AND id = ?`, callerID, id).Scan(&job.ID, &job.Kind, &job.Title, &job.State, &job.InputSHA256, &job.ArtifactSHA256, &job.OutputSize, &job.Engine, &job.Error, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	job.Mode = "artifact"
	job.CreatedAt, job.UpdatedAt = time.Unix(0, created).UTC(), time.Unix(0, updated).UTC()
	job.Attempts = []Attempt{}
	rows, err := tx.QueryContext(ctx, `SELECT number, started_at, finished_at, error FROM attempts WHERE job_id = ? ORDER BY number`, id)
	if err != nil {
		return Job{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var attempt Attempt
		var started int64
		var finished sql.NullInt64
		if err = rows.Scan(&attempt.Number, &started, &finished, &attempt.Error); err != nil {
			return Job{}, err
		}
		attempt.StartedAt = time.Unix(0, started).UTC()
		if finished.Valid {
			value := time.Unix(0, finished.Int64).UTC()
			attempt.FinishedAt = &value
		}
		job.Attempts = append(job.Attempts, attempt)
	}
	return job, rows.Err()
}

type execution struct {
	id       string
	kind     Kind
	document []byte
	attempt  int
}

func (s *Service) recover(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().UnixNano()
	_, err = tx.ExecContext(ctx, `UPDATE attempts SET finished_at = ?, error = '服务退出导致执行中断'
		WHERE finished_at IS NULL AND job_id IN (SELECT id FROM jobs WHERE state = 'rendering')`, now)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE jobs SET
		state = CASE WHEN recovery_count < 1 THEN 'queued' ELSE 'failed' END,
		error = CASE WHEN recovery_count < 1 THEN '' ELSE '执行连续中断，已达到自动恢复上限' END,
		recovery_count = recovery_count + 1,
		updated_at = ? WHERE state = 'rendering'`, now)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) claim(ctx context.Context) (execution, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return execution{}, err
	}
	defer tx.Rollback()
	var task execution
	err = tx.QueryRowContext(ctx, `SELECT id, kind, document, attempt_count + 1 FROM jobs WHERE state = 'queued' ORDER BY seq LIMIT 1`).Scan(&task.id, &task.kind, &task.document, &task.attempt)
	if err != nil {
		return execution{}, err
	}
	now := time.Now().UTC().UnixNano()
	_, err = tx.ExecContext(ctx, `UPDATE jobs SET state = 'rendering', attempt_count = ?, updated_at = ? WHERE id = ?`, task.attempt, now, task.id)
	if err != nil {
		return execution{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO attempts (job_id, number, started_at) VALUES (?, ?, ?)`, task.id, task.attempt, now)
	if err != nil {
		return execution{}, err
	}
	if err = tx.Commit(); err != nil {
		return execution{}, err
	}
	return task, nil
}

func (s *Service) complete(ctx context.Context, task execution, output []byte, engine string, executionErr error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	state, message, digest := StateOutputReady, "", ""
	if executionErr != nil {
		state, message, output = StateFailed, executionErr.Error(), nil
	} else {
		digest = sha256Hex(output)
	}
	now := time.Now().UTC().UnixNano()
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET state = ?, artifact = ?, artifact_sha256 = ?, output_size = ?, engine = ?, error = ?, updated_at = ?
		WHERE id = ? AND state = 'rendering'`, state, output, digest, len(output), engine, message, now, task.id)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("任务 %s 不处于执行状态", task.id)
	}
	_, err = tx.ExecContext(ctx, `UPDATE attempts SET finished_at = ?, error = ? WHERE job_id = ? AND number = ?`, now, message, task.id, task.attempt)
	if err != nil {
		return err
	}
	return tx.Commit()
}
