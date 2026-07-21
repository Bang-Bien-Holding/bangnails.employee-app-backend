package pgerr

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestTranslate(t *testing.T) {
	ErrMapped := errors.New("mapped domain error")
	byConstraint := map[string]error{"things_name_key": ErrMapped}

	tests := []struct {
		name     string
		err      error
		code     string
		expected error
	}{
		{
			name:     "matching code and mapped constraint returns the domain error",
			err:      &pgconn.PgError{Code: UniqueViolation, ConstraintName: "things_name_key"},
			code:     UniqueViolation,
			expected: ErrMapped,
		},
		{
			name:     "matching code but unmapped constraint returns the original error",
			err:      &pgconn.PgError{Code: UniqueViolation, ConstraintName: "other_key"},
			code:     UniqueViolation,
			expected: nil,
		},
		{
			name:     "different code returns the original error untouched",
			err:      &pgconn.PgError{Code: ForeignKeyViolation, ConstraintName: "things_name_key"},
			code:     UniqueViolation,
			expected: nil,
		},
		{
			name:     "non-Postgres error returns the original error untouched",
			err:      errors.New("connection refused"),
			code:     UniqueViolation,
			expected: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Translate(tc.err, tc.code, byConstraint)

			if tc.expected != nil {
				if !errors.Is(got, tc.expected) {
					t.Errorf("expected %v, got %v", tc.expected, got)
				}
				return
			}
			if !errors.Is(got, tc.err) {
				t.Errorf("expected original error %v to pass through unchanged, got %v", tc.err, got)
			}
		})
	}
}
