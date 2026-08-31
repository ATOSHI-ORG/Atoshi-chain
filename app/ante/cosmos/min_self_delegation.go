package cosmos

import (
	"fmt"

	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	errortypes "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/x/authz"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// TokenomicsKeeper is the subset of x/tokenomics this decorator needs.
type TokenomicsKeeper interface {
	GetValidatorMinSelfDelegation(ctx sdk.Context) math.Int
}

// MinSelfDelegationDecorator enforces a chain-wide floor on a validator's own
// stake.
//
// The SDK cannot express this on its own. min_self_delegation is declared by the
// validator in MsgCreateValidator, and x/staking only enforces that the
// validator's self-stake stays at or above whatever it declared -- so declaring
// 1 opts out of the requirement completely, which is exactly what a default
// gentx does.
//
// The floor is therefore applied where the validator sets that value:
//
//   - MsgCreateValidator must declare min_self_delegation >= floor AND must fund
//     the validator with at least the floor. Both matter: the declared minimum
//     without the stake would create a validator the SDK immediately unbonds,
//     and the stake without the declared minimum could be withdrawn the next
//     block.
//   - MsgEditValidator may not lower min_self_delegation below the floor.
//     x/staking already refuses to lower it at all, but that is its rule to
//     change, not an invariant this can rely on.
//
// Once creation is constrained, the SDK's own machinery maintains the invariant:
// it unbonds any validator whose self-stake drops below its declared minimum.
// There is deliberately no check on MsgUndelegate here, which would duplicate
// that logic and get it subtly wrong (it would have to model redelegations,
// slashing and the unbonding queue).
//
// Runs in the AnteHandler rather than a msg server so the rejection also happens
// during CheckTx, keeping doomed txs out of the mempool.
//
// KNOWN LIMITATION: genesis validators never pass through an AnteHandler, so a
// gentx in genesis.json is not covered. docs/check_genesis.py verifies that
// separately -- if the floor could be bypassed at launch the rule would be
// worth little.
type MinSelfDelegationDecorator struct {
	tokenomicsKeeper TokenomicsKeeper
}

// NewMinSelfDelegationDecorator returns a decorator enforcing the chain-wide
// validator self-stake floor from x/tokenomics params.
func NewMinSelfDelegationDecorator(tk TokenomicsKeeper) MinSelfDelegationDecorator {
	return MinSelfDelegationDecorator{tokenomicsKeeper: tk}
}

func (d MinSelfDelegationDecorator) AnteHandle(
	ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler,
) (sdk.Context, error) {
	floor := d.tokenomicsKeeper.GetValidatorMinSelfDelegation(ctx)
	if floor.IsNil() || !floor.IsPositive() {
		return next(ctx, tx, simulate)
	}

	if err := d.check(tx.GetMsgs(), floor, 1); err != nil {
		return ctx, errorsmod.Wrapf(errortypes.ErrInvalidRequest, "%s", err.Error())
	}
	return next(ctx, tx, simulate)
}

// check walks the msgs, descending into authz MsgExec so a create-validator
// cannot be smuggled past the floor by wrapping it. Depth is bounded the same
// way AuthzLimiterDecorator bounds it.
func (d MinSelfDelegationDecorator) check(msgs []sdk.Msg, floor math.Int, nestedLvl int) error {
	if nestedLvl >= maxNestedMsgs {
		return fmt.Errorf("found more nested msgs than permitted. Limit is: %d", maxNestedMsgs)
	}

	for _, msg := range msgs {
		switch m := msg.(type) {
		case *authz.MsgExec:
			inner, err := m.GetMessages()
			if err != nil {
				return err
			}
			if err := d.check(inner, floor, nestedLvl+1); err != nil {
				return err
			}

		case *stakingtypes.MsgCreateValidator:
			if m.MinSelfDelegation.IsNil() || m.MinSelfDelegation.LT(floor) {
				return fmt.Errorf(
					"validator min_self_delegation must be at least %s, got %s",
					floor, m.MinSelfDelegation,
				)
			}
			if m.Value.Amount.LT(floor) {
				return fmt.Errorf(
					"validator must self-delegate at least %s, got %s",
					floor, m.Value.Amount,
				)
			}

		case *stakingtypes.MsgEditValidator:
			// Nil means "leave unchanged", which is always fine.
			if m.MinSelfDelegation != nil && m.MinSelfDelegation.LT(floor) {
				return fmt.Errorf(
					"validator min_self_delegation must be at least %s, got %s",
					floor, *m.MinSelfDelegation,
				)
			}
		}
	}

	return nil
}
