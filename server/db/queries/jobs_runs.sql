-- The worker's run records (migration 0018).

-- JobRunClaim takes one due run of a job. No row back: another worker (or an
-- earlier start of this one) already has it, and this one must not run it.
-- name: JobRunClaim :one
INSERT INTO job_runs (job, due_at, worker)
VALUES ($1, $2, $3)
ON CONFLICT (job, due_at) DO NOTHING
RETURNING started_at;

-- name: JobRunFinish :exec
UPDATE job_runs SET finished_at = now(), error = $3 WHERE job = $1 AND due_at = $2;

-- JobRunLatest is the latest due time recorded for a job, finished or not;
-- no row when the job never ran under the Go worker.
-- name: JobRunLatest :one
SELECT due_at FROM job_runs WHERE job = $1 ORDER BY due_at DESC LIMIT 1;

-- name: JobRunPrune :execrows
DELETE FROM job_runs WHERE started_at < $1;
