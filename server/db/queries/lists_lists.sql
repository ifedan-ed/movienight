-- Lists (package lists, homecfg): a person's own lists (owner_id = them)
-- and the install's catalog lists (owner_id NULL: Movie Night copies and
-- followed MDBList lists), their items, and the stars people give them.

-- name: GetList :one
SELECT * FROM lists WHERE id = $1;

-- VisibleLists is every list one person sees, in Lists-tab order: their own
-- lists first, then Shared lists, then MDBList lists, oldest first.
-- name: VisibleLists :many
SELECT * FROM lists
WHERE owner_id = @user_id OR owner_id IS NULL
ORDER BY CASE WHEN owner_id IS NOT NULL THEN 0 WHEN source = 'follow' THEN 2 ELSE 1 END, created_at, id;

-- name: ListCounts :many
SELECT list_id, media_type::text AS media_type, count(*)::integer AS n
FROM list_items WHERE list_id = ANY(@ids::integer[])
GROUP BY list_id, media_type;

-- ListCollageKeys: the first five items of each list (the collage posters).
-- name: ListCollageKeys :many
SELECT list_id, media_type::text AS media_type, tmdb_id FROM (
    SELECT list_id, media_type, tmdb_id,
           row_number() OVER (PARTITION BY list_id ORDER BY position, id) AS n
    FROM list_items WHERE list_id = ANY(@ids::integer[])
) ranked WHERE n <= 5 ORDER BY list_id, n;

-- name: StarredLists :many
SELECT list_id FROM list_favorites WHERE user_id = $1;

-- name: StarList :exec
INSERT INTO list_favorites (user_id, list_id, added_at) VALUES ($1, $2, now())
ON CONFLICT (user_id, list_id) DO NOTHING;

-- name: UnstarList :exec
DELETE FROM list_favorites WHERE user_id = $1 AND list_id = $2;

-- name: CreateList :one
INSERT INTO lists (owner_id, name, description, kind, source, filters_json, created_at)
VALUES (@owner_id, @name, @description, @kind, 'app', @filters_json, now())
RETURNING *;

-- name: RenameList :exec
UPDATE lists SET name = @name, description = @description WHERE id = @id;

-- DeleteList: its items and stars go with it (foreign keys cascade).
-- name: DeleteList :exec
DELETE FROM lists WHERE id = $1;

-- LockList takes the list's row for the rest of the transaction, so the
-- writes to its items that follow (an added item's position, a sync's
-- replacement) happen one at a time per list.
-- name: LockList :one
SELECT id FROM lists WHERE id = $1 FOR UPDATE;

-- AddListItem appends a title at the end of a list; nothing when it is
-- already there (added = false). Run with the list's row locked (LockList)
-- in the same transaction: the Python read max(position) and wrote max+1 in
-- two steps, so two adds at once could take the same position. With the
-- lock, the second add waits and reads the first one's position.
-- name: AddListItem :one
WITH added AS (
    INSERT INTO list_items (list_id, media_type, tmdb_id, position, added_at)
    SELECT @list_id, @media_type, @tmdb_id,
           COALESCE((SELECT max(position) FROM list_items WHERE list_id = @list_id), 0) + 1, now()
    ON CONFLICT (list_id, media_type, tmdb_id) DO NOTHING
    RETURNING id
)
SELECT count(*)::integer AS added FROM added;

-- name: RemoveListItem :execrows
DELETE FROM list_items WHERE list_id = $1 AND media_type = $2 AND tmdb_id = $3;

-- CopyListItems copies every item of one list into another, in order.
-- name: CopyListItems :exec
INSERT INTO list_items (list_id, media_type, tmdb_id, position, added_at)
SELECT @to_list::integer, media_type, tmdb_id, (row_number() OVER (ORDER BY position, id) - 1)::integer, now()
FROM list_items WHERE list_id = @from_list::integer
ORDER BY position, id;

