package auth

import (
	"context"
	"time"
)

// Store is the persistence boundary consumed by Service. Implementations own
// database drivers, SQL, and transaction lifecycle details.
type Store interface {
	Begin(context.Context) (Transaction, error)
	LookupRefresh(context.Context, []byte) (RefreshLookup, error)
}

// Transaction exposes only authentication persistence operations. It keeps
// pgx types out of the service layer while preserving transaction boundaries.
type Transaction interface {
	Commit(context.Context) error
	Rollback(context.Context) error
	UserByUsername(context.Context, string, bool) (UserRecord, error)
	UserByID(context.Context, string, bool) (UserRecord, error)
	Session(context.Context, string, bool) (SessionRecord, error)
	RefreshToken(context.Context, string, string, bool) (RefreshTokenRecord, error)
	CreateSession(context.Context, string, string, int64) (time.Time, error)
	CreateRefreshToken(context.Context, string, string, []byte, string, time.Time) error
	RevokeSession(context.Context, string, time.Time, string) error
	ConsumeRefreshToken(context.Context, string, time.Time) error
	TouchSession(context.Context, string, time.Time) error
	UpdatePassword(context.Context, string, string) error
	RecordAudit(context.Context, AuditEvent) error
}

type RefreshLookup struct {
	TokenID   string
	SessionID string
	UserID    string
}

type SessionRecord struct {
	ID          string
	AuthVersion int64
	ExpiresAt   time.Time
	Revoked     bool
}

type RefreshTokenRecord struct {
	ID        string
	ExpiresAt time.Time
	Consumed  bool
}

type AuditEvent struct {
	EventType string
	Outcome   string
	ActorID   string
	SubjectID string
	EntityID  string
	SessionID string
	RequestID string
	Details   map[string]string
}
