package wagering

import (
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/estrategiahq/pedro-test/app/src/entities"
	persistenceiface "github.com/estrategiahq/pedro-test/app/src/interfaces/persistence"
	wageringiface "github.com/estrategiahq/pedro-test/app/src/interfaces/wagering"
	"github.com/estrategiahq/pedro-test/app/src/libs/middleware"
	"github.com/estrategiahq/pedro-test/app/src/structs"
)

// idempotencyKeyHeader carries the key a provider chose for an operation. The server never
// derives or replaces it: a key that is not the one the provider sent could not be told apart
// from a different operation.
const idempotencyKeyHeader = "Idempotency-Key"

// Handler serves the wagering routes.
type Handler struct {
	service wageringiface.Service
}

// NewHandler builds the handler.
func NewHandler(service wageringiface.Service) *Handler {
	return &Handler{service: service}
}

// ServerRoutes registers the wagering domain on the HTTP server. Submitting an operation moves
// money, so it asks for authentication and for the provider role; which provider the caller may
// act for is decided from the token inside the handler, because it depends on the request.
func ServerRoutes(e *echo.Echo, h *Handler, requireAuthentication, requireProvider echo.MiddlewareFunc) {
	e.POST("/wagering/transactions", h.Submit, requireAuthentication, requireProvider)
	e.GET("/wagering/transactions/:transactionId", h.Get, requireAuthentication, requireProvider)
	e.GET("/providers/:providerId/wagering/transactions/:externalTransactionId", h.GetByExternal, requireAuthentication, requireProvider)
}

// SubmitRequest is the body of an operation.
type SubmitRequest struct {
	ProviderID                     string           `json:"providerId" example:"provider-a"`
	ExternalTransactionID          string           `json:"externalTransactionId" example:"transaction-123"`
	PlayerID                       string           `json:"playerId" example:"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"`
	WalletID                       string           `json:"walletId" example:"0192f291-27dd-7d3f-8071-5f8685deef37"`
	RoundID                        string           `json:"roundId" example:"round-987"`
	GameID                         string           `json:"gameId" example:"fortune-chimp"`
	Kind                           string           `json:"kind" enums:"BET,WIN,LOSS,REFUND,ROLLBACK" example:"BET"`
	Money                          structs.MoneyDTO `json:"money"`
	ReferenceExternalTransactionID string           `json:"referenceExternalTransactionId,omitempty" example:"transaction-122"`
}

// TransactionResponse is what an operation came to. The balance is the one observed when the
// operation was applied, so a replay answers the same however much the wallet has moved since.
type TransactionResponse struct {
	TransactionID    string            `json:"transactionId" example:"0192f298-345e-7e38-af88-e43f851a819d"`
	Status           string            `json:"status" enums:"PROCESSED,REJECTED,PENDING_REFERENCE,FAILED" example:"PROCESSED"`
	Balance          *structs.MoneyDTO `json:"balance,omitempty"`
	FailureCode      string            `json:"failureCode,omitempty" example:"INSUFFICIENT_FUNDS"`
	IdempotentReplay bool              `json:"idempotentReplay" example:"false"`
}

// Submit applies an operation of a game provider to a wallet.
//
//	@Summary		Envia uma operação de um provedor
//	@Description	Aplica BET, WIN, LOSS, REFUND ou ROLLBACK a uma carteira, uma única vez, por mais que seja reenviada. O header Idempotency-Key é obrigatório e nunca é substituído pelo servidor. Mesma chave e mesmo conteúdo devolvem o resultado gravado, com o saldo observado no processamento original (200 ou 422, com idempotentReplay true). Mesma chave com outro conteúdo, ou a mesma operação sob outra chave, é 409. O providerId do corpo precisa ser o do token.
//	@Description	Distinguível pelo status: 200 processada, 202 aguardando referência, 400 entrada inválida (nada gravado), 409 conflito de idempotência, 422 rejeição de negócio (com failureCode, gravada e reproduzível), 503 indisponibilidade transitória (tente de novo).
//	@Tags			wagering
//	@Accept			json
//	@Produce		json
//	@Param			Idempotency-Key	header		string				true	"Chave escolhida pelo provedor, por exemplo provider-a:transaction-123"
//	@Param			request			body		SubmitRequest		true	"A operação"
//	@Success		200				{object}	TransactionResponse
//	@Success		202				{object}	TransactionResponse
//	@Failure		400				{object}	structs.APIError
//	@Failure		401				{object}	structs.APIError
//	@Failure		403				{object}	structs.APIError
//	@Failure		409				{object}	structs.APIError
//	@Failure		422				{object}	TransactionResponse
//	@Failure		503				{object}	structs.APIError
//	@Security		OAuth2Password
//	@Router			/wagering/transactions [post]
func (h *Handler) Submit(c echo.Context) error {
	operation, err := readOperation(c)
	if err != nil {
		return err
	}
	outcome, err := h.service.Submit(c.Request().Context(), operation)
	if err != nil {
		return httpError(err)
	}
	return respond(c, outcome)
}

