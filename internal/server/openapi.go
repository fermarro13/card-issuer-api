package server

// The types and annotation-only functions in this file describe the public
// HTTP contract. They intentionally mirror the handlers' dynamic routes
// without becoming a second routing implementation.

type statusResponse struct {
	Status string `json:"status" example:"ok"`
}

type problemResponse struct {
	Type      string `json:"type" example:"urn:card-issuer-api:error:invalid_request"`
	Title     string `json:"title" example:"Bad Request"`
	Status    int    `json:"status" example:"400"`
	Code      string `json:"code" example:"invalid_request"`
	Detail    string `json:"detail" example:"The request is invalid."`
	RequestID string `json:"request_id" format:"uuid"`
}

type userSummaryResponse struct {
	Username string `json:"username" example:"issuer_operator"`
	Status   string `json:"status" enums:"active,disabled" example:"active"`
	Role     string `json:"role" enums:"issuer_operator,issuer_readonly,bank_operator,bank_readonly" example:"issuer_operator"`
}

type tokenResponse struct {
	AccessToken string              `json:"access_token"`
	TokenType   string              `json:"token_type" example:"Bearer"`
	ExpiresAt   string              `json:"expires_at" format:"date-time"`
	User        userSummaryResponse `json:"user"`
}

type openAPILoginRequest struct {
	Username string `json:"username" example:"issuer_operator" minLength:"1"`
	Password string `json:"password" minLength:"1" maxLength:"1024"`
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password" minLength:"1"`
	NewPassword     string `json:"new_password" minLength:"13"`
}

type bankResponse struct {
	ID            string `json:"id" format:"uuid"`
	Name          string `json:"name" example:"Test Bank"`
	Status        string `json:"status" enums:"active,inactive" example:"active"`
	CreatedAt     string `json:"created_at" format:"date-time"`
	UpdatedAt     string `json:"updated_at" format:"date-time"`
	BankReference string `json:"bank_reference" example:"test-bank"`
	RoutingStatus string `json:"routing_status,omitempty" enums:"active,paused,provisioning" example:"active"`
}

type bankCollectionResponse struct {
	Data       []bankResponse `json:"data"`
	NextCursor *string        `json:"next_cursor"`
}

type createBankRequest struct {
	BankReference string `json:"bank_reference" example:"example-bank" minLength:"1"`
	Name          string `json:"name" example:"Example Bank" minLength:"1"`
}

type updateBankRequest struct {
	Name   *string `json:"name,omitempty" example:"Example Bank"`
	Status *string `json:"status,omitempty" enums:"active,inactive"`
}

type provisioningResponse struct {
	ID           string `json:"id" format:"uuid"`
	Provisioning bool   `json:"provisioning" example:"true"`
}

type staffUserResponse struct {
	ID        string  `json:"id" format:"uuid"`
	Username  string  `json:"username" example:"bank_operator"`
	Role      string  `json:"role" enums:"issuer_operator,issuer_readonly,bank_operator,bank_readonly"`
	EntityID  *string `json:"entity_id" format:"uuid"`
	Status    string  `json:"status" enums:"active,disabled"`
	CreatedAt string  `json:"created_at" format:"date-time"`
	UpdatedAt string  `json:"updated_at" format:"date-time"`
}

type staffUserCollectionResponse struct {
	Data       []staffUserResponse `json:"data"`
	NextCursor *string             `json:"next_cursor"`
}

type createUserRequest struct {
	Username string `json:"username" example:"bank_operator" minLength:"1"`
	Password string `json:"password" minLength:"13"`
	Role     string `json:"role" enums:"issuer_operator,issuer_readonly,bank_operator,bank_readonly"`
	EntityID string `json:"entity_id,omitempty" format:"uuid"`
}

type updateUserRequest struct {
	Role     string  `json:"role" enums:"issuer_operator,issuer_readonly,bank_operator,bank_readonly"`
	EntityID *string `json:"entity_id" format:"uuid"`
}

type setUserPasswordRequest struct {
	NewPassword string `json:"new_password" minLength:"13"`
}

type cardProductResponse struct {
	ID                   string                 `json:"id" format:"uuid"`
	ProductCode          string                 `json:"product_code" example:"gold"`
	Name                 string                 `json:"name" example:"Gold"`
	Status               string                 `json:"status" enums:"active,inactive"`
	Configuration        map[string]interface{} `json:"configuration"`
	ConfigurationVersion int64                  `json:"configuration_version" example:"1"`
	CreatedAt            string                 `json:"created_at" format:"date-time"`
	UpdatedAt            string                 `json:"updated_at" format:"date-time"`
}

