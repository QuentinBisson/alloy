// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package remotesource // import "github.com/open-telemetry/opentelemetry-collector-contrib/extension/jaegerremotesampling/internal/source/remotesource"

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/jaegertracing/jaeger-idl/proto-gen/api_v2"
	"go.opentelemetry.io/collector/config/configgrpc"
	"go.opentelemetry.io/collector/config/configopaque"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/grafana/alloy/internal/component/otelcol/extension/jaeger_remote_sampling/internal/jaegerremotesampling/internal/source"
)

type grpcRemoteStrategyStore struct {
	headerAdditions map[string]configopaque.String
	delegate        *ConfigManagerProxy
	cache           serviceStrategyCache
}

// NewRemoteSource returns a StrategyStore that delegates to the configured Jaeger gRPC endpoint, making
// extension-configured enhancements (header additions only for now) to the gRPC context of every outbound gRPC call.
// Note: it would be nice to expand the configuration surface to include an optional TTL-based caching behavior
// for service-specific outbound GetSamplingStrategy calls.
func NewRemoteSource(
	conn *grpc.ClientConn,
	grpcClientSettings *configgrpc.ClientConfig,
	reloadInterval time.Duration,
) (source.Source, io.Closer) {
	cache := newNoopStrategyCache()
	if reloadInterval > 0 {
		cache = newServiceStrategyCache(reloadInterval)
	}

	return &grpcRemoteStrategyStore{
		headerAdditions: grpcClientSettings.Headers,
		delegate:        NewConfigManager(conn),
		cache:           cache,
	}, cache
}

func (g *grpcRemoteStrategyStore) GetSamplingStrategy(
	ctx context.Context,
	serviceName string,
) (*api_v2.SamplingStrategyResponse, error) {
	if cachedResponse, ok := g.cache.get(ctx, serviceName); ok {
		return cachedResponse, nil
	}
	freshResult, err := g.delegate.GetSamplingStrategy(g.enhanceContext(ctx), serviceName)
	if err != nil {
		return nil, fmt.Errorf("remote call failed: %w", err)
	}
	g.cache.put(ctx, serviceName, freshResult)
	return freshResult, nil
}

// This function is used to add the extension configuration defined HTTP headers to a given outbound gRPC call's context.
func (g *grpcRemoteStrategyStore) enhanceContext(ctx context.Context) context.Context {
	md := metadata.New(nil)
	for k, v := range g.headerAdditions {
		md.Set(k, string(v))
	}
	return metadata.NewOutgoingContext(ctx, md)
}

func (g *grpcRemoteStrategyStore) Close() error {
	return g.cache.Close()
}