// readOperation turns the request into the operation the domain validates. Everything about who
// the caller is comes from the token: the body says which provider it wants to act for, and the
// token says which one it may.
func readOperation(c echo.Context) (entities.ExternalOperation, error) {
	key := strings.TrimSpace(c.Request().Header.Get(idempotencyKeyHeader))
	if key == "" {
		return entities.ExternalOperation{}, badRequest("the "+idempotencyKeyHeader+" header is required", "")
	}
	var request SubmitRequest
	if err := c.Bind(&request); err != nil {
		return entities.ExternalOperation{}, badRequest("the request body is not valid JSON of the expected shape", "")
	}
	if err := authorizeProvider(c, request.ProviderID); err != nil {
		return entities.ExternalOperation{}, err
	}
	amount, err := request.Money.Parse()
	if err != nil {
		return entities.ExternalOperation{}, badRequest("money: "+err.Error(), string(entities.FailureInvalidAmount))
	}

	return entities.ExternalOperation{
		ProviderID:                     request.ProviderID,
		ExternalTransactionID:          request.ExternalTransactionID,
		IdempotencyKey:                 key,
		PlayerID:                       request.PlayerID,
		WalletID:                       request.WalletID,
		RoundID:                        request.RoundID,
		GameID:                         request.GameID,
		Kind:                           request.Kind,
		Money:                          amount,
		ReferenceExternalTransactionID: request.ReferenceExternalTransactionID,
	}, nil
}

// authorizeProvider refuses an operation that names a provider other than the one the token
// belongs to. It runs before anything is read or written, so a refused caller leaves no trace and
// learns nothing about what the other provider has stored.
func authorizeProvider(c echo.Context, requested string) error {
	principal, ok := middleware.AuthenticatedPrincipal(c.Request().Context())
	if !ok || principal.ProviderID == "" {
		return forbidden("this identity does not act for a provider")
	}
	if requested != principal.ProviderID {
		return forbidden("the token does not authorize operations for this providerId")
	}
	return nil
}

// respond maps the state the transaction is in to a status the client can tell apart.
func respond(c echo.Context, outcome structs.WagerOutcome) error {
	tx := outcome.Transaction
	response := TransactionResponse{
		TransactionID:    tx.ID().String(),
		Status:           string(tx.Status()),
		FailureCode:      string(tx.FailureCode()),
		IdempotentReplay: outcome.Replay,
	}
	if tx.Status() == entities.StatusProcessed {
		balance := structs.MoneyDTOOf(tx.ResultBalance())
		response.Balance = &balance
	}
	return c.JSON(statusOf(tx.Status()), response)
}

func statusOf(status entities.TransactionStatus) int {
	switch status {
	case entities.StatusProcessed:
		return http.StatusOK
	case entities.StatusRejected:
		return http.StatusUnprocessableEntity
	case entities.StatusPending, entities.StatusPendingReference:
		return http.StatusAccepted
	default:
		return http.StatusInternalServerError
	}
}

