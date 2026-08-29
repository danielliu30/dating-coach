package coaching

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// isSlotConflict reports whether err is the unique or the overlap-exclusion
// violation guarding a coach's calendar.
func isSlotConflict(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "23505" || pgErr.Code == "23P01"
}
