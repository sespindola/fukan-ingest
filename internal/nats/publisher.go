package nats

import (
	"encoding/json"
	"fmt"

	"github.com/nats-io/nats.go"
	"github.com/sespindola/fukan-ingest/internal/model"
)

// Publisher publishes FukanEvents to NATS subjects.
type Publisher interface {
    Publish(subject string, event model.FukanEvent) error
    // Drain flushes pending publishes and closes the connection gracefully.
    Drain() error
    Close()
}

type publisher struct {
	conn *nats.Conn
}

// NewPublisher connects to NATS and returns a Publisher.
func NewPublisher(url string) (Publisher, error) {
	nc, err := nats.Connect(url)
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}
	return &publisher{conn: nc}, nil
}

func (p *publisher) Publish(subject string, event model.FukanEvent) error {
    data, err := json.Marshal(event)
    if err != nil {
        return fmt.Errorf("marshal event: %w", err)
    }
    return p.conn.Publish(subject, data)
}

func (p *publisher) Drain() error {
    return p.conn.Drain()
}

func (p *publisher) Close() {
    p.conn.Close()
}
