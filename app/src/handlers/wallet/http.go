package wallet

import (
	"errors"
	"net/http"
	"strconv"

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

// ServerRoutes registers the wallet domain on the HTTP server. Every wallet route belongs to the
// internal service: opening a wallet moves money, and reading a wallet or its ledger, or
// reconciling it, is an operation of the platform and not something a provider does. The border is
// where that is decided, so each route says it.
func ServerRoutes(e *echo.Echo, h *Handler, requireAuthentication, requireInternalService echo.MiddlewareFunc) {
	e.POST("/wallets", h.Open, requireAuthentication, requireInternalService)
	e.GET("/wallets/:walletId", h.Get, requireAuthentication, requireInternalService)
	e.GET("/wallets/:walletId/ledger", h.Ledger, requireAuthentication, requireInternalService)
	e.POST("/wallets/:walletId/reconciliation", h.Reconcile, requireAuthentication, requireInternalService)
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
	return c.JSON(http.StatusCreated, walletResponse(wallet))
}

// Get reads a wallet.
//
//	@Summary		Lê uma carteira
//	@Description	Devolve o saldo e a versão atuais. Restrita ao serviço interno.
//	@Tags			wallets
//	@Produce		json
//	@Param			walletId	path		string	true	"Id da carteira"
//	@Success		200			{object}	Response
//	@Failure		400			{object}	structs.APIError
//	@Failure		401			{object}	structs.APIError
//	@Failure		403			{object}	structs.APIError
//	@Failure		404			{object}	structs.APIError
//	@Failure		503			{object}	structs.APIError
//	@Security		OAuth2Password
//	@Router			/wallets/{walletId} [get]
func (h *Handler) Get(c echo.Context) error {
	id, err := walletID(c)
	if err != nil {
		return err
	}
	wallet, err := h.service.Get(c.Request().Context(), id)
	if err != nil {
		return httpError(err)
	}
	return c.JSON(http.StatusOK, walletResponse(wallet))
}

// LedgerEntryResponse is one line of a wallet's ledger.
type LedgerEntryResponse struct {
	ID            string           `json:"id" example:"0192f2a1-4d3e-7b1a-9c55-0f1e2d3c4b5a"`
	TransactionID string           `json:"transactionId" example:"0192f298-345e-7e38-af88-e43f851a819d"`
	Direction     string           `json:"direction" enums:"DEBIT,CREDIT" example:"DEBIT"`
	Money         structs.MoneyDTO `json:"money"`
	BalanceBefore structs.MoneyDTO `json:"balanceBefore"`
	BalanceAfter  structs.MoneyDTO `json:"balanceAfter"`
	CreatedAt     string           `json:"createdAt" example:"2026-09-08T12:00:00.000Z"`
}

// LedgerResponse is one page of a wallet's ledger, oldest entry first.
type LedgerResponse struct {
	Entries []LedgerEntryResponse `json:"entries"`
	// NextCursor is absent on the last page. It is opaque: hand it back unchanged.
	NextCursor string `json:"nextCursor,omitempty" example:"djEuNDI"`
}

// Ledger reads one page of a wallet's ledger.
//
//	@Summary		Lê o ledger de uma carteira
//	@Description	Paginado por cursor opaco, do lançamento mais antigo para o mais novo. A ordem é a posição que o banco deu a cada lançamento, que não muda nem se repete: um lançamento gravado enquanto o cliente lê aparece numa página posterior, e nenhum é pulado nem visto duas vezes. Restrita ao serviço interno.
//	@Tags			wallets
//	@Produce		json
//	@Param			walletId	path		string	true	"Id da carteira"
//	@Param			cursor		query		string	false	"Cursor devolvido pela página anterior"
//	@Param			limit		query		int		false	"Tamanho da página, de 1 a 200. Padrão 50."
//	@Success		200			{object}	LedgerResponse
//	@Failure		400			{object}	structs.APIError
//	@Failure		401			{object}	structs.APIError
//	@Failure		403			{object}	structs.APIError
//	@Failure		404			{object}	structs.APIError
//	@Failure		503			{object}	structs.APIError
//	@Security		OAuth2Password
//	@Router			/wallets/{walletId}/ledger [get]
func (h *Handler) Ledger(c echo.Context) error {
	id, err := walletID(c)
	if err != nil {
		return err
	}
	limit, err := pageLimit(c.QueryParam("limit"))
	if err != nil {
		return err
	}
	page, err := h.service.Ledger(c.Request().Context(), id, c.QueryParam("cursor"), limit)
	if err != nil {
		return httpError(err)
	}

	response := LedgerResponse{Entries: make([]LedgerEntryResponse, 0, len(page.Entries))}
	for _, entry := range page.Entries {
		response.Entries = append(response.Entries, LedgerEntryResponse{
			ID:            entry.ID().String(),
			TransactionID: entry.TransactionID().String(),
			Direction:     string(entry.Direction()),
			Money:         structs.MoneyDTOOf(entry.Money()),
			BalanceBefore: structs.MoneyDTOOf(entry.BalanceBefore()),
			BalanceAfter:  structs.MoneyDTOOf(entry.BalanceAfter()),
			CreatedAt:     structs.Timestamp(entry.CreatedAt()),
		})
	}
	if page.HasMore {
		response.NextCursor = structs.EncodeCursor(page.NextAfter)
	}
	return c.JSON(http.StatusOK, response)
}

// ReconciliationResponse is a wallet balance rebuilt from its ledger, next to the stored one.
type ReconciliationResponse struct {
	WalletID          string           `json:"walletId" example:"0192f291-27dd-7d3f-8071-5f8685deef37"`
	StoredBalance     structs.MoneyDTO `json:"storedBalance"`
	CalculatedBalance structs.MoneyDTO `json:"calculatedBalance"`
	// Difference is the stored balance minus the calculated one.
	Difference     structs.MoneyDTO `json:"difference"`
	Consistent     bool             `json:"consistent" example:"true"`
	CheckedEntries int              `json:"checkedEntries" example:"2"`
}

// Reconcile rebuilds a wallet balance from its ledger.
//
//	@Summary		Reconcilia uma carteira
//	@Description	Reconstrói o saldo a partir do ledger, abertura incluída, e compara com o saldo gravado, os dois lidos de uma mesma visão consistente dos dados. Não altera nada: uma divergência vai na resposta, no log e numa métrica, e nunca é corrigida. Restrita ao serviço interno.
//	@Tags			wallets
//	@Produce		json
//	@Param			walletId	path		string	true	"Id da carteira"
//	@Success		200			{object}	ReconciliationResponse
//	@Failure		400			{object}	structs.APIError
//	@Failure		401			{object}	structs.APIError
//	@Failure		403			{object}	structs.APIError
//	@Failure		404			{object}	structs.APIError
//	@Failure		503			{object}	structs.APIError
//	@Security		OAuth2Password
//	@Router			/wallets/{walletId}/reconciliation [post]
func (h *Handler) Reconcile(c echo.Context) error {
	id, err := walletID(c)
	if err != nil {
		return err
	}
	result, err := h.service.Reconcile(c.Request().Context(), id)
	if err != nil {
		return httpError(err)
	}
	return c.JSON(http.StatusOK, ReconciliationResponse{
		WalletID:          result.WalletID,
		StoredBalance:     structs.MoneyDTOOf(result.StoredBalance),
		CalculatedBalance: structs.MoneyDTOOf(result.CalculatedBalance),
		Difference:        structs.MoneyDTOOf(result.Difference),
		Consistent:        result.Consistent,
		CheckedEntries:    result.CheckedEntries,
	})
}

func walletID(c echo.Context) (uuid.UUID, error) {
	id, err := uuid.Parse(c.Param("walletId"))
	if err != nil {
		return uuid.Nil, badRequest("walletId must be a UUID", "")
	}
	return id, nil
}

// pageLimit reads the limit of a page. An absent limit is zero, which the service reads as its
// default; one that is present has to be a positive number, because a request that says "limit=0"
// or "limit=abc" means something other than "give me the default" and should hear that it was
// misunderstood.
func pageLimit(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 {
		return 0, badRequest("limit must be a whole number of at least 1", "")
	}
	return limit, nil
}

func walletResponse(wallet *entities.Wallet) Response {
	return Response{
		ID:       wallet.ID().String(),
		PlayerID: wallet.PlayerID().String(),
		Balance:  structs.MoneyDTOOf(wallet.Balance()),
		Version:  wallet.Version(),
	}
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
	{walletiface.ErrNotFound, http.StatusNotFound, "wallet not found"},
	{walletiface.ErrInvalidPage, http.StatusBadRequest, "the page is not valid"},
	{structs.ErrInvalidCursor, http.StatusBadRequest, "the cursor is not one this system issued"},
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