type cardProductCollectionResponse struct {
	Data       []cardProductResponse `json:"data"`
	NextCursor *string               `json:"next_cursor"`
}
type createCardProductRequest struct {
	ProductCode   string                 `json:"product_code" example:"gold" minLength:"1"`
	Name          string                 `json:"name" example:"Gold" minLength:"1"`
	Status        string                 `json:"status,omitempty" enums:"active,inactive"`
	Configuration map[string]interface{} `json:"configuration"`
}
type updateCardProductRequest struct {
	Name          *string                `json:"name,omitempty" example:"Gold Plus"`
	Status        *string                `json:"status,omitempty" enums:"active,inactive"`
	Configuration map[string]interface{} `json:"configuration,omitempty"`
}

type clientResponse struct {
	ID                string  `json:"id" format:"uuid"`
	ExternalClientRef string  `json:"external_client_ref" example:"customer-123"`
	DisplayName       *string `json:"display_name" example:"Example Customer"`
	CreatedAt         string  `json:"created_at" format:"date-time"`
	UpdatedAt         string  `json:"updated_at" format:"date-time"`
}
type clientCollectionResponse struct {
	Data       []clientResponse `json:"data"`
	NextCursor *string          `json:"next_cursor"`
}
type createClientRequest struct {
	ExternalClientRef string  `json:"external_client_ref" example:"customer-123" minLength:"1"`
	DisplayName       *string `json:"display_name,omitempty" example:"Example Customer"`
}
type updateClientRequest struct {
	DisplayName *string `json:"display_name" example:"Example Customer"`
}

type accountReferenceResponse struct {
	ID                 string `json:"id" format:"uuid"`
	ClientID           string `json:"client_id" format:"uuid"`
	ExternalAccountRef string `json:"external_account_ref" example:"account-123"`
	CreatedAt          string `json:"created_at" format:"date-time"`
	UpdatedAt          string `json:"updated_at" format:"date-time"`
}
type accountReferenceCollectionResponse struct {
	Data       []accountReferenceResponse `json:"data"`
	NextCursor *string                    `json:"next_cursor"`
}
type createAccountReferenceRequest struct {
	ClientID           string `json:"client_id" format:"uuid"`
	ExternalAccountRef string `json:"external_account_ref" example:"account-123" minLength:"1"`
}
type updateAccountReferenceRequest struct {
	ClientID string `json:"client_id" format:"uuid"`
}

type cardResponse struct {
	ID                 string  `json:"id" format:"uuid"`
	ClientID           string  `json:"client_id" format:"uuid"`
	AccountReferenceID string  `json:"account_reference_id" format:"uuid"`
	ProductID          string  `json:"product_id" format:"uuid"`
	Status             string  `json:"status" enums:"pending,issued,active,suspended,closed,expired"`
	MaskedPAN          *string `json:"masked_pan" example:"************1234"`
	PredecessorCardID  *string `json:"predecessor_card_id" format:"uuid"`
	IssuedAt           *string `json:"issued_at" format:"date-time"`
	ActivatedAt        *string `json:"activated_at" format:"date-time"`
	SuspendedAt        *string `json:"suspended_at" format:"date-time"`
	ClosedAt           *string `json:"closed_at" format:"date-time"`
	ExpiresAt          *string `json:"expires_at" format:"date-time"`
	Version            int64   `json:"version"`
	CreatedAt          string  `json:"created_at" format:"date-time"`
	UpdatedAt          string  `json:"updated_at" format:"date-time"`
}
type cardCollectionResponse struct {
	Data       []cardResponse `json:"data"`
	NextCursor *string        `json:"next_cursor"`
}
type issueCardRequest struct {
	ClientID           string `json:"client_id" format:"uuid"`
	AccountReferenceID string `json:"account_reference_id" format:"uuid"`
	ProductID          string `json:"product_id" format:"uuid"`
	Reason             string `json:"reason" example:"Initial issuance" minLength:"1"`
}
type cardCommandRequest struct {
	Reason string `json:"reason" example:"Requested by customer" minLength:"1"`
}
type cardOperationResponse struct {
	ID          string  `json:"id" format:"uuid"`
	CardID      string  `json:"card_id" format:"uuid"`
	Action      string  `json:"action"`
	Status      string  `json:"status"`
	Reason      string  `json:"reason"`
	CreatedAt   string  `json:"created_at" format:"date-time"`
	CompletedAt *string `json:"completed_at" format:"date-time"`
}
type cardOperationCollectionResponse struct {
	Data       []cardOperationResponse `json:"data"`
	NextCursor *string                 `json:"next_cursor"`
}
type cardHistoryResponse struct {
	ID             string `json:"id" format:"uuid"`
	PreviousStatus string `json:"previous_status"`
	NewStatus      string `json:"new_status"`
	Reason         string `json:"reason"`
	CreatedAt      string `json:"created_at" format:"date-time"`
}
type cardHistoryCollectionResponse struct {
	Data       []cardHistoryResponse `json:"data"`
	NextCursor *string               `json:"next_cursor"`
}
type cardCommandResponse struct {
	Card      cardResponse          `json:"card"`
	Operation cardOperationResponse `json:"operation"`
}
type transientCredentialsResponse struct {
	PAN string `json:"pan" description:"Returned only in the initial issuance or replacement response; never persisted or repeated."`
	CVV string `json:"cvv" description:"Returned only in the initial issuance or replacement response; never persisted or repeated."`
}
type cardIssuanceResponse struct {
	Card        cardResponse                 `json:"card"`
	Operation   cardOperationResponse        `json:"operation"`
	Credentials transientCredentialsResponse `json:"credentials"`
}

