package remote

import (
	"context"

	"spaghetti/internal/user"
)

type Client interface {
	FetchAttributes(ctx context.Context, address string) (*user.Attributes, error)
	FetchAllUsers(ctx context.Context) ([]*user.User, error)
}
