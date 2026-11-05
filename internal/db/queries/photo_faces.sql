-- People in the photos gallery: faces found by the ML service, grouped into persons.
-- A card is visible when it is in the user's feed: not hidden, not in the trash.

-- Cards waiting for face detection, newest first, of users who turned people on. Only
-- cards (no live-video halves, no originals under an edit) with a rendered thumbnail.
-- name: ClaimPendingFaces :many
SELECT a.* FROM photo_assets a
JOIN photo_settings s ON s.user_id = a.user_id AND s.enabled AND s.faces_enabled
WHERE a.face_state IS NULL AND a.parent_id IS NULL AND a.thumb_state = 'ready'
  AND a.id <> ALL(COALESCE(sqlc.arg(exclude)::uuid[], '{}'))
ORDER BY a.taken_local DESC, a.id
LIMIT sqlc.arg(max_rows);

-- name: SetFaceState :exec
UPDATE photo_assets SET face_state = sqlc.narg(face_state), face_error = sqlc.narg(face_error),
    face_model = sqlc.narg(face_model)
WHERE id = sqlc.arg(id);

-- Another face model than the cards were looked at with: they go back to the queue (their
-- faces stay, with their persons, until the new ones replace them).
-- name: ResetFacesOfOtherModel :execrows
UPDATE photo_assets SET face_state = NULL, face_error = NULL
WHERE face_state IS NOT NULL AND face_model IS DISTINCT FROM sqlc.arg(model)::text;

-- name: ListAssetFaces :many
SELECT * FROM photo_faces WHERE asset_id = $1 ORDER BY id;

-- name: DeleteAssetFaces :exec
DELETE FROM photo_faces WHERE asset_id = $1;

-- name: InsertPhotoFace :one
INSERT INTO photo_faces (user_id, asset_id, person_id, x1, y1, x2, y2, score, embedding, model)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING id;

-- Every face of the user made by the current model, for a clustering pass.
-- name: ListUserFaceVectors :many
SELECT f.id, f.person_id, f.embedding, f.clustered
FROM photo_faces f
WHERE f.user_id = sqlc.arg(user_id) AND f.model = sqlc.arg(model)::text
  AND NOT (f.manual AND f.person_id IS NULL)
ORDER BY f.created_at, f.id;

-- name: CountUnclusteredFaces :one
SELECT count(*) FROM photo_faces WHERE user_id = sqlc.arg(user_id) AND model = sqlc.arg(model)::text AND NOT clustered;

-- name: CountUserFacesOfModel :one
SELECT count(*) FROM photo_faces WHERE user_id = sqlc.arg(user_id) AND model = sqlc.arg(model)::text;

-- name: MarkFacesClustered :exec
UPDATE photo_faces SET clustered = true WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: AssignFaces :exec
-- Only faces still without a person: a merge or another pass meanwhile wins.
UPDATE photo_faces SET person_id = sqlc.arg(person_id)
WHERE id = ANY(sqlc.arg(ids)::uuid[]) AND person_id IS NULL;

-- name: CreatePhotoPerson :one
INSERT INTO photo_persons (user_id) VALUES ($1) RETURNING id;

-- Persons left without faces, unless the user named or hid them.
-- name: DeleteEmptyPhotoPersons :exec
DELETE FROM photo_persons p
WHERE p.user_id = $1 AND p.name = '' AND NOT p.hidden
  AND NOT EXISTS (SELECT 1 FROM photo_faces f WHERE f.person_id = p.id);

-- Persons with at least one visible card; named first, then by the number of cards. The
-- cover is the face on a visible card with the largest score times box area, worked out
-- on read so hiding or trashing a photo changes it at once.
-- name: ListPhotoPeople :many
SELECT p.id, p.name, p.hidden, p.birth_date, c.count::bigint AS count, c.cover::uuid AS cover_face_id
FROM photo_persons p
JOIN LATERAL (
    SELECT count(DISTINCT f.asset_id) AS count,
           (array_agg(f.id ORDER BY f.score * (f.x2 - f.x1) * (f.y2 - f.y1) DESC, f.id))[1] AS cover
    FROM photo_faces f JOIN photo_assets a ON a.id = f.asset_id
    WHERE f.person_id = p.id AND NOT a.hidden AND a.trashed_at IS NULL AND a.parent_id IS NULL
) c ON true
WHERE p.user_id = sqlc.arg(user_id) AND p.hidden = sqlc.arg(hidden) AND c.count > 0
ORDER BY (p.name = ''), c.count DESC, p.name, p.id;

-- name: GetPhotoPerson :one
SELECT p.id, p.name, p.hidden, p.birth_date, c.count::bigint AS count, c.cover::uuid AS cover_face_id
FROM photo_persons p
JOIN LATERAL (
    SELECT count(DISTINCT f.asset_id) AS count,
           (array_agg(f.id ORDER BY f.score * (f.x2 - f.x1) * (f.y2 - f.y1) DESC, f.id))[1] AS cover
    FROM photo_faces f JOIN photo_assets a ON a.id = f.asset_id
    WHERE f.person_id = p.id AND NOT a.hidden AND a.trashed_at IS NULL AND a.parent_id IS NULL
) c ON true
WHERE p.id = sqlc.arg(id) AND p.user_id = sqlc.arg(user_id);