type cardStatusBatchResponse struct {
	ID             string  `json:"id" format:"uuid"`
	TargetStatus   string  `json:"target_status" enums:"active,suspended,closed"`
	Reason         string  `json:"reason"`
	CreatedBy      string  `json:"created_by" format:"uuid"`
	RequesterRole  string  `json:"requester_role"`
	Status         string  `json:"status" enums:"draft,queued,processing,completed,failed,cancelled"`
	ItemCount      int     `json:"item_count"`
	AppliedCount   int     `json:"applied_count"`
	IgnoredCount   int     `json:"ignored_count"`
	FailureCode    *string `json:"failure_code,omitempty"`
	FailureSummary *string `json:"failure_summary,omitempty"`
	RetryOfBatchID *string `json:"retry_of_batch_id" format:"uuid"`
	CreatedAt      string  `json:"created_at" format:"date-time"`
	UpdatedAt      string  `json:"updated_at" format:"date-time"`
	CompletedAt    *string `json:"completed_at" format:"date-time"`
}
type cardStatusBatchItemResponse struct {
	ID             string  `json:"id" format:"uuid"`
	CardID         string  `json:"card_id" format:"uuid"`
	OperationID    string  `json:"operation_id" format:"uuid"`
	PreviousStatus *string `json:"previous_status"`
	Outcome        string  `json:"outcome"`
	FailureCode    *string `json:"failure_code"`
	CreatedAt      string  `json:"created_at" format:"date-time"`
}
type cardStatusBatchCollectionResponse struct {
	Data       []cardStatusBatchResponse `json:"data"`
	NextCursor *string                   `json:"next_cursor"`
}
type cardStatusBatchItemCollectionResponse struct {
	Data       []cardStatusBatchItemResponse `json:"data"`
	NextCursor *string                       `json:"next_cursor"`
}
type cardStatusBatchDetailResponse struct {
	cardStatusBatchResponse
	Items []cardStatusBatchItemResponse `json:"items,omitempty"`
	Links map[string]string             `json:"links"`
}
type createCardStatusBatchRequest struct {
	TargetStatus string   `json:"target_status" enums:"active,suspended,closed"`
	Reason       string   `json:"reason" example:"Fraud review" minLength:"1"`
	CardIDs      []string `json:"card_ids" minLength:"1" maxLength:"200"`
}
type emptyCommandRequest struct{}
type pendingResponse struct {
	ID     string `json:"id" format:"uuid"`
	Status string `json:"status" example:"pending"`
}

// LiveHealth godoc
// @Summary Liveness probe
// @Tags Health
// @Produce json
// @Success 200 {object} statusResponse
// @Router /health/live [get]
func LiveHealth() {}

// ReadyHealth godoc
// @Summary Readiness probe
// @Tags Health
// @Produce json
// @Success 200 {object} statusResponse
// @Failure 503 {object} statusResponse
// @Router /health/ready [get]
func ReadyHealth() {}

// Login godoc
// @Summary Log in
// @Description A direct source IP may make up to five attempts in one minute. Rate-limited requests include Retry-After.
// @Tags Authentication
// @Accept json
// @Produce json
// @Param X-Request-ID header string false "Optional request UUID"
// @Param request body openAPILoginRequest true "Credentials; password is limited to 1,024 UTF-8 bytes"
// @Success 200 {object} tokenResponse
// @Failure 400 {object} problemResponse
// @Failure 401 {object} problemResponse
// @Failure 429 {object} problemResponse
// @Router /v1/auth/login [post]
func Login() {}

// RefreshAccessToken godoc
// @Summary Refresh an access token
// @Description Requires the HttpOnly refresh cookie issued by login.
// @Tags Authentication
// @Produce json
// @Param X-Request-ID header string false "Optional request UUID"
// @Success 200 {object} tokenResponse
// @Failure 401 {object} problemResponse
// @Router /v1/auth/refresh [post]
func RefreshAccessToken() {}

// Logout godoc
// @Summary Log out
// @Description Revokes the refresh session when its cookie is present and always clears the cookie.
// @Tags Authentication
// @Param X-Request-ID header string false "Optional request UUID"
// @Success 204
// @Failure 500 {object} problemResponse
// @Router /v1/auth/logout [post]
func Logout() {}

