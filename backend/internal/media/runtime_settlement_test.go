package media

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGateSettlementDecisionNeverInfersCaptureFromProviderCharge(t *testing.T) {
	var envelope gateExecutionEnvelope
	envelope.Projection.QueueState = "terminal"
	envelope.Projection.ChargeState = "charged"
	envelope.Projection.DeliveryState = "failed"
	envelope.ManualReviewRequired = true

	require.Equal(t, "manual_review", gateSettlementDecision(envelope))
}

func TestGateSettlementDecisionAcceptsExplicitAutomaticOrHumanCapture(t *testing.T) {
	var ready gateExecutionEnvelope
	ready.SettlementAction = "capture"
	ready.Projection.QueueState = "terminal"
	ready.Projection.DeliveryState = "ready"
	require.Equal(t, "capture", gateSettlementDecision(ready))

	ready.Projection.DeliveryState = "failed"
	require.Equal(t, "capture", gateSettlementDecision(ready))
}

func TestGateSettlementDecisionAcceptsOnlyExplicitHumanRelease(t *testing.T) {
	var pending gateExecutionEnvelope
	pending.Projection.QueueState = "terminal"
	pending.Projection.ChargeState = "not_charged"
	require.Equal(t, "manual_review", gateSettlementDecision(pending))

	pending.SettlementAction = "release"
	require.Equal(t, "release", gateSettlementDecision(pending))
}
