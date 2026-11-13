-- 0020: an index for the title page (phases 2 and 3, the title routes).
--
-- user_ratings (media_type, tmdb_id): a title page lists everyone's rating
-- of the title (Community). Only (user_id, media_type, tmdb_id) existed, so
-- each title page read every rating on the install.
CREATE INDEX ix_user_ratings_title ON user_ratings (media_type, tmdb_id);
