// Copyright Tharsis Labs Ltd.(Evmos)
// SPDX-License-Identifier:ENCL-1.0(https://github.com/evmos/evmos/blob/main/LICENSE)

package factory

import (
	"github.com/atoshi-chain/atoshi/v20/testutil/integration/evmos/grpc"
	"github.com/atoshi-chain/atoshi/v20/testutil/integration/evmos/network"
)

const (
	// GasAdjustment multiplies the simulated gas to get the limit the harness
	// submits. Raised from 1.7, which sat directly on top of a real ratio and so
	// failed intermittently.
	//
	// Measured: a MsgSend funding a ClawbackVestingAccount simulates at 107,316
	// but executes at 182,670 -- a ratio of 1.702. At 1.7 the tx was submitted
	// with gasWanted 182,437 and aborted "out of gas in location: ReadPerByte"
	// 233 gas short, i.e. by 0.13%. Every test that funds a vesting account
	// inherited that coin flip.
	//
	// Simulation genuinely under-reporting a plain send by ~70% is its own
	// question and predates this fork (it reproduces on main), but it is not
	// exposed by this repo's own tooling: the ops scripts pass an explicit
	// --gas 500000 rather than --gas auto. Flagged for audit; the number here
	// only needs enough margin that tests stop depending on the exact ratio.
	GasAdjustment = float64(2.5)
)

// CoreTxFactory is the interface that wraps the methods
// to build and broadcast cosmos transactions, and also
// includes module-specific transactions
type CoreTxFactory interface {
	BaseTxFactory
	DistributionTxFactory
	StakingTxFactory
	FundTxFactory
}

var _ CoreTxFactory = (*IntegrationTxFactory)(nil)

// IntegrationTxFactory is a helper struct to build and broadcast transactions
// to the network on integration tests. This is to simulate the behavior of a real user.
type IntegrationTxFactory struct {
	BaseTxFactory
	DistributionTxFactory
	StakingTxFactory
	FundTxFactory
}

// New creates a new IntegrationTxFactory instance
func New(
	network network.Network,
	grpcHandler grpc.Handler,
) CoreTxFactory {
	bf := newBaseTxFactory(network, grpcHandler)
	return &IntegrationTxFactory{
		bf,
		newDistrTxFactory(bf),
		newStakingTxFactory(bf),
		newFundTxFactory(bf),
	}
}
