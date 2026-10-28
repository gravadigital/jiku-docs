// Package bus registers the two NATS micro services, `{serviceName}-commands` and
// `{serviceName}-queries`, each with its own queue group, on the same connection — the pattern of
// core's BusHost. It only moves bytes: everything else is package service.
package bus

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/micro"

	"github.com/gravadigital/jiku-docs/internal/protocol"
	"github.com/gravadigital/jiku-docs/internal/service"
)

// Correlation headers accepted from callers (the first present wins).
var correlationHeaders = []string{"Correlation-Id", "X-Correlation-Id", "Jiku-Trace-Id"}

// MaxInFlight bounds concurrent requests per service: micro calls a handler sequentially per
// endpoint, so each request runs in its own goroutine.
const MaxInFlight = 64

// RequestTimeout bounds a single request on the server side.
const RequestTimeout = 30 * time.Second

// Server holds the registered micro services.
type Server struct {
	services []micro.Service
	sem      chan struct{}
	stop     sync.Once
}

var semverRe = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

// SemVer returns v when it is a semantic version (micro requires one) or 0.0.0-<v> otherwise.
func SemVer(v string) string {
	v = strings.TrimPrefix(v, "v")
	if semverRe.MatchString(v) {
		return v
	}
	clean := regexp.MustCompile(`[^0-9A-Za-z.-]`).ReplaceAllString(v, "-")
	if clean == "" {
		clean = "dev"
	}
	return "0.0.0-" + clean
}

// Start registers both services.
func Start(nc *nats.Conn, svc *service.Service, serviceName, instance, version string, log *slog.Logger) (*Server, error) {
	s := &Server{sem: make(chan struct{}, 2*MaxInFlight)}
	version = SemVer(version)
	planes := []struct {
		plane, desc string
		endpoints   []service.Endpoint
	}{
		{protocol.PlaneCommands, "jiku-docs: write plane", svc.Commands()},
		{protocol.PlaneQueries, "jiku-docs: read plane", svc.Queries()},
	}
	for _, p := range planes {
		name := protocol.ServiceFor(serviceName, p.plane)
		srv, err := micro.AddService(nc, micro.Config{
			Name: name, Version: version, Description: p.desc, QueueGroup: name,
			Metadata: map[string]string{"instance": instance, "protocol": protocol.ProtocolVersion},
		})
		if err != nil {
			s.Stop()
			return nil, err
		}
		s.services = append(s.services, srv)
		group := srv.AddGroup(protocol.GroupSubject(instance, name))
		plane := p.plane
		for _, ep := range p.endpoints {
			subject := protocol.EndpointSubject(ep.Pattern)
			handler := micro.HandlerFunc(func(req micro.Request) { s.handle(svc, plane, req) })
			if err := group.AddEndpoint(protocol.EndpointName(ep.Pattern), handler, micro.WithEndpointSubject(subject)); err != nil {
				s.Stop()
				return nil, err
			}
		}
		log.Info("micro service registered", "service", name, "endpoints", len(p.endpoints))
	}
	return s, nil
}

func (s *Server) handle(svc *service.Service, plane string, req micro.Request) {
	s.sem <- struct{}{}
	go func() {
		defer func() { <-s.sem }()
		ctx, cancel := context.WithTimeout(context.Background(), RequestTimeout)
		defer cancel()
		corr := ""
		if h := req.Headers(); h != nil {
			for _, k := range correlationHeaders {
				if v := nats.Header(h).Get(k); v != "" {
					corr = v
					break
				}
			}
		}
		if len(corr) > 128 {
			corr = corr[:128]
		}
		reply := svc.Dispatch(ctx, service.Request{Plane: plane, Subject: req.Subject(), Data: req.Data(), CorrelationID: corr})
		body := reply.Encode()
		if reply.Status == protocol.StatusSuccess {
			_ = req.Respond(body)
			return
		}
		// Like core: the envelope travels in the body; the Nats-Service-Error headers are added,
		// never a replacement for it.
		_ = req.Error("500", reply.ErrorCode, body)
	}()
}

// Stop unregisters the services (in-flight requests finish).
func (s *Server) Stop() {
	s.stop.Do(func() {
		for _, srv := range s.services {
			_ = srv.Stop()
		}
		for i := 0; i < cap(s.sem); i++ {
			s.sem <- struct{}{}
		}
	})
}