// CurrentUser godoc
// @Summary Get the current user
// @Tags Authentication
// @Produce json
// @Param X-Request-ID header string false "Optional request UUID"
// @Success 200 {object} userSummaryResponse
// @Failure 401 {object} problemResponse
// @Security BearerAuth
// @Router /v1/me [get]
func CurrentUser() {}

// ChangePassword godoc
// @Summary Change the current user's password
// @Tags Authentication
// @Accept json
// @Param X-Request-ID header string false "Optional request UUID"
// @Param request body changePasswordRequest true "Current and new password"
// @Success 204
// @Failure 400 {object} problemResponse
// @Failure 401 {object} problemResponse
// @Security BearerAuth
// @Router /v1/me/password [post]
func ChangePassword() {}

// ListBanks godoc
// @Summary List banks
// @Tags Banks
// @Produce json
// @Param page_size query int false "Items per page" minimum(1) maximum(200)
// @Param cursor query string false "Pagination cursor"
// @Success 200 {object} bankCollectionResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Security BearerAuth
// @Router /v1/banks [get]
func ListBanks() {}

// CreateBank godoc
// @Summary Provision a bank
// @Tags Banks
// @Accept json
// @Produce json
// @Param Idempotency-Key header string true "Unique idempotency key" minLength(1) maxLength(256)
// @Param request body createBankRequest true "Bank to provision"
// @Success 201 {object} bankResponse
// @Success 202 {object} provisioningResponse
// @Failure 400 {object} problemResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Failure 409 {object} problemResponse
// @Security BearerAuth
// @Router /v1/banks [post]
func CreateBank() {}

// GetBank godoc
// @Summary Get a bank
// @Tags Banks
// @Produce json
// @Param bankID path string true "Bank UUID" format(uuid)
// @Success 200 {object} bankResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Failure 404 {object} problemResponse
// @Security BearerAuth
// @Router /v1/banks/{bankID} [get]
func GetBank() {}

// UpdateBank godoc
// @Summary Update a bank
// @Tags Banks
// @Accept json
// @Produce json
// @Param bankID path string true "Bank UUID" format(uuid)
// @Param Idempotency-Key header string true "Unique idempotency key" minLength(1) maxLength(256)
// @Param request body updateBankRequest true "Mutable bank attributes"
// @Success 200 {object} bankResponse
// @Failure 400 {object} problemResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Failure 404 {object} problemResponse
// @Failure 409 {object} problemResponse
// @Security BearerAuth
// @Router /v1/banks/{bankID} [patch]
func UpdateBank() {}

// ListUsers godoc
// @Summary List staff users
// @Tags Staff
// @Produce json
// @Param page_size query int false "Items per page" minimum(1) maximum(200)
// @Param cursor query string false "Pagination cursor"
// @Success 200 {object} staffUserCollectionResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Security BearerAuth
// @Router /v1/users [get]
func ListUsers() {}

// CreateUser godoc
// @Summary Create a staff user
// @Tags Staff
// @Accept json
// @Produce json
// @Param Idempotency-Key header string true "Unique idempotency key" minLength(1) maxLength(256)
// @Param request body createUserRequest true "Staff user"
// @Success 201 {object} staffUserResponse
// @Failure 400 {object} problemResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Failure 409 {object} problemResponse
// @Security BearerAuth
// @Router /v1/users [post]
func CreateUser() {}

// GetUser godoc
// @Summary Get a staff user
// @Tags Staff
// @Produce json
// @Param userID path string true "User UUID" format(uuid)
// @Success 200 {object} staffUserResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Failure 404 {object} problemResponse
// @Security BearerAuth
// @Router /v1/users/{userID} [get]
func GetUser() {}

// UpdateUser godoc
// @Summary Update a staff user's role
// @Tags Staff
// @Accept json
// @Produce json
// @Param userID path string true "User UUID" format(uuid)
// @Param Idempotency-Key header string true "Unique idempotency key" minLength(1) maxLength(256)
// @Param request body updateUserRequest true "Role and bank assignment"
// @Success 200 {object} staffUserResponse
// @Failure 400 {object} problemResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Failure 404 {object} problemResponse
// @Failure 409 {object} problemResponse
// @Security BearerAuth
// @Router /v1/users/{userID} [patch]
func UpdateUser() {}

// DisableUser godoc
// @Summary Disable a staff user
// @Tags Staff
// @Produce json
// @Param userID path string true "User UUID" format(uuid)
// @Param Idempotency-Key header string true "Unique idempotency key" minLength(1) maxLength(256)
// @Success 200 {object} staffUserResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Failure 404 {object} problemResponse
// @Failure 409 {object} problemResponse
// @Security BearerAuth
// @Router /v1/users/{userID}:disable [post]
func DisableUser() {}

