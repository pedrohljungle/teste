package wagering

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/estrategiahq/pedro-test/app/src/entities"
	wageringiface "github.com/estrategiahq/pedro-test/app/src/interfaces/wagering"
	"github.com/estrategiahq/pedro-test/app/src/libs/jobrunner"
	"github.com/estrategiahq/pedro-test/app/src/libs/observability"
	"github.com/estrategiahq/pedro-test/app/src/structs"
)

// messageType is the only kind of message this consumer understands.
const messageType = "WagerTransactionRequested"

// errMalformed is a message that is not the contract: not JSON, another type, no id. No retry
// changes what it says.
var errMalformed = errors.New("malformed message")

// JobHandler consumes the wagering messages of the queue. Consuming a queue is delivery, so it is
// a handler in the domain that the messages belong to, next to the one that serves HTTP. It reads
// the message and decides the fate of it from what handling it came to; the rules are the
// service's, the same ones HTTP reaches.
type JobHandler struct {
	service    wageringiface.Service
	deadLetter wageringiface.DeadLetter
	obs        *observability.Observer
}

// NewJobHandler builds the handler.
func NewJobHandler(service wageringiface.Service, deadLetter wageringiface.DeadLetter, obs *observability.Observer) *JobHandler {
	return &JobHandler{service: service, deadLetter: deadLetter, obs: obs}
}

// PrepareWorker registers the wagering messages on the job runner, mirroring how ServerRoutes
// registers the routes on the server.
func PrepareWorker(runner *jobrunner.Runner, source jobrunner.Source, h *JobHandler) {
	runner.Register(source, h.Handle)
}

// messageBody is the contract of a message: the envelope and the operation it carries, with the
// same fields an HTTP request has plus the idempotency key, which travels in the body because a
// queue message has no headers of its own.
type messageBody struct {
	MessageID  string `json:"messageId"`
	Type       string `json:"type"`
	OccurredAt string `json:"occurredAt"`
	Data       struct {
		ProviderID                     string           `json:"providerId"`
		ExternalTransactionID          string           `json:"externalTransactionId"`
		IdempotencyKey                 string           `json:"idempotencyKey"`
		PlayerID                       string           `json:"playerId"`
		WalletID                       string           `json:"walletId"`
		RoundID                        string           `json:"roundId"`
		GameID                         string           `json:"gameId"`
		Kind                           string           `json:"kind"`
		Money                          structs.MoneyDTO `json:"money"`
		ReferenceExternalTransactionID string           `json:"referenceExternalTransactionId"`
	} `json:"data"`
}

// Handle is what the runner calls for each message. Returning nil deletes the message from the
// queue; returning an error leaves it for redelivery, and after enough deliveries SQS moves it to
// the dead letter queue on its own.
//
// Three outcomes, and only the last one is a retry:
//   - the operation was applied, replayed or rejected by a business rule: done, delete;
//   - no retry can change the result (malformed, contradicting): send it to the dead
//     letter queue with the reason, then delete it from this one;
//   - the storage was unavailable: return the error, and the queue delivers it again.
func (h *JobHandler) Handle(ctx context.Context, msg structs.QueueMessage) error {
	return observability.TraceErr(ctx, h.obs, observability.LayerHandler, "wagering.JobHandler.Handle", func(ctx context.Context) error {
		message, err := parse(msg.Payload)
		if err != nil {
			return h.giveUp(ctx, msg, err)
		}
		// The message id is the correlation id: everything the message causes carries it.
		ctx = observability.WithCorrelationID(ctx, message.ID)
		ctx = observability.WithFields(ctx,
			observability.String("messageId", message.ID),
			observability.String("providerId", message.Operation.ProviderID),
			observability.String("walletId", message.Operation.WalletID),
		)

		_, err = h.service.Receive(ctx, message)
		return h.settle(ctx, msg, err)
	})
}

// settle decides the fate of the message from what handling it came to.
func (h *JobHandler) settle(ctx context.Context, msg structs.QueueMessage, err error) error {
	switch {
	case err == nil:
		return nil
	case isPermanent(err):
		return h.giveUp(ctx, msg, err)
	case errors.Is(err, entities.ErrRejected):
		// A definitive business answer that has nothing to be stored against, such as a wallet
		// that does not exist. Retrying it would only get the same answer.
		h.obs.Warn(ctx, "message rejected by a business rule",
			observability.String("reason", err.Error()))
		return nil
	default:
		// Left for redelivery: SQS brings it back after the visibility timeout.
		h.obs.Count(ctx, "sqs_message_retries_total")
		return err
	}
}

// giveUp sends a message that cannot succeed to the dead letter queue and only then lets it be
// deleted. When the dead letter queue cannot be reached the error is returned instead, so the
// message is not lost: it comes back and is given up on again.
func (h *JobHandler) giveUp(ctx context.Context, msg structs.QueueMessage, cause error) error {
	h.obs.Error(ctx, cause, "message cannot be processed, sent to the dead letter queue")
	h.obs.Count(ctx, "sqs_dead_letters_total", observability.NewTag("reason", deadLetterReason(cause)))
	if err := h.deadLetter.Send(ctx, msg, cause.Error()); err != nil {
		return fmt.Errorf("send to the dead letter queue: %w", err)
	}
	return nil
}

// deadLetterReason is the bounded label of why a message was given up on.
func deadLetterReason(err error) string {
	switch {
	case errors.Is(err, errMalformed):
		return "malformed"
	case errors.Is(err, wageringiface.ErrMessageConflict):
		return "message_conflict"
	case errors.Is(err, wageringiface.ErrIdempotencyConflict):
		return "idempotency_conflict"
	default:
		return "invalid_operation"
	}
}

// isPermanent reports the failures a retry cannot fix.
func isPermanent(err error) bool {
	return errors.Is(err, errMalformed) ||
		errors.Is(err, entities.ErrInvalidTransaction) ||
		errors.Is(err, wageringiface.ErrIdempotencyConflict) ||
		errors.Is(err, wageringiface.ErrMessageConflict)
}

// parse reads the body of a message. The hash covers the exact bytes that arrived, so a
// redelivery, which is byte for byte the same, matches, and another message reusing the id does
// not.
func parse(payload []byte) (structs.WagerMessage, error) {
	var body messageBody
	if err := json.Unmarshal(payload, &body); err != nil {
		return structs.WagerMessage{}, fmt.Errorf("%w: %w", errMalformed, err)
	}
	if body.Type != messageType {
		return structs.WagerMessage{}, fmt.Errorf("%w: type %q, expected %s", errMalformed, body.Type, messageType)
	}
	if body.MessageID == "" {
		return structs.WagerMessage{}, fmt.Errorf("%w: messageId is required", errMalformed)
	}
	amount, err := body.Data.Money.Parse()
	if err != nil {
		return structs.WagerMessage{}, fmt.Errorf("%w: money: %w", errMalformed, err)
	}

	hash := sha256.Sum256(payload)
	return structs.WagerMessage{
		ID: body.MessageID,
		Operation: entities.ExternalOperation{
			ProviderID:                     body.Data.ProviderID,
			ExternalTransactionID:          body.Data.ExternalTransactionID,
			IdempotencyKey:                 body.Data.IdempotencyKey,
			PlayerID:                       body.Data.PlayerID,
			WalletID:                       body.Data.WalletID,
			RoundID:                        body.Data.RoundID,
			GameID:                         body.Data.GameID,
			Kind:                           body.Data.Kind,
			Money:                          amount,
			ReferenceExternalTransactionID: body.Data.ReferenceExternalTransactionID,
		},
		Hash: hash[:],
	}, nil
}