-- ListItemsPage is one page of a list's titles, filtered by media ("" for
-- both) and sorted (list order, newest added, title, year or TMDB rating;
-- list order breaks ties), with how many there are in all. The sort is one
-- query with CASE keys rather than five copies of it; the titles join is on
-- the titles primary key. Paged here, not after reading every item.
-- name: ListItemsPage :many
SELECT li.media_type::text AS media_type, li.tmdb_id, count(*) OVER ()::integer AS total
FROM list_items li
LEFT JOIN titles t ON t.key = li.media_type || ':' || li.tmdb_id::text
WHERE li.list_id = @list_id AND (@media::text = '' OR li.media_type = @media::text)
ORDER BY
    CASE WHEN @sort::text = 'added' THEN li.added_at END DESC,
    CASE WHEN @sort::text = 'added' THEN li.id END DESC,
    CASE WHEN @sort::text = 'title' THEN lower(t.title) END ASC NULLS LAST,
    CASE WHEN @sort::text = 'year' THEN t.release_date END DESC NULLS LAST,
    CASE WHEN @sort::text = 'rating' THEN t.vote_average END DESC NULLS LAST,
    li.position, li.id
LIMIT @lim OFFSET @off;

-- Memberships: the person's own hand-made lists, and whether a title is in each.
-- name: Memberships :many
SELECT l.id, l.name, l.source,
       EXISTS (SELECT 1 FROM list_items li WHERE li.list_id = l.id AND li.media_type = @media AND li.tmdb_id = @tmdb_id) AS in_list
FROM lists l
WHERE l.owner_id = @user_id AND l.kind = 'list'
ORDER BY l.created_at, l.id;

-- ---------------------------------------------------------------------------
-- MDBList lists
-- ---------------------------------------------------------------------------

-- name: FollowedMDBList :one
SELECT * FROM lists WHERE origin = 'mdblist' AND origin_id = $1 AND owner_id IS NULL;

-- name: FollowedMDBListIDs :many
SELECT origin_id, id FROM lists WHERE origin = 'mdblist' AND owner_id IS NULL AND origin_id IS NOT NULL;

-- name: FollowedMDBListsToSync :many
SELECT id FROM lists WHERE source = 'follow' AND origin = 'mdblist' ORDER BY id;

-- name: CreateMDBList :one
INSERT INTO lists (owner_id, name, description, kind, source, origin, origin_id, origin_user, origin_slug, likes, created_at)
VALUES (NULL, @name, @description, 'list', 'follow', 'mdblist', @origin_id, @origin_user, @origin_slug, @likes, now())
RETURNING *;

-- name: UpdateMDBListSummary :exec
UPDATE lists SET name = @name, likes = @likes, description = @description WHERE id = @id;

-- name: ListHasItems :one
SELECT EXISTS (SELECT 1 FROM list_items WHERE list_id = $1)::boolean;

-- ReplaceListItems makes a catalog list's items exactly these, in this
-- order (one JSON array of {media_type, tmdb_id, position}): the ones no
-- longer there go, the rest are inserted or moved. Run in one transaction
-- with the list locked (LockList), after DropListItemsExcept. The rows are
-- written in (media_type, tmdb_id) order, so a sync and an add on the same
-- list lock their rows in one order.
-- name: ReplaceListItems :exec
INSERT INTO list_items AS li (list_id, media_type, tmdb_id, position, added_at)
SELECT @list_id::integer, r.media_type, r.tmdb_id, r.position, now()
FROM jsonb_to_recordset(@rows::jsonb) AS r(media_type text, tmdb_id integer, position integer)
ORDER BY r.media_type, r.tmdb_id
ON CONFLICT (list_id, media_type, tmdb_id) DO UPDATE SET position = excluded.position
WHERE li.position IS DISTINCT FROM excluded.position;

-- name: DropListItemsExcept :exec
DELETE FROM list_items li
WHERE li.list_id = @list_id::integer
  AND NOT EXISTS (
    SELECT 1 FROM jsonb_to_recordset(@rows::jsonb) AS r(media_type text, tmdb_id integer)
    WHERE r.media_type = li.media_type AND r.tmdb_id = li.tmdb_id);

-- name: MarkListSynced :exec
UPDATE lists SET not_shown = @not_shown, synced_at = now() WHERE id = @id;