// TransactionDetail is a transaction as a provider follows it: where it stands, why it was refused
// when it was, and, while it waits for its reference, how the wait is going.
type TransactionDetail struct {
	TransactionID                  string            `json:"transactionId" example:"0192f298-345e-7e38-af88-e43f851a819d"`
	ProviderID                     string            `json:"providerId" example:"provider-a"`
	ExternalTransactionID          string            `json:"externalTransactionId" example:"transaction-123"`
	WalletID                       string            `json:"walletId" example:"0192f291-27dd-7d3f-8071-5f8685deef37"`
	PlayerID                       string            `json:"playerId" example:"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"`
	RoundID                        string            `json:"roundId" example:"round-987"`
	GameID                         string            `json:"gameId" example:"fortune-chimp"`
	Kind                           string            `json:"kind" enums:"BET,WIN,LOSS,REFUND,ROLLBACK" example:"BET"`
	Money                          structs.MoneyDTO  `json:"money"`
	Status                         string            `json:"status" enums:"PENDING,PENDING_REFERENCE,PROCESSED,REJECTED,FAILED" example:"PROCESSED"`
	FailureCode                    string            `json:"failureCode,omitempty" example:"INSUFFICIENT_FUNDS"`
	Balance                        *structs.MoneyDTO `json:"balance,omitempty"`
	ReferenceExternalTransactionID string            `json:"referenceExternalTransactionId,omitempty" example:"transaction-122"`
	ReferenceAttempts              int               `json:"referenceAttempts,omitempty" example:"3"`
	NextAttemptAt                  string            `json:"nextAttemptAt,omitempty" example:"2026-09-08T12:00:05.000Z"`
	ExpiresAt                      string            `json:"expiresAt,omitempty" example:"2026-09-09T12:00:00.000Z"`
	CreatedAt                      string            `json:"createdAt" example:"2026-09-08T12:00:00.000Z"`
	UpdatedAt                      string            `json:"updatedAt" example:"2026-09-08T12:00:00.000Z"`
	SettledAt                      string            `json:"settledAt,omitempty" example:"2026-09-08T12:00:00.000Z"`
}

// Get reads one of the provider's own transactions by id.
//
//	@Summary		Lê uma transação pelo id
//	@Description	Permite acompanhar uma pendência (tentativas, próxima olhada e expiração) e consultar o código de uma rejeição ou falha. Uma transação de outro provedor responde 404, exatamente como uma que não existe: perguntar não revela de quem é.
//	@Tags			wagering
//	@Produce		json
//	@Param			transactionId	path		string	true	"Id da transação"
//	@Success		200				{object}	TransactionDetail
//	@Failure		400				{object}	structs.APIError
//	@Failure		401				{object}	structs.APIError
//	@Failure		403				{object}	structs.APIError
//	@Failure		404				{object}	structs.APIError
//	@Failure		503				{object}	structs.APIError
//	@Security		OAuth2Password
//	@Router			/wagering/transactions/{transactionId} [get]
func (h *Handler) Get(c echo.Context) error {
	provider, err := actingProvider(c)
	if err != nil {
		return err
	}
	id, parseErr := uuid.Parse(c.Param("transactionId"))
	if parseErr != nil {
		return badRequest("transactionId must be a UUID", "")
	}
	tx, err := h.service.Get(c.Request().Context(), provider, id)
	if err != nil {
		return httpError(err)
	}
	return c.JSON(http.StatusOK, detail(tx))
}

// GetByExternal reads one of the provider's own transactions by the id the provider gave it.
//
//	@Summary		Lê uma transação pelo id do provedor
//	@Description	O providerId do caminho precisa ser o do token: outro é 403, decidido antes de tocar em qualquer dado.
//	@Tags			wagering
//	@Produce		json
//	@Param			providerId				path		string	true	"Id do provedor, o mesmo do token"
//	@Param			externalTransactionId	path		string	true	"Id que o provedor deu à transação"
//	@Success		200						{object}	TransactionDetail
//	@Failure		401						{object}	structs.APIError
//	@Failure		403						{object}	structs.APIError
//	@Failure		404						{object}	structs.APIError
//	@Failure		503						{object}	structs.APIError
//	@Security		OAuth2Password
//	@Router			/providers/{providerId}/wagering/transactions/{externalTransactionId} [get]
func (h *Handler) GetByExternal(c echo.Context) error {
	if err := authorizeProvider(c, c.Param("providerId")); err != nil {
		return err
	}
	provider, err := actingProvider(c)
	if err != nil {
		return err
	}
	tx, err := h.service.GetByExternal(c.Request().Context(), provider, c.Param("externalTransactionId"))
	if err != nil {
		return httpError(err)
	}
	return c.JSON(http.StatusOK, detail(tx))
}

