// Package domain contains persistence-neutral data shared across application features.
package domain

import (
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrNotFound            = errors.New("repository record not found")
	ErrIdempotencyConflict = errors.New("idempotency request fingerprint conflict")
	ErrBatchNotCancellable = errors.New("batch is not cancellable")
	ErrBatchNotRetryable   = errors.New("batch is not retryable")
)

type Entity struct {
	ID            string    `json:"id"`
	BankReference string    `json:"bank_reference"`
	Name          string    `json:"name"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}
type Route struct {
	EntityID        string
	ShardID         string
	PlacementStatus string
	CreatedAt       time.Time
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
type CardFilter struct {
	ClientID  string
	AccountID string
	ProductID string
	Status    string
}
type Card struct {
	ID                 string     `json:"id"`
	ClientID           string     `json:"client_id"`
	AccountReferenceID string     `json:"account_reference_id"`
	ProductID          string     `json:"product_id"`
	Status             string     `json:"status"`
	PredecessorCardID  *string    `json:"predecessor_card_id"`
	IssuedAt           *time.Time `json:"issued_at"`
	ActivatedAt        *time.Time `json:"activated_at"`
	SuspendedAt        *time.Time `json:"suspended_at"`
	ClosedAt           *time.Time `json:"closed_at"`
	ExpiresAt          *time.Time `json:"expires_at"`
	Version            int64      `json:"version"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}
type CardOperation struct {
	ID          string     `json:"id"`
	CardID      string     `json:"card_id"`
	Action      string     `json:"action"`
	Status      string     `json:"status"`
	Reason      string     `json:"reason"`
	CreatedAt   time.Time  `json:"created_at"`
	CompletedAt *time.Time `json:"completed_at"`
}
type CardStatusHistory struct {
	ID             string    `json:"id"`
	PreviousStatus string    `json:"previous_status"`
	NewStatus      string    `json:"new_status"`
	Reason         string    `json:"reason"`
	CreatedAt      time.Time `json:"created_at"`
}
type CardProduct struct {
	ID                   string          `json:"id"`
	ProductCode          string          `json:"product_code"`
	Name                 string          `json:"name"`
	Status               string          `json:"status"`
	Configuration        json.RawMessage `json:"configuration"`
	ConfigurationVersion int64           `json:"configuration_version"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
}
type Client struct {
	ID                string    `json:"id"`
	ExternalClientRef string    `json:"external_client_ref"`
	DisplayName       *string   `json:"display_name"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}
type AccountReference struct {
	ID                 string    `json:"id"`
	ClientID           string    `json:"client_id"`
	ExternalAccountRef string    `json:"external_account_ref"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}
type CardStatusBatch struct {
	ID             string     `json:"id"`
	TargetStatus   string     `json:"target_status"`
	Reason         string     `json:"reason"`
	CreatedBy      string     `json:"created_by"`
	RequesterRole  string     `json:"requester_role"`
	Status         string     `json:"status"`
	ItemCount      int        `json:"item_count"`
	AppliedCount   int        `json:"applied_count"`
	IgnoredCount   int        `json:"ignored_count"`
	RetryOfBatchID *string    `json:"retry_of_batch_id"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	CompletedAt    *time.Time `json:"completed_at"`
}
type CardStatusBatchItem struct {
	ID             string    `json:"id"`
	CardID         string    `json:"card_id"`
	OperationID    string    `json:"operation_id"`
	PreviousStatus *string   `json:"previous_status"`
	Outcome        string    `json:"outcome"`
	FailureCode    *string   `json:"failure_code"`
	CreatedAt      time.Time `json:"created_at"`
}

type ShardIdempotencyClaim struct {
	Bank        string
	Scope       string
	Key         string
	Fingerprint []byte
	Now         time.Time
	ExpiresAt   time.Time
}
type ShardIdempotencyReplay struct {
	RecordID string
	Response json.RawMessage
	Status   int
	Found    bool
}
type CentralIdempotencyClaim struct {
	Scope       string
	Key         string
	Fingerprint []byte
	Now         time.Time
	ExpiresAt   time.Time
}
type CentralIdempotencyReplay struct {
	Response json.RawMessage
	Found    bool
}
type StaffUserCreate struct {
	Username     string
	PasswordHash string
	Role         string
	EntityID     string
	ActorID      string
}
type AuthenticationAudit struct {
	EventType     string
	ActorUserID   string
	SubjectUserID string
	EntityID      string
	RequestID     string
}
type CardProductCreate struct {
	ProductCode   string
	Name          string
	Status        string
	Configuration json.RawMessage
	ActorID       string
}
type CardProductPatch struct {
	Name          *string
	Status        *string
	Configuration json.RawMessage
	ActorID       string
}
type ClientCreate struct {
	ExternalClientRef string
	DisplayName       *string
	ActorID           string
}
type ClientPatch struct {
	DisplayName *string
	ActorID     string
}
type AccountReferenceCreate struct {
	ClientID           string
	ExternalAccountRef string
	ActorID            string
}
type AccountReferencePatch struct {
	ClientID string
	ActorID  string
}
type ReferenceAudit struct {
	Bank       string
	ActorID    string
	Action     string
	Kind       string
	ResourceID string
	RequestID  string
}
type CardIssue struct {
	CardID              string
	OperationID         string
	ClientID            string
	AccountReferenceID  string
	ProductID           string
	CredentialReference string
	Reason              string
	ActorID             string
	ActorRole           string
	ActorEntityID       string
	RequestID           string
}
type CardState struct {
	Status             string
	ClientID           string
	AccountReferenceID string
	ProductID          string
}
type CardReplacement struct {
	SuccessorID         string
	OperationID         string
	PredecessorID       string
	ClientID            string
	AccountReferenceID  string
	ProductID           string
	CredentialReference string
	Reason              string
	ActorID             string
	ActorRole           string
	ActorEntityID       string
	RequestID           string
}
type CardCommand struct {
	CardID                 string
	OperationID            string
	PredecessorOperationID string
	Action                 string
	PreviousStatus         string
	TargetStatus           string
	Ignored                bool
	Reason                 string
	ActorID                string
	ActorRole              string
	ActorEntityID          string
	RequestID              string
}
type EntityProvision struct {
	ID            string
	BankReference string
	Name          string
	ActorID       string
	RequestID     string
}
type EntityPatch struct {
	ID        string
	Name      *string
	Status    *string
	ActorID   string
	RequestID string
}
type CardStatusBatchDraft struct {
	ID                  string
	IdempotencyRecordID string
	TargetStatus        string
	Reason              string
	CardIDs             []string
	OperationIDs        []string
	ActorID             string
	ActorRole           string
	ActorEntityID       string
	RequestID           string
}
type CardStatusBatchRetry struct {
	ID                  string
	IdempotencyRecordID string
	OperationIDs        []string
	ActorID             string
	ActorRole           string
	ActorEntityID       string
	RequestID           string
}
