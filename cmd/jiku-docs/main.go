// Command jiku-docs is the documentation module of Jiku: a NATS micro service with its own
// PostgreSQL and content bucket. See README.md.
//
//	jiku-docs [serve]              run the service (default)
//	jiku-docs migrate              apply database migrations and exit
//	jiku-docs reindex              rebuild the full-text index
//	jiku-docs module deregister    delete the `docs` key of JIKU_MODULES (uninstall)
//	jiku-docs import hedgedoc ...  import notes from a HedgeDoc database
//	jiku-docs history compact      compact the version history now (--document, --dry-run)
//	jiku-docs version
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // DOCS_HISTORY_TIMEZONE works on an image without zoneinfo

	"github.com/gravadigital/jiku-go"
	"github.com/gravadigital/jiku-go/auth"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/gravadigital/jiku-docs/internal/app"
	"github.com/gravadigital/jiku-docs/internal/blob"
	"github.com/gravadigital/jiku-docs/internal/config"
	"github.com/gravadigital/jiku-docs/internal/coreapi"
	"github.com/gravadigital/jiku-docs/internal/importer/hedgedoc"
	"github.com/gravadigital/jiku-docs/internal/indexer"
	"github.com/gravadigital/jiku-docs/internal/logging"
	"github.com/gravadigital/jiku-docs/internal/metrics"
	"github.com/gravadigital/jiku-docs/internal/protocol"
	"github.com/gravadigital/jiku-docs/internal/registry"
	"github.com/gravadigital/jiku-docs/internal/service"
	"github.com/gravadigital/jiku-docs/internal/store"
)

// version is set at build time (-ldflags "-X main.version=1.0.0").
var version = "dev"

