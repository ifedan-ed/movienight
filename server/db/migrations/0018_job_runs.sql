-- 0018: one row per scheduled run of a worker job.
--
-- The Python ran its jobs with APScheduler inside every web process: with two
-- replicas each job ran twice, and nothing recorded a run, so one missed
-- while the app was down (a deploy across 02:30) was simply lost.
--
-- The Go worker claims each run here before it starts it: the primary key is
-- (job, due_at), the moment the run was due by the schedule, so however many
-- workers see the same moment (a leader change, a deploy that briefly runs
-- old and new side by side), the INSERT ... ON CONFLICT DO NOTHING lets
-- exactly one of them have it. The row then records how it ended, which is
-- what the start-up catch-up reads ("was the latest due run of this job
-- made?") and what an admin reads when a job misbehaves.
--
-- Runs started by hand or by the catch-up use the due time they stand in for,
-- so they too can happen only once.
CREATE TABLE job_runs (
    job         text NOT NULL,
    due_at      timestamp with time zone NOT NULL,
    started_at  timestamp with time zone NOT NULL DEFAULT now(),
    finished_at timestamp with time zone,
    -- NULL while running or after a clean run; the error's text otherwise.
    error       text,
    -- Which worker ran it (host name and process id), for the log trail.
    worker      text NOT NULL,
    PRIMARY KEY (job, due_at)
);

-- Old rows are pruned by the worker (a month of history is kept); the prune
-- walks this.
CREATE INDEX job_runs_started_at_idx ON job_runs (started_at);