// SetUserPassword godoc
// @Summary Set a staff user's password
// @Tags Staff
// @Accept json
// @Produce json
// @Param userID path string true "User UUID" format(uuid)
// @Param Idempotency-Key header string true "Unique idempotency key" minLength(1) maxLength(256)
// @Param request body setUserPasswordRequest true "New password"
// @Success 200 {object} staffUserResponse
// @Failure 400 {object} problemResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Failure 404 {object} problemResponse
// @Failure 409 {object} problemResponse
// @Security BearerAuth
// @Router /v1/users/{userID}:set-password [post]
func SetUserPassword() {}

// ListCardProducts godoc
// @Summary List card products
// @Tags Reference data
// @Produce json
// @Param bankID path string true "Bank UUID" format(uuid)
// @Param page_size query int false "Items per page" minimum(1) maximum(200)
// @Param cursor query string false "Pagination cursor"
// @Success 200 {object} cardProductCollectionResponse
// @Failure 400 {object} problemResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Security BearerAuth
// @Router /v1/banks/{bankID}/card-products [get]
func ListCardProducts() {}

// CreateCardProduct godoc
// @Summary Create a card product
// @Tags Reference data
// @Accept json
// @Produce json
// @Param bankID path string true "Bank UUID" format(uuid)
// @Param Idempotency-Key header string true "Unique idempotency key" minLength(1) maxLength(256)
// @Param request body createCardProductRequest true "Card product"
// @Success 201 {object} cardProductResponse
// @Failure 400 {object} problemResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Failure 409 {object} problemResponse
// @Security BearerAuth
// @Router /v1/banks/{bankID}/card-products [post]
func CreateCardProduct() {}

// GetCardProduct godoc
// @Summary Get a card product
// @Tags Reference data
// @Produce json
// @Param bankID path string true "Bank UUID" format(uuid)
// @Param productID path string true "Card product UUID" format(uuid)
// @Success 200 {object} cardProductResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Failure 404 {object} problemResponse
// @Security BearerAuth
// @Router /v1/banks/{bankID}/card-products/{productID} [get]
func GetCardProduct() {}

// UpdateCardProduct godoc
// @Summary Update a card product
// @Tags Reference data
// @Accept json
// @Produce json
// @Param bankID path string true "Bank UUID" format(uuid)
// @Param productID path string true "Card product UUID" format(uuid)
// @Param Idempotency-Key header string true "Unique idempotency key" minLength(1) maxLength(256)
// @Param request body updateCardProductRequest true "Mutable product attributes"
// @Success 200 {object} cardProductResponse
// @Failure 400 {object} problemResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Failure 404 {object} problemResponse
// @Failure 409 {object} problemResponse
// @Security BearerAuth
// @Router /v1/banks/{bankID}/card-products/{productID} [patch]
func UpdateCardProduct() {}

// ListClients godoc
// @Summary List clients
// @Tags Reference data
// @Produce json
// @Param bankID path string true "Bank UUID" format(uuid)
// @Param page_size query int false "Items per page" minimum(1) maximum(200)
// @Param cursor query string false "Pagination cursor"
// @Success 200 {object} clientCollectionResponse
// @Failure 400 {object} problemResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Security BearerAuth
// @Router /v1/banks/{bankID}/clients [get]
func ListClients() {}

// CreateClient godoc
// @Summary Create a client
// @Tags Reference data
// @Accept json
// @Produce json
// @Param bankID path string true "Bank UUID" format(uuid)
// @Param Idempotency-Key header string true "Unique idempotency key" minLength(1) maxLength(256)
// @Param request body createClientRequest true "Client"
// @Success 201 {object} clientResponse
// @Failure 400 {object} problemResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Failure 409 {object} problemResponse
// @Security BearerAuth
// @Router /v1/banks/{bankID}/clients [post]
func CreateClient() {}

// GetClient godoc
// @Summary Get a client
// @Tags Reference data
// @Produce json
// @Param bankID path string true "Bank UUID" format(uuid)
// @Param clientID path string true "Client UUID" format(uuid)
// @Success 200 {object} clientResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Failure 404 {object} problemResponse
// @Security BearerAuth
// @Router /v1/banks/{bankID}/clients/{clientID} [get]
func GetClient() {}

// UpdateClient godoc
// @Summary Update a client
// @Tags Reference data
// @Accept json
// @Produce json
// @Param bankID path string true "Bank UUID" format(uuid)
// @Param clientID path string true "Client UUID" format(uuid)
// @Param Idempotency-Key header string true "Unique idempotency key" minLength(1) maxLength(256)
// @Param request body updateClientRequest true "Mutable client attributes"
// @Success 200 {object} clientResponse
// @Failure 400 {object} problemResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Failure 404 {object} problemResponse
// @Failure 409 {object} problemResponse
// @Security BearerAuth
// @Router /v1/banks/{bankID}/clients/{clientID} [patch]
func UpdateClient() {}

