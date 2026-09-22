package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"

	"discodrive/internal/quota"
	"github.com/jackc/pgx/v5/pgtype"
)

type stagedFile struct {
	rel         string
	size        int64
	hash        string
	reservation *quota.Reservation
	reused      bool
}

// stage charges ordinary uploads too: a concurrent direct upload must not spend
// the same budget that a resumable upload is reserving. Completion reuses its
// assembled file instead of duplicating it into another temporary file.
func (s *FileService) stage(ctx context.Context, owner pgtype.UUID, source io.Reader) (*stagedFile, error) {
	file := &stagedFile{rel: tmpName()}
	if id, bytes, ok := quota.Credit(ctx, owner); ok {
		file.rel = ".uploads/" + id
		file.reused = true
		hash := sha256.New()
		n, err := io.Copy(hash, source)
		if err != nil {
			return nil, err
		}
		if n != bytes {
			return nil, ErrUploadSize
		}
		file.size = n
		file.hash = hex.EncodeToString(hash.Sum(nil))
		return file, nil
	}
	if s.quota != nil {
		id := strings.TrimPrefix(file.rel, ".tmp/")
		file.rel = ".uploads/" + id
		if err := s.quota.CreateReservation(ctx, id, owner); err != nil {
			return nil, err
		}
		reservation, err := s.quota.LockReservation(ctx, id)
		if err != nil {
			s.quota.CancelEmptyReservation(id)
			return nil, err
		}
		file.reservation = reservation
		source = reservation.Reader(source)
	}
	size, hash, err := s.st.WriteFile(file.rel, source)
	if err != nil {
		file.cleanup(s.st)
		return nil, err
	}
	file.size = size
	file.hash = hash
	return file, nil
}

func (f *stagedFile) cleanup(st Storage) {
	if f.reused {
		return
	} // owned by the resumable session until publication succeeds
	err := st.Remove(f.rel)
	if f.reservation != nil {
		if err == nil {
			_ = f.reservation.Release()
		}
		f.reservation.Close()
	}
}
