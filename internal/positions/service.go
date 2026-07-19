package positions

import (
	"context"
	"errors"

	repo "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// uniqueViolationCode is Postgres' SQLSTATE for a unique_violation error.
const uniqueViolationCode = "23505"

// positionsNameKeyConstraint comes from
// internal/adapters/postgresql/migrations/00010_create_positions.sql
// (Postgres' default naming: <table>_<column>_key).
const positionsNameKeyConstraint = "positions_name_key"

type service struct {
	repo repo.Querier
}

func NewService(r repo.Querier) Service {
	return &service{repo: r}
}

func (s *service) CreatePosition(ctx context.Context, params createPositionParams) (repo.Position, error) {
	position, err := s.repo.CreatePosition(ctx, params.Name)
	if err != nil {
		return repo.Position{}, translatePositionUniqueViolation(err)
	}
	return position, nil
}

func (s *service) ListPositions(ctx context.Context) ([]repo.Position, error) {
	return s.repo.ListPositions(ctx)
}

func (s *service) UpdatePosition(ctx context.Context, id int64, params updatePositionParams) (repo.Position, error) {
	position, err := s.repo.UpdatePosition(ctx, repo.UpdatePositionParams{
		ID:   id,
		Name: params.Name,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return repo.Position{}, ErrPositionNotFound
		}
		return repo.Position{}, translatePositionUniqueViolation(err)
	}
	return position, nil
}

func (s *service) DeletePosition(ctx context.Context, id int64) error {
	rowsAffected, err := s.repo.DeletePosition(ctx, id)
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return ErrPositionNotFound
	}
	return nil
}

// translatePositionUniqueViolation maps a Postgres unique-violation on
// positions.name to ErrPositionNameAlreadyExists, leaving every other error
// untouched. Shared by CreatePosition and UpdatePosition.
func translatePositionUniqueViolation(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != uniqueViolationCode {
		return err
	}
	if pgErr.ConstraintName == positionsNameKeyConstraint {
		return ErrPositionNameAlreadyExists
	}
	return err
}
