// Package app wires the running service: the JetStream stream, the two micro services, the
// outbox publisher, the indexer, the janitor and the module registration. `serve` and the
// integration tests use the same wiring; only the connection and the core client differ.
package app

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/gravadigital/jiku-docs/internal/blob"
	"github.com/gravadigital/jiku-docs/internal/bus"
	"github.com/gravadigital/jiku-docs/internal/coreapi"
	"github.com/gravadigital/jiku-docs/internal/indexer"
	"github.com/gravadigital/jiku-docs/internal/janitor"
	"github.com/gravadigital/jiku-docs/internal/metrics"
	"github.com/gravadigital/jiku-docs/internal/outbox"
	"github.com/gravadigital/jiku-docs/internal/registry"
	"github.com/gravadigital/jiku-docs/internal/service"
)

// Options of a running instance.
type Options struct {
	NC       *nats.Conn
	DB       *pgxpool.Pool
	Blob     blob.Store
	Core     coreapi.API
	Log      *slog.Logger
	Metrics  *metrics.Metrics
	Settings service.Settings
	Version  string

	Indexer bool
	Janitor bool
	Outbox  bool
	// Register writes the JIKU_MODULES key once ready (retried until the bucket exists).
	Register bool
	// JanitorTick overrides the janitor period (tests).
	JanitorTick time.Duration
	ReviewHour  int
	// History is the compaction policy; HistoryEnabled runs it daily from HistoryHour.
	History        service.HistoryPolicy
	HistoryEnabled bool
	HistoryHour    int
}

// Instance is a started service.
type Instance struct {
	Svc     *service.Service
	JS      jetstream.JetStream
	Indexer *indexer.Indexer
	server  *bus.Server
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	// Registered is closed once the module record is written.
	Registered chan struct{}
}

// Start brings the service up. The order matters: stream first (so no event is lost), then the
// micro services, then the background loops, and only then the module record — written once the
// service can already answer.
func Start(ctx context.Context, o Options) (*Instance, error) {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	svc := service.New(o.DB, o.Blob, o.Core, o.Log, o.Settings)
	svc.History = o.History
	svc.Hooks.Request = o.Metrics.Request
	if mp := o.NC.MaxPayload(); mp > 0 {
		svc.PageBudget = int(mp / 2)
	}
	js, err := jetstream.New(o.NC)
	if err != nil {
		return nil, err
	}
	sctx, scancel := context.WithTimeout(ctx, 15*time.Second)
	err = outbox.EnsureStream(sctx, js, o.Settings.Instance)
	scancel()
	if err != nil {
		return nil, err
	}
	server, err := bus.Start(o.NC, svc, o.Settings.ServiceName, o.Settings.Instance, o.Version, o.Log)
	if err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	inst := &Instance{Svc: svc, JS: js, server: server, cancel: cancel, Registered: make(chan struct{})}
	inst.Indexer = &indexer.Indexer{DB: o.DB, Blob: o.Blob, Log: o.Log, Metrics: o.Metrics}
	if o.Outbox {
		pub := &outbox.Publisher{DB: o.DB, JS: js, NC: o.NC, Svc: svc, Instance: o.Settings.Instance, Log: o.Log, Metrics: o.Metrics}
		inst.goRun(func() { _ = pub.Run(runCtx) })
	}
	if o.Indexer {
		inst.goRun(func() { _ = inst.Indexer.Run(runCtx, js, outbox.StreamName, o.Settings.Instance) })
	}
	if o.Janitor {
		j := &janitor.Janitor{DB: o.DB, Svc: svc, Log: o.Log, Metrics: o.Metrics, ReviewHour: o.ReviewHour, Tick: o.JanitorTick,
			HistoryEnabled: o.HistoryEnabled, HistoryHour: o.HistoryHour, HistoryLocation: o.History.Location}
		inst.goRun(func() { _ = j.Run(runCtx) })
	}
	if o.Register {
		inst.goRun(func() { inst.register(runCtx, js, o) })
	}
	return inst, nil
}

func (i *Instance) goRun(fn func()) {
	i.wg.Add(1)
	go func() {
		defer i.wg.Done()
		fn()
	}()
}

func (i *Instance) register(ctx context.Context, js jetstream.JetStream, o Options) {
	for {
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		rec, err := registry.Register(rctx, js, o.Settings.ServiceName)
		cancel()
		if err == nil {
			o.Log.Info("module registered in JIKU_MODULES", "key", registry.Key, "serviceName", rec.ServiceName, "protocol", rec.Protocol)
			close(i.Registered)
			return
		}
		if errors.Is(err, registry.ErrNoBucket) {
			o.Log.Warn("module not registered yet: " + err.Error())
		} else {
			o.Log.Warn("module registration failed, retrying", "err", err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
	}
}

// Stop unregisters the micro services, then stops the loops. The module record is kept: a
// restart is not an uninstall.
func (i *Instance) Stop() {
	i.server.Stop()
	i.cancel()
	i.wg.Wait()
}