// actingProvider is the provider the token belongs to. Every read is scoped by it, and never by
// anything the request says about itself.
func actingProvider(c echo.Context) (string, error) {
	principal, ok := middleware.AuthenticatedPrincipal(c.Request().Context())
	if !ok || principal.ProviderID == "" {
		return "", forbidden("this identity does not act for a provider")
	}
	return principal.ProviderID, nil
}

func detail(tx *entities.WagerTransaction) TransactionDetail {
	response := TransactionDetail{
		TransactionID:                  tx.ID().String(),
		ProviderID:                     tx.ProviderID(),
		ExternalTransactionID:          tx.ExternalTransactionID(),
		WalletID:                       tx.WalletID().String(),
		PlayerID:                       tx.PlayerID().String(),
		RoundID:                        tx.RoundID(),
		GameID:                         tx.GameID(),
		Kind:                           string(tx.Kind()),
		Money:                          structs.MoneyDTOOf(tx.Money()),
		Status:                         string(tx.Status()),
		FailureCode:                    string(tx.FailureCode()),
		ReferenceExternalTransactionID: tx.ReferenceExternalTransactionID(),
		ReferenceAttempts:              tx.ReferenceAttempts(),
		NextAttemptAt:                  structs.Timestamp(tx.ReferenceNextAttemptAt()),
		ExpiresAt:                      structs.Timestamp(tx.ReferenceExpiresAt()),
		CreatedAt:                      structs.Timestamp(tx.CreatedAt()),
		UpdatedAt:                      structs.Timestamp(tx.UpdatedAt()),
		SettledAt:                      structs.Timestamp(tx.SettledAt()),
	}
	if tx.Status() == entities.StatusProcessed {
		balance := structs.MoneyDTOOf(tx.ResultBalance())
		response.Balance = &balance
	}
	return response
}

func badRequest(message, failureCode string) *echo.HTTPError {
	return echo.NewHTTPError(http.StatusBadRequest, structs.APIError{Message: message, FailureCode: failureCode})
}

func forbidden(message string) *echo.HTTPError {
	return echo.NewHTTPError(http.StatusForbidden, structs.APIError{Message: message})
}

// failures maps what the service reports to what the client is told. The order matters: the
// first sentinel that matches wins, so the more specific ones come before the general ones.
var failures = []struct {
	target  error
	status  int
	message string
	code    string
}{
	{entities.ErrOpeningNotAllowed, http.StatusBadRequest, "OPENING is reserved to the internal wallet opening", string(entities.FailureOpeningNotAllowed)},
	{wageringiface.ErrNotFound, http.StatusNotFound, "transaction not found", ""},
	{wageringiface.ErrIdempotencyConflict, http.StatusConflict, "the idempotency key or the provider transaction id was already used with other content", ""},
	{persistenceiface.ErrUnavailable, http.StatusServiceUnavailable, "storage is temporarily unavailable, try again", ""},
}

func httpError(err error) *echo.HTTPError {
	// A rejection that reaches here was not stored, because there was nothing to attach it to,
	// such as a wallet that does not exist. It still has its stable code.
	if code, ok := entities.RejectionCode(err); ok {
		return echo.NewHTTPError(http.StatusUnprocessableEntity, structs.APIError{Message: err.Error(), FailureCode: string(code)})
	}
	for _, failure := range failures {
		if errors.Is(err, failure.target) {
			return echo.NewHTTPError(failure.status, structs.APIError{Message: failure.message, FailureCode: failure.code})
		}
	}
	if errors.Is(err, entities.ErrInvalidTransaction) {
		return badRequest(err.Error(), "")
	}
	return echo.NewHTTPError(http.StatusInternalServerError, structs.APIError{Message: "internal error"})
}
