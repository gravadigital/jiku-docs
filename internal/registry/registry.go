// Package registry writes the module record in Jiku's KV `JIKU_MODULES` (key `docs`), which is
// how the api, connectors and agents discover that jiku-docs is installed. Jiku creates the bucket;
// jiku-docs only writes (and, on uninstall, deletes) its own key.
package registry

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/gravadigital/jiku-docs/internal/protocol"
)

// Bucket and key of the record.
const (
	Bucket = "JIKU_MODULES"
	Key    = "docs"
)

// Record is the value of the key (docs/apis/jiku-modules.yaml).
type Record struct {
	Module       string `json:"module"`
	ServiceName  string `json:"serviceName"`
	Protocol     string `json:"protocol"`
	RegisteredAt string `json:"registeredAt"`
}

// ErrNoBucket means Jiku has not created JIKU_MODULES yet.
var ErrNoBucket = errors.New("the KV bucket JIKU_MODULES does not exist: Jiku's deployment creates it")

// Register writes the record. Called once the service can already answer.
func Register(ctx context.Context, js jetstream.JetStream, serviceName string) (*Record, error) {
	kv, err := js.KeyValue(ctx, Bucket)
	if errors.Is(err, jetstream.ErrBucketNotFound) {
		return nil, ErrNoBucket
	}
	if err != nil {
		return nil, err
	}
	rec := &Record{Module: Key, ServiceName: serviceName, Protocol: protocol.ProtocolVersion, RegisteredAt: protocol.FormatTime(protocol.Now())}
	b, _ := json.Marshal(rec)
	if _, err := kv.Put(ctx, Key, b); err != nil {
		return nil, err
	}
	return rec, nil
}

// Deregister deletes the key: the module stops being "installed" for Jiku. A normal shutdown
// never calls this (a restart is not an uninstall).
func Deregister(ctx context.Context, js jetstream.JetStream) error {
	kv, err := js.KeyValue(ctx, Bucket)
	if errors.Is(err, jetstream.ErrBucketNotFound) {
		return ErrNoBucket
	}
	if err != nil {
		return err
	}
	return kv.Delete(ctx, Key)
}
