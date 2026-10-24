package coreapi

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gravadigital/jiku-go"
	"github.com/nats-io/nats.go"

	"github.com/gravadigital/jiku-docs/internal/protocol"
)

// JikuTransport talks to core with the jiku-go client (inbox prefix, token refresh and subject
// grammar handled there).
type JikuTransport struct{ Client *jiku.Client }

func (t JikuTransport) Request(ctx context.Context, service, method string, payload any) (*Reply, error) {
	r, err := t.Client.Request(ctx, service, method, payload)
	if err != nil {
		return nil, err
	}
	return &Reply{Status: string(r.Status), ErrorCode: r.ErrorCode, ErrorMessage: r.ErrorMessage, Data: r.Data}, nil
}

// NATSTransport talks to core over a bare connection. It is what the tests use against the core
// double, and it builds exactly the same subjects as jiku-go.
type NATSTransport struct {
	Conn     *nats.Conn
	Instance string
	UserID   string
}

func (t NATSTransport) Request(ctx context.Context, service, method string, payload any) (*Reply, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	msg, err := t.Conn.RequestWithContext(ctx, protocol.RequestSubject(t.Instance, t.UserID, service, method), body)
	if err != nil {
		return nil, err
	}
	var r Reply
	if err := json.Unmarshal(msg.Data, &r); err != nil {
		return nil, fmt.Errorf("decoding the envelope of %s: %w", method, err)
	}
	return &r, nil
}
