package resource

import (
	"context"
	"time"
)

// ControlStore contains read-only staff-directory operations. Mutating staff
// workflows remain on the existing transaction path until their full atomic
// extraction is complete.
type ControlStore interface {
	Users(context.Context) ([]DirectoryUser, error)
}

type DirectoryUser struct {
	ID        string
	Username  string
	Role      string
	EntityID  string
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}
