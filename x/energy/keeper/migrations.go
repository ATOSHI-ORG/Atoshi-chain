package keeper

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/atoshi-chain/atoshi/v20/x/energy/types"
)

// RefreshAllSnapshots walks every stored EnergyAccount and resets
// last_balance_snapshot to the current EligibleBalance reading from
// bank. Intended for one-shot use during a chain upgrade that fixes a
// stale-snapshot bug: prior to wiring SendRestriction into bank, every
// inbound transfer left the receiver's snapshot at its initial value,
// silently zeroing accrued capacity for every wallet that received
// funds after creation.
//
// The function is idempotent — re-running it just rewrites the same
// snapshot — so it is safe to invoke from a migration handler that may
// itself be retried.
func (k Keeper) RefreshAllSnapshots(ctx sdk.Context) (refreshed int) {
	now := ctx.BlockTime().Unix()

	addrs := make([]sdk.AccAddress, 0)
	k.IterateAccounts(ctx, func(a types.EnergyAccount) bool {
		addr, err := sdk.AccAddressFromBech32(a.Address)
		if err != nil {
			return false
		}
		addrs = append(addrs, addr)
		return false
	})

	for _, addr := range addrs {
		acct := k.GetEnergyAccount(ctx, addr)
		acct.LastBalanceSnapshot = k.EligibleBalance(ctx, addr)
		acct.LastUpdatedTime = now

		params := k.GetParams(ctx)
		newTxCap := types.TxEnergyCapacity(acct.LastBalanceSnapshot, params)
		if acct.TxEnergyAccrued > newTxCap {
			acct.TxEnergyAccrued = newTxCap
		}
		if acct.DeployEnergyAccrued > params.DeployEnergyCapacity {
			acct.DeployEnergyAccrued = params.DeployEnergyCapacity
		}
		k.SetEnergyAccount(ctx, acct)
		refreshed++
	}
	return refreshed
}
