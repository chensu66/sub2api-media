package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeGroupModelPricingRejectsMediaPricing(t *testing.T) {
	pricing := []ChannelModelPricing{{
		Platform: PlatformMedia,
		Models:   []string{"gpt-image-1"},
	}}

	_, err := normalizeGroupModelPricing(PlatformMedia, pricing)
	require.EqualError(t, err, "media groups use Gate quotes and do not support model pricing")
}

func TestNormalizeGroupModelPricingAllowsEmptyMediaPricing(t *testing.T) {
	pricing, err := normalizeGroupModelPricing(PlatformMedia, nil)

	require.NoError(t, err)
	require.Empty(t, pricing)
}