// ListAccountReferences godoc
// @Summary List account references
// @Tags Reference data
// @Produce json
// @Param bankID path string true "Bank UUID" format(uuid)
// @Param page_size query int false "Items per page" minimum(1) maximum(200)
// @Param cursor query string false "Pagination cursor"
// @Success 200 {object} accountReferenceCollectionResponse
// @Failure 400 {object} problemResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Security BearerAuth
// @Router /v1/banks/{bankID}/account-references [get]
func ListAccountReferences() {}

// CreateAccountReference godoc
// @Summary Create an account reference
// @Tags Reference data
// @Accept json
// @Produce json
// @Param bankID path string true "Bank UUID" format(uuid)
// @Param Idempotency-Key header string true "Unique idempotency key" minLength(1) maxLength(256)
// @Param request body createAccountReferenceRequest true "Account reference"
// @Success 201 {object} accountReferenceResponse
// @Failure 400 {object} problemResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Failure 409 {object} problemResponse
// @Security BearerAuth
// @Router /v1/banks/{bankID}/account-references [post]
func CreateAccountReference() {}

// GetAccountReference godoc
// @Summary Get an account reference
// @Tags Reference data
// @Produce json
// @Param bankID path string true "Bank UUID" format(uuid)
// @Param accountReferenceID path string true "Account reference UUID" format(uuid)
// @Success 200 {object} accountReferenceResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Failure 404 {object} problemResponse
// @Security BearerAuth
// @Router /v1/banks/{bankID}/account-references/{accountReferenceID} [get]
func GetAccountReference() {}

// UpdateAccountReference godoc
// @Summary Rebind an account reference
// @Tags Reference data
// @Accept json
// @Produce json
// @Param bankID path string true "Bank UUID" format(uuid)
// @Param accountReferenceID path string true "Account reference UUID" format(uuid)
// @Param Idempotency-Key header string true "Unique idempotency key" minLength(1) maxLength(256)
// @Param request body updateAccountReferenceRequest true "New client assignment"
// @Success 200 {object} accountReferenceResponse
// @Failure 400 {object} problemResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Failure 404 {object} problemResponse
// @Failure 409 {object} problemResponse
// @Security BearerAuth
// @Router /v1/banks/{bankID}/account-references/{accountReferenceID} [patch]
func UpdateAccountReference() {}

// ListCards godoc
// @Summary List cards
// @Tags Cards
// @Produce json
// @Param bankID path string true "Bank UUID" format(uuid)
// @Param client_id query string false "Client UUID" format(uuid)
// @Param account_reference_id query string false "Account reference UUID" format(uuid)
// @Param product_id query string false "Card product UUID" format(uuid)
// @Param status query string false "Card status" Enums(pending,issued,active,suspended,closed,expired)
// @Param page_size query int false "Items per page" minimum(1) maximum(200)
// @Param cursor query string false "Pagination cursor"
// @Success 200 {object} cardCollectionResponse
// @Failure 400 {object} problemResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Security BearerAuth
// @Router /v1/banks/{bankID}/cards [get]
func ListCards() {}

// IssueCard godoc
// @Summary Issue a card
// @Description The PAN and CVV are returned only once in this initial response and have no example values in this catalog.
// @Tags Cards
// @Accept json
// @Produce json
// @Param bankID path string true "Bank UUID" format(uuid)
// @Param Idempotency-Key header string true "Unique idempotency key" minLength(1) maxLength(256)
// @Param request body issueCardRequest true "Card to issue"
// @Success 201 {object} cardIssuanceResponse
// @Failure 400 {object} problemResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Failure 409 {object} problemResponse
// @Security BearerAuth
// @Router /v1/banks/{bankID}/cards [post]
func IssueCard() {}

// GetCard godoc
// @Summary Get a card
// @Tags Cards
// @Produce json
// @Param bankID path string true "Bank UUID" format(uuid)
// @Param cardID path string true "Card UUID" format(uuid)
// @Success 200 {object} cardResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Failure 404 {object} problemResponse
// @Security BearerAuth
// @Router /v1/banks/{bankID}/cards/{cardID} [get]
func GetCard() {}

// ListCardOperations godoc
// @Summary List card operations
// @Tags Cards
// @Produce json
// @Param bankID path string true "Bank UUID" format(uuid)
// @Param cardID path string true "Card UUID" format(uuid)
// @Param page_size query int false "Items per page" minimum(1) maximum(200)
// @Param cursor query string false "Pagination cursor"
// @Success 200 {object} cardOperationCollectionResponse
// @Failure 400 {object} problemResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Failure 404 {object} problemResponse
// @Security BearerAuth
// @Router /v1/banks/{bankID}/cards/{cardID}/operations [get]
func ListCardOperations() {}

