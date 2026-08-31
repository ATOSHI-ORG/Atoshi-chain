// Copyright Tharsis Labs Ltd.(Evmos)
// SPDX-License-Identifier:ENCL-1.0(https://github.com/evmos/evmos/blob/main/LICENSE)
package utils

import (
	"errors"
	"time"

	errorsmod "cosmossdk.io/errors"
	"github.com/atoshi-chain/atoshi/v20/testutil/integration/evmos/grpc"
	"github.com/atoshi-chain/atoshi/v20/testutil/integration/evmos/network"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// errAccrualTimeout is the sentinel the bounded wait loops return, so callers
// can tell "nothing ever accrued" apart from a query or block-production error.
var errAccrualTimeout = errors.New("timed out waiting for accrual")

// maxAccrualRounds bounds the wait loops below.
//
// They used to spin unbounded, each round advancing the chain a simulated week.
// If the target denom never accrues, that is not a test failure but a hang: the
// suite burns the whole `go test` timeout and dies in a goroutine dump with no
// statement of what it was waiting for. It is a real trap on this chain, where
// inflation is disabled and block rewards are paid in ATOX, so empty blocks
// accrue no base-denom rewards at all and the condition can never be met.
//
// 60 rounds is over a simulated year, far past anything a legitimate test needs.
const maxAccrualRounds = 60

// accrued reports whether got covers every denom and amount in want.
//
// Both wait loops used to test only the base denom, ignoring whatever the
// caller actually passed. On this chain block rewards are ATOX and inflation is
// off, so the base denom never accrues from empty blocks and the condition was
// unsatisfiable regardless of what the caller asked for. Honouring the caller's
// denoms lets a test wait for the coin it really expects.
func accrued(got, want sdk.DecCoins) bool {
	for _, c := range want {
		if got.AmountOf(c.Denom).LT(c.Amount) {
			return false
		}
	}
	return true
}

// WaitToAccrueRewards is a helper function that waits for rewards to
// accumulate up to a specified expected amount
func WaitToAccrueRewards(n network.Network, gh grpc.Handler, delegatorAddr string, expRewards sdk.DecCoins) (sdk.DecCoins, error) {
	var (
		err     error
		lapse   = time.Hour * 24 * 7 // one week
		rewards = sdk.DecCoins{}
	)

	for i := 0; !accrued(rewards, expRewards); i++ {
		if i >= maxAccrualRounds {
			return nil, errorsmod.Wrapf(
				errAccrualTimeout,
				"rewards for %s: wanted at least %q, have %q after %d rounds (~%d simulated weeks)",
				delegatorAddr, expRewards, rewards, i, i,
			)
		}
		rewards, err = checkRewardsAfter(n, gh, delegatorAddr, lapse)
		if err != nil {
			return nil, errorsmod.Wrap(err, "error checking rewards")
		}
	}

	return rewards, err
}

// checkRewardsAfter is a helper function that checks the accrued rewards
// after the provided time lapse
func checkRewardsAfter(n network.Network, gh grpc.Handler, delegatorAddr string, lapse time.Duration) (sdk.DecCoins, error) {
	err := n.NextBlockAfter(lapse)
	if err != nil {
		return nil, errorsmod.Wrap(err, "failed to commit block after voting period ends")
	}

	res, err := gh.GetDelegationTotalRewards(delegatorAddr)
	if err != nil {
		return nil, errorsmod.Wrapf(err, "error while querying for delegation rewards")
	}

	return res.Total, nil
}

// WaitToAccrueCommission is a helper function that waits for commission to
// accumulate up to a specified expected amount
func WaitToAccrueCommission(n network.Network, gh grpc.Handler, validatorAddr string, expCommission sdk.DecCoins) (sdk.DecCoins, error) {
	var (
		err        error
		lapse      = time.Hour * 24 * 7 // one week
		commission = sdk.DecCoins{}
	)

	for i := 0; !accrued(commission, expCommission); i++ {
		if i >= maxAccrualRounds {
			return nil, errorsmod.Wrapf(
				errAccrualTimeout,
				"commission for %s: wanted at least %q, have %q after %d rounds (~%d simulated weeks)",
				validatorAddr, expCommission, commission, i, i,
			)
		}
		commission, err = checkCommissionAfter(n, gh, validatorAddr, lapse)
		if err != nil {
			return nil, errorsmod.Wrap(err, "error checking commission")
		}
	}

	return commission, err
}

// checkCommissionAfter is a helper function that checks the accrued commission
// after the provided time lapse
func checkCommissionAfter(n network.Network, gh grpc.Handler, valAddr string, lapse time.Duration) (sdk.DecCoins, error) {
	err := n.NextBlockAfter(lapse)
	if err != nil {
		return nil, errorsmod.Wrap(err, "failed to commit block after voting period ends")
	}

	res, err := gh.GetValidatorCommission(valAddr)
	if err != nil {
		return nil, errorsmod.Wrapf(err, "error while querying for delegation rewards")
	}

	return res.Commission.Commission, nil
}
