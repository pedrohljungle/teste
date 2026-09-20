package structs

// QueueMessage is the transport envelope of a job: the payload as it was published plus the
// trace context of the publisher.
//
// It exists so the queue runtime can carry a message without knowing what is inside it, and
// so the adapter that reads from the broker does not have to import the runtime.
type QueueMessage struct {
	Queue        string
	TraceContext map[string]string
	Payload      []byte
	// AckToken is what the adapter needs to confirm the message was processed — a receipt
	// handle on SQS. It is opaque here on purpose: the runtime hands it back without ever
	// learning what the broker does with it.
	AckToken string
}
