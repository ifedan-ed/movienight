-- 0019: indexes for the read and write routes (phases 2 and 3).
--
-- history (user_id, tmdb_id): a page's watch counts (StateForTitles), a
-- title page's watches and a show's ticked episodes look up one person's
-- watches of given titles. Only (user_id) and (tmdb_id) existed, so each
-- lookup read either all of the person's history or everyone's watches of
-- the title.
CREATE INDEX ix_history_user_tmdb ON history (user_id, tmdb_id);
