package dto

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGroupFromServicePreservesRawKiroForceCacheCreation(t *testing.T) {
	src := &service.Group{
		ID:                            12,
		Name:                          "kiro",
		Platform:                      service.PlatformKiro,
		KiroCacheEmulationEnabled:     false,
		KiroCacheForceCreationEnabled: true,
		KiroCacheEmulationRatio:       1,
	}

	got := GroupFromService(src)

	require.NotNil(t, got)
	require.False(t, got.KiroCacheEmulationEnabled)
	require.True(t, got.KiroCacheForceCreationEnabled)
}
