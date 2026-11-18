package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

type ChatJob struct {
	ID        string
	BindingID string
	Kind      string
	Payload   json.RawMessage
	Attempts  int
	RequestID string
	QueuedAt  int64
}

func insertChatJob(ctx context.Context, tx *sql.Tx, account string, job ChatJob) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO chat_jobs(id,account,binding_id,kind,payload,request_id,queued_at)
	VALUES (?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, job.ID, account, job.BindingID, job.Kind, string(job.Payload), job.RequestID, job.QueuedAt)
	return err
}

func (s *Store) EnqueueChatJob(ctx context.Context, account string, job ChatJob) error {
	return s.write(ctx, func(tx *sql.Tx) error { return insertChatJob(ctx, tx, account, job) })
}

func (s *Store) EnqueueChatPage(ctx context.Context, account, bindingID, cursor string, jobs []ChatJob) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		statement, err := tx.PrepareContext(ctx, `INSERT INTO chat_jobs(id,account,binding_id,kind,payload)
			VALUES (?,?,?,?,?) ON CONFLICT(id) DO NOTHING`)
		if err != nil {
			return err
		}
		defer statement.Close()
		for _, job := range jobs {
			if _, err := statement.ExecContext(ctx, job.ID, account, bindingID, job.Kind, string(job.Payload)); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO chat_cursors VALUES (?,?)
			ON CONFLICT(binding_id) DO UPDATE SET cursor=excluded.cursor`, bindingID, cursor)
		return err
	})
}

func (s *Store) ChatCursor(ctx context.Context, bindingID, start string) (string, error) {
	var cursor string
	err := s.db.QueryRowContext(ctx, "SELECT cursor FROM chat_cursors WHERE binding_id=?", bindingID).Scan(&cursor)
	if errors.Is(err, sql.ErrNoRows) {
		return start, nil
	}
	return cursor, err
}

func (s *Store) PendingChatJobs(ctx context.Context, account, kind, bindingID string) ([]ChatJob, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,binding_id,kind,payload,attempts,request_id,queued_at FROM chat_jobs
		WHERE account=? AND kind=? AND (?='' OR binding_id=?) AND state='pending' AND ready_at<=?
		ORDER BY rowid LIMIT 20`, account, kind, bindingID, bindingID, s.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []ChatJob
	for rows.Next() {
		var job ChatJob
		var body string
		if err := rows.Scan(&job.ID, &job.BindingID, &job.Kind, &body, &job.Attempts, &job.RequestID, &job.QueuedAt); err != nil {
			return nil, err
		}
		job.Payload = json.RawMessage(body)
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *Store) SaveChatJobPayload(ctx context.Context, job ChatJob) error {
	_, err := s.db.ExecContext(ctx, "UPDATE chat_jobs SET payload=? WHERE id=? AND state='pending'", string(job.Payload), job.ID)
	return err
}

func (s *Store) ReplaceChatJob(ctx context.Context, job ChatJob) error {
	_, err := s.db.ExecContext(ctx, `UPDATE chat_jobs SET kind=?,payload=?,attempts=0,ready_at=0
		WHERE id=? AND state='pending'`, job.Kind, string(job.Payload), job.ID)
	return err
}

func (s *Store) SetChatJobState(ctx context.Context, id, state, result string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE chat_jobs SET state=?,result=?,
		payload=CASE WHEN ? IN ('sending','blocked') THEN payload ELSE '' END WHERE id=?`, state, result, state, id)
	return err
}

func (s *Store) RetryChatJob(ctx context.Context, job ChatJob) error {
	wait := min(300, 1<<min(job.Attempts, 9))
	_, err := s.db.ExecContext(ctx, `UPDATE chat_jobs SET state='pending',attempts=attempts+1,ready_at=? WHERE id=? AND state IN ('pending','sending')`,
		s.Now().Unix()+int64(wait), job.ID)
	return err
}

func (s *Store) ClaimChatDelivery(ctx context.Context, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, "UPDATE chat_jobs SET state='sending',result='' WHERE id=? AND state='pending'", id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *Store) RecoverChatJobs(ctx context.Context, account string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE chat_jobs SET state='unknown',payload='',result='Interrupted delivery'
		WHERE account=? AND state='sending'`, account)
	return err
}
