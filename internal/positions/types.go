package positions

//go:generate mockgen -source=types.go -destination=service_mock_test.go -package=positions

import (
	"context"
	"errors"

	repo "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc"
	"github.com/jackc/pgx/v5/pgtype"
)

// ErrPositionNotFound is returned by UpdatePosition/DeletePosition for an id
// with no matching row.
var ErrPositionNotFound = errors.New("position not found")

// ErrPositionNameAlreadyExists is returned by CreatePosition/UpdatePosition
// when name collides with an existing position (see ADR-0008 — this must
// surface as a clear client error, not a raw database constraint failure).
var ErrPositionNameAlreadyExists = errors.New("position name already exists")

// createPositionParams is the body for POST /v1/positions.
type createPositionParams struct {
	Name string `json:"name" validate:"required"`
}

// updatePositionParams is the body for PUT /v1/positions/{id} — Position
// has exactly one field, so a rename is the whole resource.
type updatePositionParams struct {
	Name string `json:"name" validate:"required"`
}

// positionResponse mirrors repo.Position for HTTP responses.
type positionResponse struct {
	ID        int64              `json:"id"`
	Name      string             `json:"name"`
	CreatedAt pgtype.Timestamptz `json:"created_at"`
	UpdatedAt pgtype.Timestamptz `json:"updated_at"`
}

func newPositionResponse(p repo.Position) positionResponse {
	return positionResponse{
		ID:        p.ID,
		Name:      p.Name,
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
	}
}

func newPositionResponses(positions []repo.Position) []positionResponse {
	responses := make([]positionResponse, len(positions))
	for i, p := range positions {
		responses[i] = newPositionResponse(p)
	}
	return responses
}

type Service interface {
	CreatePosition(ctx context.Context, params createPositionParams) (repo.Position, error)
	ListPositions(ctx context.Context) ([]repo.Position, error)
	UpdatePosition(ctx context.Context, id int64, params updatePositionParams) (repo.Position, error)
	DeletePosition(ctx context.Context, id int64) error
}
