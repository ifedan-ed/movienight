-- 0021: each person's own time zone, and the digests each person has had.
--
-- users.time_zone is the IANA name (e.g. "Pacific/Auckland") the person's
-- browser or phone reports (PUT /api/v1/me/time-zone), never guessed from
-- their address. NULL: never reported; their days then follow the install's
-- TIMEZONE, a documented default (internal/users.Location). The name is
-- checked when it is written (Go's time.LoadLocation, with the zone database
-- built into the binary), not here: Postgres's own list of zones is a
-- different copy and may be older or newer.
ALTER TABLE users ADD COLUMN time_zone text;

-- digest_done: the latest period of each kind ("week", "month", "year")
-- that the schedule has finished for a person, as the local date the
-- period starts on in their zone. Before, one install-wide marker
-- (app_settings "digest_done:<kind>") said a period was done for everyone;
-- with a zone per person a week ends at a different moment for each, so
-- each person's period is decided and recorded on its own. A date, not a
-- moment: someone whose zone changes keeps "the week of 2 November" done
-- instead of getting it again with new boundaries.
CREATE TABLE digest_done (
    user_id      integer NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind         text NOT NULL,
    period_start date NOT NULL,
    done_at      timestamp with time zone NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, kind)
);