// ListCardHistory godoc
// @Summary List card status history
// @Tags Cards
// @Produce json
// @Param bankID path string true "Bank UUID" format(uuid)
// @Param cardID path string true "Card UUID" format(uuid)
// @Param page_size query int false "Items per page" minimum(1) maximum(200)
// @Param cursor query string false "Pagination cursor"
// @Success 200 {object} cardHistoryCollectionResponse
// @Failure 400 {object} problemResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Failure 404 {object} problemResponse
// @Security BearerAuth
// @Router /v1/banks/{bankID}/cards/{cardID}/history [get]
func ListCardHistory() {}

// ActivateCard godoc
// @Summary Activate a card
// @Tags Cards
// @Accept json
// @Produce json
// @Param bankID path string true "Bank UUID" format(uuid)
// @Param cardID path string true "Card UUID" format(uuid)
// @Param Idempotency-Key header string true "Unique idempotency key" minLength(1) maxLength(256)
// @Param request body cardCommandRequest true "Activation reason"
// @Success 200 {object} cardCommandResponse
// @Failure 400 {object} problemResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Failure 404 {object} problemResponse
// @Failure 409 {object} problemResponse
// @Security BearerAuth
// @Router /v1/banks/{bankID}/cards/{cardID}:activate [post]
func ActivateCard() {}

// SuspendCard godoc
// @Summary Suspend a card
// @Tags Cards
// @Accept json
// @Produce json
// @Param bankID path string true "Bank UUID" format(uuid)
// @Param cardID path string true "Card UUID" format(uuid)
// @Param Idempotency-Key header string true "Unique idempotency key" minLength(1) maxLength(256)
// @Param request body cardCommandRequest true "Suspension reason"
// @Success 200 {object} cardCommandResponse
// @Failure 400 {object} problemResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Failure 404 {object} problemResponse
// @Failure 409 {object} problemResponse
// @Security BearerAuth
// @Router /v1/banks/{bankID}/cards/{cardID}:suspend [post]
func SuspendCard() {}

// ResumeCard godoc
// @Summary Resume a suspended card
// @Tags Cards
// @Accept json
// @Produce json
// @Param bankID path string true "Bank UUID" format(uuid)
// @Param cardID path string true "Card UUID" format(uuid)
// @Param Idempotency-Key header string true "Unique idempotency key" minLength(1) maxLength(256)
// @Param request body cardCommandRequest true "Resumption reason"
// @Success 200 {object} cardCommandResponse
// @Failure 400 {object} problemResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Failure 404 {object} problemResponse
// @Failure 409 {object} problemResponse
// @Security BearerAuth
// @Router /v1/banks/{bankID}/cards/{cardID}:resume [post]
func ResumeCard() {}

// CloseCard godoc
// @Summary Close a card
// @Tags Cards
// @Accept json
// @Produce json
// @Param bankID path string true "Bank UUID" format(uuid)
// @Param cardID path string true "Card UUID" format(uuid)
// @Param Idempotency-Key header string true "Unique idempotency key" minLength(1) maxLength(256)
// @Param request body cardCommandRequest true "Closure reason"
// @Success 200 {object} cardCommandResponse
// @Failure 400 {object} problemResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Failure 404 {object} problemResponse
// @Failure 409 {object} problemResponse
// @Security BearerAuth
// @Router /v1/banks/{bankID}/cards/{cardID}:close [post]
func CloseCard() {}

// ReplaceCard godoc
// @Summary Replace a card
// @Description The PAN and CVV are returned only once in this initial response and have no example values in this catalog.
// @Tags Cards
// @Accept json
// @Produce json
// @Param bankID path string true "Bank UUID" format(uuid)
// @Param cardID path string true "Card UUID" format(uuid)
// @Param Idempotency-Key header string true "Unique idempotency key" minLength(1) maxLength(256)
// @Param request body cardCommandRequest true "Replacement reason"
// @Success 201 {object} cardIssuanceResponse
// @Failure 400 {object} problemResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Failure 404 {object} problemResponse
// @Failure 409 {object} problemResponse
// @Security BearerAuth
// @Router /v1/banks/{bankID}/cards/{cardID}:replace [post]
func ReplaceCard() {}

// ListCardStatusBatches godoc
// @Summary List card-status batches
// @Tags Card-status batches
// @Produce json
// @Param bankID path string true "Bank UUID" format(uuid)
// @Param page_size query int false "Items per page" minimum(1) maximum(200)
// @Param cursor query string false "Pagination cursor"
// @Success 200 {object} cardStatusBatchCollectionResponse
// @Failure 400 {object} problemResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Security BearerAuth
// @Router /v1/banks/{bankID}/card-status-batches [get]
func ListCardStatusBatches() {}

