package wallet

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/estrategiahq/pedro-test/app/src/entities"
	persistenceiface "github.com/estrategiahq/pedro-test/app/src/interfaces/persistence"
	walletiface "github.com/estrategiahq/pedro-test/app/src/interfaces/wallet"
	"github.com/estrategiahq/pedro-test/app/src/structs"
)

// Handler serves the wallet routes.
type Handler struct {
	service walletiface.Service
}

// NewHandler builds the handler.
func NewHandler(service walletiface.Service) *Handler {
	return &Handler{service: service}
}

// ServerRoutes registers the wallet domain on the HTTP server. Opening a wallet moves money, so
// it asks for authentication and for the internal service role: a provider has no business
// creating wallets, and the border is where that is decided.
func ServerRoutes(e *echo.Echo, h *Handler, requireAuthentication, requireInternalService echo.MiddlewareFunc) {
	e.POST("/wallets", h.Open, requireAuthentication, requireInternalService)
}

// OpenRequest is the body of a wallet opening.
type OpenRequest struct {
	PlayerID       string           `json:"playerId" example:"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"`
	InitialBalance structs.MoneyDTO `json:"initialBalance"`
}

// Response is a wallet as the API describes it.
type Response struct {
	ID       string           `json:"id" example:"0192f291-27dd-7d3f-8071-5f8685deef37"`
	PlayerID string           `json:"playerId" example:"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"`
	Balance  structs.MoneyDTO `json:"balance"`
	Version  int64            `json:"version" example:"1"`
}

// Open creates the wallet of a player.
//
//	@Summary		Abre a carteira de um jogador
//	@Description	Cria a carteira na moeda do saldo inicial. Com saldo positivo, cria também a transação OPENING, o crédito no ledger e os eventos, no mesmo commit. Com saldo zero, cria só a carteira. Restrita ao serviço interno.
//	@Tags			wallets
//	@Accept			json
//	@Produce		json
//	@Param			request	body		OpenRequest	true	"Jogador e saldo inicial"
//	@Success		201		{object}	Response
//	@Failure		400		{object}	structs.APIError
//	@Failure		401		{object}	structs.APIError
//	@Failure		403		{object}	structs.APIError
//	@Failure		409		{object}	structs.APIError
//	@Failure		503		{object}	structs.APIError
//	@Security		OAuth2Password
//	@Router			/wallets [post]
func (h *Handler) Open(c echo.Context) error {
	var request OpenRequest
	if err := c.Bind(&request); err != nil {
		return badRequest("the request body is not valid JSON of the expected shape", "")
	}
	playerID, err := uuid.Parse(request.PlayerID)
	if err != nil {
		return badRequest("playerId must be a UUID", "")
	}
	initialBalance, err := request.InitialBalance.Parse()
	if err != nil {
		return badRequest("initialBalance: "+err.Error(), string(entities.FailureInvalidAmount))
	}

	wallet, err := h.service.Open(c.Request().Context(), playerID, initialBalance)
	if err != nil {
		return httpError(err)
	}
	return c.JSON(http.StatusCreated, Response{
		ID:       wallet.ID().String(),
		PlayerID: wallet.PlayerID().String(),
		Balance:  structs.MoneyDTOOf(wallet.Balance()),
		Version:  wallet.Version(),
	})
}

func badRequest(message, failureCode string) *echo.HTTPError {
	return echo.NewHTTPError(http.StatusBadRequest, structs.APIError{Message: message, FailureCode: failureCode})
}

// failures maps what the service reports to what the client is told. The order matters only
// where two sentinels can match one error, and none does today.
var failures = []struct {
	target  error
	status  int
	message string
}{
	{walletiface.ErrAlreadyExists, http.StatusConflict, "the player already has a wallet in this currency"},
	{persistenceiface.ErrUnavailable, http.StatusServiceUnavailable, "storage is temporarily unavailable, try again"},
}

func httpError(err error) *echo.HTTPError {
	for _, failure := range failures {
		if errors.Is(err, failure.target) {
			return echo.NewHTTPError(failure.status, structs.APIError{Message: failure.message})
		}
	}
	if errors.Is(err, entities.ErrInvalidWallet) {
		return badRequest(err.Error(), "")
	}
	return echo.NewHTTPError(http.StatusInternalServerError, structs.APIError{Message: "internal error"})
}
