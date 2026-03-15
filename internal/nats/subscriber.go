package nats

import (
	"fmt"

	"github.com/nats-io/nats.go"
)

// MsgHandler processes a received NATS message.
type MsgHandler func(msg *nats.Msg)

// Subscriber subscribes to NATS subjects using queue groups.
type Subscriber interface {
	QueueSubscribe(subject, queue string, handler MsgHandler) (*nats.Subscription, error)
	Drain() error
	Close()
}

type subscriber struct {
	conn *nats.Conn
}

// NewSubscriber connects to NATS and returns a Subscriber.
func NewSubscriber(url string) (Subscriber, error) {
	nc, err := nats.Connect(url)
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}
	return &subscriber{conn: nc}, nil
}

func (s *subscriber) QueueSubscribe(subject, queue string, handler MsgHandler) (*nats.Subscription, error) {
	return s.conn.QueueSubscribe(subject, queue, func(msg *nats.Msg) {
		handler(msg)
	})
}

func (s *subscriber) Drain() error {
	return s.conn.Drain()
}

func (s *subscriber) Close() {
	s.conn.Close()
}
