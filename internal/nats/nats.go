package nats

import (
	"encoding/json"
	"fmt"

	"github.com/nats-io/nats.go"
)

// Connect dials NATS and returns the bare connection.
func Connect(url string) (*nats.Conn, error) {
	nc, err := nats.Connect(url)
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}
	return nc, nil
}

// PublishJSON marshals v as JSON and publishes to the given subject.
func PublishJSON(nc *nats.Conn, subject string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return nc.Publish(subject, data)
}
