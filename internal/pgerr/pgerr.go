// Package pgerr translates Postgres constraint violations into
// caller-supplied domain errors, by SQLSTATE code and constraint name.
package pgerr

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// SQLSTATE codes for the violation kinds Translate is used with.
const (
	UniqueViolation     = "23505"
	ForeignKeyViolation = "23503"
)

// Translate returns byConstraint[pgErr.ConstraintName] when err is a
// *pgconn.PgError with the given SQLSTATE code and a constraint name present
// in byConstraint. Otherwise it returns err unchanged — including when err
// isn't a Postgres error, has a different code, or names a constraint the
// caller didn't map.
func Translate(err error, code string, byConstraint map[string]error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != code {
		return err
	}
	if mapped, ok := byConstraint[pgErr.ConstraintName]; ok {
		return mapped
	}
	return err
}