// CreateCardStatusBatch godoc
// @Summary Create a card-status batch draft
// @Tags Card-status batches
// @Accept json
// @Produce json
// @Param bankID path string true "Bank UUID" format(uuid)
// @Param Idempotency-Key header string true "Unique idempotency key" minLength(1) maxLength(256)
// @Param request body createCardStatusBatchRequest true "Draft batch"
// @Success 201 {object} cardStatusBatchDetailResponse
// @Success 202 {object} map[string]string "An identical request is still processing"
// @Failure 400 {object} problemResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Failure 409 {object} problemResponse
// @Failure 422 {object} problemResponse
// @Security BearerAuth
// @Router /v1/banks/{bankID}/card-status-batches [post]
func CreateCardStatusBatch() {}

// GetCardStatusBatch godoc
// @Summary Get a card-status batch
// @Tags Card-status batches
// @Produce json
// @Param bankID path string true "Bank UUID" format(uuid)
// @Param batchID path string true "Batch UUID" format(uuid)
// @Success 200 {object} cardStatusBatchDetailResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Failure 404 {object} problemResponse
// @Security BearerAuth
// @Router /v1/banks/{bankID}/card-status-batches/{batchID} [get]
func GetCardStatusBatch() {}

// ListCardStatusBatchItems godoc
// @Summary List card-status batch items
// @Tags Card-status batches
// @Produce json
// @Param bankID path string true "Bank UUID" format(uuid)
// @Param batchID path string true "Batch UUID" format(uuid)
// @Param page_size query int false "Items per page" minimum(1) maximum(200)
// @Param cursor query string false "Pagination cursor"
// @Success 200 {object} cardStatusBatchItemCollectionResponse
// @Failure 400 {object} problemResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Failure 404 {object} problemResponse
// @Security BearerAuth
// @Router /v1/banks/{bankID}/card-status-batches/{batchID}/items [get]
func ListCardStatusBatchItems() {}

// ExecuteCardStatusBatch godoc
// @Summary Queue a card-status batch for execution
// @Tags Card-status batches
// @Accept json
// @Produce json
// @Param bankID path string true "Bank UUID" format(uuid)
// @Param batchID path string true "Batch UUID" format(uuid)
// @Param Idempotency-Key header string true "Unique idempotency key" minLength(1) maxLength(256)
// @Param request body emptyCommandRequest true "Empty command object"
// @Success 202 {object} cardStatusBatchDetailResponse
// @Failure 400 {object} problemResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Failure 404 {object} problemResponse
// @Failure 409 {object} problemResponse
// @Security BearerAuth
// @Router /v1/banks/{bankID}/card-status-batches/{batchID}:execute [post]
func ExecuteCardStatusBatch() {}

// CancelCardStatusBatch godoc
// @Summary Cancel a card-status batch
// @Tags Card-status batches
// @Accept json
// @Produce json
// @Param bankID path string true "Bank UUID" format(uuid)
// @Param batchID path string true "Batch UUID" format(uuid)
// @Param Idempotency-Key header string true "Unique idempotency key" minLength(1) maxLength(256)
// @Param request body emptyCommandRequest true "Empty command object"
// @Success 200 {object} cardStatusBatchDetailResponse
// @Failure 400 {object} problemResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Failure 404 {object} problemResponse
// @Failure 409 {object} problemResponse
// @Security BearerAuth
// @Router /v1/banks/{bankID}/card-status-batches/{batchID}:cancel [post]
func CancelCardStatusBatch() {}

// RetryCardStatusBatch godoc
// @Summary Create a retry draft from a terminal batch
// @Tags Card-status batches
// @Accept json
// @Produce json
// @Param bankID path string true "Bank UUID" format(uuid)
// @Param batchID path string true "Batch UUID" format(uuid)
// @Param Idempotency-Key header string true "Unique idempotency key" minLength(1) maxLength(256)
// @Param request body emptyCommandRequest true "Empty command object"
// @Success 201 {object} cardStatusBatchDetailResponse
// @Failure 400 {object} problemResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Failure 404 {object} problemResponse
// @Failure 409 {object} problemResponse
// @Security BearerAuth
// @Router /v1/banks/{bankID}/card-status-batches/{batchID}:retry [post]
func RetryCardStatusBatch() {}

// RetryExpiryItem godoc
// @Summary Retry an expiry-run item
// @Tags Expiry
// @Accept json
// @Produce json
// @Param bankID path string true "Bank UUID" format(uuid)
// @Param expiryRunID path string true "Expiry run UUID" format(uuid)
// @Param expiryItemID path string true "Expiry item UUID" format(uuid)
// @Param Idempotency-Key header string true "Unique idempotency key" minLength(1) maxLength(256)
// @Param request body cardCommandRequest true "Retry reason"
// @Success 202 {object} pendingResponse
// @Failure 400 {object} problemResponse
// @Failure 401 {object} problemResponse
// @Failure 403 {object} problemResponse
// @Failure 404 {object} problemResponse
// @Failure 409 {object} problemResponse
// @Security BearerAuth
// @Router /v1/banks/{bankID}/card-expiry-runs/{expiryRunID}/items/{expiryItemID}:retry [post]
func RetryExpiryItem() {}