-- name: UpdatePhotoPerson :execrows
UPDATE photo_persons SET
    name = COALESCE(sqlc.narg(name)::text, name),
    hidden = COALESCE(sqlc.narg(hidden)::bool, hidden),
    birth_date = CASE WHEN sqlc.arg(set_birth_date)::bool THEN sqlc.narg(birth_date)::date ELSE birth_date END,
    updated_at = now()
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id);

-- name: SetPhotoPersonsHidden :execrows
UPDATE photo_persons SET hidden = sqlc.arg(hidden), updated_at = now()
WHERE user_id = sqlc.arg(user_id) AND id = ANY(sqlc.arg(ids)::uuid[]) AND hidden <> sqlc.arg(hidden);

-- name: MoveFacesToPerson :exec
UPDATE photo_faces SET person_id = sqlc.arg(target)
WHERE user_id = sqlc.arg(user_id) AND person_id = ANY(sqlc.arg(sources)::uuid[]);

-- name: DeletePhotoPersons :exec
DELETE FROM photo_persons WHERE user_id = sqlc.arg(user_id) AND id = ANY(sqlc.arg(ids)::uuid[]);

-- name: ListPhotoPersonsByIDs :many
SELECT * FROM photo_persons WHERE user_id = sqlc.arg(user_id) AND id = ANY(sqlc.arg(ids)::uuid[]);

-- Persons on a card, except hidden ones; the face box with each.
-- name: ListAssetPeople :many
SELECT p.id, p.name, p.birth_date, f.id AS face_id, f.x1, f.y1, f.x2, f.y2
FROM photo_faces f JOIN photo_persons p ON p.id = f.person_id
WHERE f.asset_id = $1 AND NOT p.hidden
ORDER BY f.x1, p.id;

-- A face of the user on a visible card (the face thumbnail).
-- name: GetVisiblePhotoFace :one
SELECT f.id, f.asset_id, f.x1, f.y1, f.x2, f.y2
FROM photo_faces f JOIN photo_assets a ON a.id = f.asset_id
WHERE f.id = sqlc.arg(id) AND f.user_id = sqlc.arg(user_id)
  AND NOT a.hidden AND a.trashed_at IS NULL;

-- Cards whose thumbnail failed are never looked at (the file cannot be rendered): they
-- count neither as done nor as to do.
-- name: FaceProgress :one
SELECT count(*) FILTER (WHERE a.face_state = 'done')::bigint AS done,
       count(*) FILTER (WHERE a.face_state = 'failed')::bigint AS failed,
       count(*) FILTER (WHERE a.face_state IS NULL)::bigint AS pending,
       count(*)::bigint AS total
FROM photo_assets a
WHERE a.user_id = $1 AND a.parent_id IS NULL
  AND NOT (a.face_state IS NULL AND a.thumb_state = 'failed');

-- name: SetFacesEnabled :execrows
UPDATE photo_settings SET faces_enabled = $2, updated_at = now() WHERE user_id = $1;

-- Users with people turned on (the queue's passes after a restart).
-- name: ListFacesUsers :many
SELECT user_id FROM photo_settings WHERE enabled AND faces_enabled;

-- recluster: faces leave the persons the user neither named nor hid, and every face
-- without a person is looked at again (a looser threshold may join it to a named one).
-- Only faces of the current model: the others are not clustered until they are looked at again.
-- name: UnassignUnnamedPersons :exec
UPDATE photo_faces f SET person_id = NULL, clustered = false
WHERE f.user_id = sqlc.arg(user_id) AND f.model = sqlc.arg(model)::text AND (f.person_id IS NULL OR f.person_id IN (
    SELECT p.id FROM photo_persons p WHERE p.user_id = sqlc.arg(user_id) AND p.name = '' AND NOT p.hidden));

-- redetect: every card goes back to the detection queue.
-- name: ResetFaceState :exec
UPDATE photo_assets SET face_state = NULL, face_error = NULL WHERE user_id = $1 AND parent_id IS NULL;

-- Faces on a card nobody was matched to (the info panel offers to name them).
-- name: ListAssetUnknownFaces :many
SELECT id, x1, y1, x2, y2 FROM photo_faces
WHERE asset_id = $1 AND person_id IS NULL
ORDER BY x1, id;

-- name: GetUserPhotoFace :one
SELECT id, asset_id, person_id FROM photo_faces WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id);

-- The user said who it is: the face goes to that person, whatever it had, or to nobody
-- (a pass never moves an assigned face, and leaves out one taken off, so the choice stays).
-- name: SetFacePerson :exec
UPDATE photo_faces SET person_id = sqlc.narg(person_id), clustered = true, manual = true
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id);

-- The faces of one person on the given cards go to another person, or to nobody.
-- name: MovePersonFaces :execrows
UPDATE photo_faces SET person_id = sqlc.narg(target), clustered = true, manual = true
WHERE user_id = sqlc.arg(user_id) AND person_id = sqlc.arg(source)
  AND asset_id = ANY(sqlc.arg(asset_ids)::uuid[]);