func main() {
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	cfg := config.FromEnv()
	log := logging.New(cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch cmd {
	case "serve":
		err = serve(ctx, cfg, log)
	case "migrate":
		err = migrate(ctx, cfg, log)
	case "reindex":
		err = reindex(ctx, cfg, log)
	case "module":
		if len(args) != 1 || args[0] != "deregister" {
			err = errors.New("usage: jiku-docs module deregister")
			break
		}
		err = deregister(ctx, cfg, log)
	case "import":
		if len(args) == 0 || args[0] != "hedgedoc" {
			err = errors.New("usage: jiku-docs import hedgedoc [flags] (see --help)")
			break
		}
		err = importHedgeDoc(ctx, cfg, log, args[1:])
	case "history":
		if len(args) == 0 || args[0] != "compact" {
			err = errors.New("usage: jiku-docs history compact [--document <id>] [--dry-run]")
			break
		}
		err = historyCompact(ctx, cfg, log, args[1:])
	case "storage":
		if len(args) != 1 || args[0] != "init" {
			err = errors.New("usage: jiku-docs storage init")
			break
		}
		err = storageInit(ctx, cfg, log)
	case "healthcheck":
		err = healthcheck(cfg)
	case "version", "--version", "-v":
		fmt.Println(version)
	case "help", "--help", "-h":
		fmt.Println(usage)
	default:
		err = fmt.Errorf("unknown command %q\n\n%s", cmd, usage)
	}
	if err != nil {
		log.Error(err.Error())
		os.Exit(1)
	}
}

const usage = `jiku-docs — módulo de documentación de Jiku

  jiku-docs [serve]             corre el servicio (default)
  jiku-docs migrate             aplica las migraciones y sale
  jiku-docs reindex             regenera el índice de búsqueda completo
  jiku-docs module deregister   borra la clave docs de JIKU_MODULES (desinstalar)
  jiku-docs import hedgedoc     importa notas de HedgeDoc (--help para las opciones)
  jiku-docs history compact     compacta el historial de versiones ahora
                                [--document <id>] [--dry-run]
  jiku-docs storage init        crea el bucket de contenido con versionado y ciclo de vida (idempotente)
  jiku-docs healthcheck          consulta /readyz del proceso local (para el HEALTHCHECK de Docker)
  jiku-docs version

La configuración se lee del entorno: ver .env.dist.`

func openDB(ctx context.Context, cfg config.Config, log *slog.Logger, migrateNow bool) (*pgxpool.Pool, error) {
	if err := cfg.ValidateDatabase(); err != nil {
		return nil, err
	}
	pool, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if migrateNow {
		if _, err := store.Migrate(ctx, pool, log); err != nil {
			pool.Close()
			return nil, err
		}
	}
	return pool, nil
}

func openBlob(cfg config.Config) (*blob.S3, error) {
	if err := cfg.ValidateStorage(); err != nil {
		return nil, err
	}
	return blob.New(blob.Config{
		Endpoint: cfg.S3Endpoint, PublicEndpoint: cfg.S3PublicEndpoint, Region: cfg.S3Region, Bucket: cfg.S3Bucket,
		AccessKey: cfg.S3AccessKey, SecretKey: cfg.S3SecretKey, PathStyle: cfg.S3PathStyle,
	})
}

// connect opens the bus connection as the jiku_docs machine user, with jiku-go: sentinel creds,
// Zitadel token refreshed on every reconnect, and the inbox prefix of its user id.
func connect(ctx context.Context, cfg config.Config) (*jiku.Client, error) {
	key, err := cfg.ServiceKey()
	if err != nil {
		return nil, err
	}
	src, err := auth.NewServiceUser(auth.ServiceUserConfig{Issuer: cfg.ZitadelIssuer, Key: key, ProjectID: cfg.ZitadelProject})
	if err != nil {
		return nil, fmt.Errorf("service user key: %w", err)
	}
	claims, err := src.Claims(ctx)
	if err != nil {
		return nil, fmt.Errorf("obtaining a Zitadel token: %w", err)
	}
	if len(claims.RoleNames()) == 0 {
		return nil, errors.New("the token carries no project roles: check ZITADEL_PROJECT_ID and the roles of the machine user (internal-app + docs)")
	}
	return jiku.Connect(ctx, jiku.Config{
		Servers: cfg.NATSURL, Instance: cfg.Instance, Creds: cfg.NATSCreds, Timeout: cfg.CoreTimeout,
		Name: "jiku-docs", Auth: src,
	})
}

// connectReady connects and checks that core knows this identity. `unknown_caller` at startup
// means the auth event that mirrors identities was lost (connector guide §3.4): a new connection
// forces a new authentication and a new event.
func connectReady(ctx context.Context, cfg config.Config, log *slog.Logger) (*jiku.Client, *coreapi.Cached, error) {
	backoff := 2 * time.Second
	for attempt := 1; ; attempt++ {
		client, err := connect(ctx, cfg)
		if err != nil {
			return nil, nil, err
		}
		core := coreapi.NewCached(coreapi.New(coreapi.JikuTransport{Client: client},
			coreapi.ParseIncludes(os.Getenv("CORE_USER_INCLUDES")), cfg.CoreTimeout))
		u, err := core.API.User(ctx, client.UserID())
		switch {
		case err == nil && u != nil:
			log.Info("connected to the bus", "server", client.ConnectedURL(), "userId", client.UserID(), "roles", u.Roles)
			return client, core, nil
		case err == nil && u == nil && attempt < 6:
			log.Warn("core does not know this identity yet (unknown_caller): reconnecting", "attempt", attempt)
		case err == nil && u == nil:
			log.Error("core still does not know this identity: requests that need core will fail until it does")
			return client, core, nil
		default:
			log.Warn("core did not answer the startup check; continuing (requests that need it answer upstream_unavailable)",
				"err", err.Error())
			return client, core, nil
		}
		_ = client.Close()
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func serve(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	if err := cfg.ValidateServe(); err != nil {
		return err
	}
	history, err := historyPolicy(cfg)
	if err != nil {
		return err
	}
	pool, err := openDB(ctx, cfg, log, true)
	if err != nil {
		return err
	}
	defer pool.Close()
	bs, err := openBlob(cfg)
	if err != nil {
		return err
	}
	if err := bs.Ping(ctx); err != nil {
		return fmt.Errorf("content bucket: %w", err)
	}
	client, core, err := connectReady(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer client.Close()
	m := metrics.New()
	inst, err := app.Start(ctx, app.Options{
		NC: client.Conn(), DB: pool, Blob: bs, Core: core, Log: log, Metrics: m, Version: version,
		Settings: service.Settings{
			Instance: cfg.Instance, ServiceName: cfg.ServiceName, APIUserID: cfg.APIUserID, SelfUserID: client.UserID(),
			WebURL: cfg.WebURL, PublicURL: cfg.PublicURL, SecretKey: cfg.SecretKey,
		},
		Indexer: cfg.IndexerEnabled, Janitor: cfg.JanitorEnabled, Outbox: cfg.OutboxEnabled, Register: true,
		ReviewHour: cfg.RelationReviewHour,
		History:    history, HistoryEnabled: cfg.HistoryCompactionEnabled, HistoryHour: cfg.HistoryCompactionHour,
	})
	if err != nil {
		return err
	}
	log.Info("jiku-docs ready", "version", version, "instance", cfg.Instance, "serviceName", cfg.ServiceName)
	httpErr := make(chan error, 1)
	go func() {
		httpErr <- metrics.Serve(ctx, cfg.HTTPAddr, m, map[string]metrics.Check{
			"database": func(c context.Context) error { return pool.Ping(c) },
			"nats": func(context.Context) error {
				if !client.Conn().IsConnected() {
					return errors.New("disconnected")
				}
				return nil
			},
		})
	}()
	select {
	case <-ctx.Done():
	case err := <-httpErr:
		if err != nil {
			log.Error("health server failed", "err", err.Error())
		}
		<-ctx.Done()
	}
	log.Info("shutting down")
	inst.Stop()
	return nil
}

func migrate(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	pool, err := openDB(ctx, cfg, log, false)
	if err != nil {
		return err
	}
	defer pool.Close()
	n, err := store.Migrate(ctx, pool, log)
	if err != nil {
		return err
	}
	log.Info("migrations done", "applied", n)
	return nil
}

func reindex(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	pool, err := openDB(ctx, cfg, log, true)
	if err != nil {
		return err
	}
	defer pool.Close()
	bs, err := openBlob(cfg)
	if err != nil {
		return err
	}
	ix := &indexer.Indexer{DB: pool, Blob: bs, Log: log}
	start := time.Now()
	err = ix.Reindex(ctx, func(done, total int) {
		if done%200 == 0 || done == total {
			log.Info("reindex progress", "done", done, "total", total)
		}
	})
	if err != nil {
		return err
	}
	log.Info("reindex done", "duration", time.Since(start).String())
	return nil
}

func deregister(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	client, err := connect(ctx, cfg)
	if err != nil {
		return err
	}
	defer client.Close()
	js, err := jetstream.New(client.Conn())
	if err != nil {
		return err
	}
	if err := registry.Deregister(ctx, js); err != nil {
		return err
	}
	log.Info("module deregistered: the docs key of JIKU_MODULES was deleted; stop the service now")
	return nil
}

func importHedgeDoc(ctx context.Context, cfg config.Config, log *slog.Logger, args []string) error {
	opts, err := hedgedoc.ParseFlags(args)
	if err != nil {
		return err
	}
	if opts.ExportMapping != "" || (opts.DryRun && !opts.CheckRelations) {
		return hedgedoc.Run(ctx, opts, nil, log)
	}
	if opts.DryRun {
		// --check-relations: only the bus, to ask core (read only); no base, no bucket.
		client, core, err := connectReady(ctx, cfg, log)
		if err != nil {
			return err
		}
		defer client.Close()
		opts.Relations = core
		return hedgedoc.Run(ctx, opts, nil, log)
	}
	if err := cfg.ValidateServe(); err != nil {
		return err
	}
	pool, err := openDB(ctx, cfg, log, true)
	if err != nil {
		return err
	}
	defer pool.Close()
	bs, err := openBlob(cfg)
	if err != nil {
		return err
	}
	client, core, err := connectReady(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer client.Close()
	svc := service.New(pool, bs, core, log, service.Settings{
		Instance: cfg.Instance, ServiceName: cfg.ServiceName, APIUserID: cfg.APIUserID, SelfUserID: client.UserID(),
		WebURL: cfg.WebURL, PublicURL: cfg.PublicURL, SecretKey: cfg.SecretKey,
	})
	return hedgedoc.Run(ctx, opts, svc, log)
}

func historyPolicy(cfg config.Config) (service.HistoryPolicy, error) {
	loc, err := cfg.ValidateHistory()
	if err != nil {
		return service.HistoryPolicy{}, err
	}
	return service.HistoryPolicy{KeepAll: cfg.HistoryKeepAll, KeepBlocks: cfg.HistoryKeepBlocks, BlockGap: cfg.HistoryBlockGap,
		Location: loc, BatchSize: cfg.HistoryBatchSize, DeleteBatch: cfg.HistoryS3DeleteBatch}, nil
}

// historyCompact runs the compaction on demand: the base and the bucket, no bus. With
// --dry-run it prints what it would remove (one JSON line per document) and touches nothing.
// If the daily run (or another manual one) holds the lock, it says so and exits with an error.
func historyCompact(ctx context.Context, cfg config.Config, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("history compact", flag.ContinueOnError)
	doc := fs.String("document", "", "compactar solo este documento (ULID), tenga o no actividad nueva")
	dry := fs.Bool("dry-run", false, "informar qué borraría, sin borrar filas ni objetos")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if *doc != "" && !protocol.IsULID(*doc) {
		return fmt.Errorf("--document must be a ULID, got %q", *doc)
	}
	policy, err := historyPolicy(cfg)
	if err != nil {
		return err
	}
	pool, err := openDB(ctx, cfg, log, true)
	if err != nil {
		return err
	}
	defer pool.Close()
	bs, err := openBlob(cfg)
	if err != nil {
		return err
	}
	svc := service.New(pool, bs, nil, log, service.Settings{Instance: cfg.Instance, ServiceName: cfg.ServiceName})
	svc.History = policy
	rep, err := svc.CompactHistory(ctx, service.HistoryOptions{DocumentID: *doc, DryRun: *dry})
	if err != nil {
		return err
	}
	if rep.Skipped {
		return service.ErrCompactionBusy
	}
	if *dry {
		enc := json.NewEncoder(os.Stdout)
		for _, p := range rep.Plans {
			if err := enc.Encode(p); err != nil {
				return err
			}
		}
	}
	fmt.Printf("documents=%d versionsPruned=%d bytesFreed=%d objectsDeleted=%d objectsPending=%d errors=%d dryRun=%v duration=%s\n",
		rep.Documents, rep.VersionsPruned, rep.BytesFreed, rep.ObjectsDeleted, rep.ObjectsPending, rep.Errors, rep.DryRun,
		rep.Duration.Round(time.Millisecond))
	if rep.Errors > 0 {
		return fmt.Errorf("%d documents failed (see the log)", rep.Errors)
	}
	return nil
}

// healthcheck asks the local process for readiness; used by Docker, where the image has no shell.
func healthcheck(cfg config.Config) error {
	addr := cfg.HTTPAddr
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + addr + "/readyz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("not ready: HTTP %d", resp.StatusCode)
	}
	return nil
}

// storageInit creates the content bucket idempotently, with versioning and the lifecycle rules.
// The local compose runs it as an init container against the MinIO of jiku.
func storageInit(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	bs, err := openBlob(cfg)
	if err != nil {
		return err
	}
	return initStorage(ctx, bs, cfg.S3Region, cfg.S3Bucket, log)
}

// initStorage is what `storage init` does: bucket with versioning, then the lifecycle rules
// (uploads/ after one day; non-current versions after 7 days, the safety net of the history
// compaction), read back to check that the provider kept them.
func initStorage(ctx context.Context, bs *blob.S3, region, bucket string, log *slog.Logger) error {
	if err := bs.EnsureBucket(ctx, region, true); err != nil {
		return err
	}
	if err := bs.EnsureLifecycle(ctx); err != nil {
		log.Warn("lifecycle rules not set: the janitor still purges uploads, but objects deleted by the history "+
			"compaction stay in the bucket as non-current versions until they are expired by hand", "err", err.Error())
	} else if err := bs.CheckLifecycle(ctx); err != nil {
		log.Warn("lifecycle rules not confirmed by the provider", "err", err.Error())
	}
	log.Info("content bucket ready", "bucket", bucket, "versioning", true,
		"noncurrentVersionExpirationDays", blob.NoncurrentVersionTTL)
	return nil
}
