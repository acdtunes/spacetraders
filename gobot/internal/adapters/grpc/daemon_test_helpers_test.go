package grpc

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// Shared DaemonServer test helpers. Originally defined in the goods-factory coordinator's test
// files; relocated here when the factory ops were retired (sp-hoj8u) since they are generic,
// registry-backed helpers used by surviving tests (shipyard-backfill, boot-standing, construction
// config round-trip). The server builders wire the REAL container-spec registry so buildCommandForType
// exercises the true resolve+build path.

func newFactoryTestServer() *DaemonServer {
	s := &DaemonServer{containerSpecs: make(map[string]ContainerSpec)}
	s.registerContainerSpecs()
	return s
}

func newManufacturingTestServer(live config.ManufacturingConfig) *DaemonServer {
	s := &DaemonServer{
		containerSpecs:      make(map[string]ContainerSpec),
		manufacturingConfig: live,
	}
	s.registerContainerSpecs()
	return s
}

// jsonRoundTrip marshals a launch config and unmarshals it back, reproducing the JSON persist→recover
// boundary a container config crosses on a daemon restart (numbers arrive back as float64), so a test
// can assert a recovered command is rebuilt identically.
func jsonRoundTrip(t *testing.T, launchConfig map[string]interface{}) map[string]interface{} {
	t.Helper()
	raw, err := json.Marshal(launchConfig)
	require.NoError(t, err)
	var persisted map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &persisted))
	return persisted
}
