-- name: CreateUploadReservation :exec
INSERT INTO upload_reservations(id,user_id) VALUES($1,$2);
-- name: GetUploadReservation :one
SELECT * FROM upload_reservations WHERE id=$1;
-- name: AddUploadReservation :exec
UPDATE upload_reservations SET bytes=bytes+$2,touched_at=clock_timestamp() WHERE id=$1;
-- name: TrimUploadReservation :exec
UPDATE upload_reservations SET bytes=$2,touched_at=clock_timestamp() WHERE id=$1 AND bytes >= $2;
-- name: TouchUploadReservation :exec
UPDATE upload_reservations SET touched_at=clock_timestamp() WHERE id=$1;
-- name: DeleteUploadReservation :exec
DELETE FROM upload_reservations WHERE id=$1;
-- name: UserUploadReserved :one
SELECT COALESCE(SUM(bytes),0)::bigint FROM upload_reservations WHERE user_id=$1;
-- name: TotalUploadReserved :one
SELECT COALESCE(SUM(bytes),0)::bigint FROM upload_reservations;
-- name: ListStaleUploadReservations :many
SELECT * FROM upload_reservations WHERE touched_at < $1 ORDER BY touched_at LIMIT 256;
-- name: LockUploadQuota :exec
SELECT pg_advisory_xact_lock(749341217);
-- name: DeleteEmptyUploadReservation :exec
DELETE FROM upload_reservations WHERE id=$1 AND bytes=0;
