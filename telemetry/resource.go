package telemetry

import (
	"context"

	config "github.com/ams-soft/nforge/config"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// NewResource describes the service; it is shared by logs and traces so that
// both reach Grafana with the same service_name/deployment_environment.
func NewResource(ctx context.Context, config config.Config) (*resource.Resource, error) {
	return resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(config.App.Name),
			semconv.ServiceVersion(config.App.Version),
			semconv.DeploymentEnvironment(config.Env),
		),
		resource.WithHost(), // host.name
		// No WithProcess(): it includes process.command_args, which may leak secrets passed via flags.
		resource.WithProcessPID(),
		resource.WithProcessExecutableName(),
		resource.WithProcessRuntimeName(),
		resource.WithProcessRuntimeVersion(),
		resource.WithFromEnv(), // OTEL_RESOURCE_ATTRIBUTES=key=value,...
	)
}
