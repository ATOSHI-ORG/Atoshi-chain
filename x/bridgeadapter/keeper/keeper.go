package keeper

import (
	"fmt"

	"cosmossdk.io/log"
	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/atoshi-chain/atoshi/v20/x/bridgeadapter/types"
)

// Keeper applies tier-release receipts arriving from Ethereum over Hyperlane.
type Keeper struct {
	cdc              codec.BinaryCodec
	storeKey         storetypes.StoreKey
	authority        string
	atoxKeeper       types.AtoxKeeper
	tokenomicsKeeper types.TokenomicsKeeper
	coreKeeper       types.CoreKeeper
}

func NewKeeper(
	cdc codec.BinaryCodec,
	storeKey storetypes.StoreKey,
	authority string,
	xk types.AtoxKeeper,
	tk types.TokenomicsKeeper,
	ck types.CoreKeeper,
) Keeper {
	return Keeper{
		cdc:              cdc,
		storeKey:         storeKey,
		authority:        authority,
		atoxKeeper:       xk,
		tokenomicsKeeper: tk,
		coreKeeper:       ck,
	}
}

func (k Keeper) Logger(ctx sdk.Context) log.Logger {
	return ctx.Logger().With("module", "x/"+types.ModuleName)
}

func (k Keeper) GetAuthority() string { return k.authority }

// ===== Params =====

func (k Keeper) GetParams(ctx sdk.Context) types.Params {
	bz := ctx.KVStore(k.storeKey).Get(types.KeyParams)
	if bz == nil {
		return types.DefaultParams()
	}
	var p types.Params
	if err := k.cdc.Unmarshal(bz, &p); err != nil {
		panic(fmt.Errorf("failed to unmarshal bridgeadapter params: %w", err))
	}
	return p
}

func (k Keeper) SetParams(ctx sdk.Context, p types.Params) error {
	if err := p.Validate(); err != nil {
		return err
	}
	bz, err := k.cdc.Marshal(&p)
	if err != nil {
		return err
	}
	ctx.KVStore(k.storeKey).Set(types.KeyParams, bz)
	return nil
}

// ===== Receipt state =====

func (k Keeper) GetReceiptState(ctx sdk.Context) types.ReceiptState {
	bz := ctx.KVStore(k.storeKey).Get(types.KeyReceiptState)
	if bz == nil {
		return types.DefaultReceiptState()
	}
	var s types.ReceiptState
	if err := k.cdc.Unmarshal(bz, &s); err != nil {
		panic(fmt.Errorf("failed to unmarshal bridgeadapter receipt state: %w", err))
	}
	return s
}

func (k Keeper) SetReceiptState(ctx sdk.Context, s types.ReceiptState) error {
	bz, err := k.cdc.Marshal(&s)
	if err != nil {
		return err
	}
	ctx.KVStore(k.storeKey).Set(types.KeyReceiptState, bz)
	return nil
}

// PendingConfirmation is what tier judgments have authorised but Ethereum has
// not yet confirmed, in ERC20 units.
//
// Operators should watch this: a figure that stays positive means receipts are
// not arriving, so the ATOX conversion rate has stopped advancing even though
// the tier engine believes it released.
func (k Keeper) PendingConfirmation(ctx sdk.Context) (bridge, project math.Int) {
	params := k.GetParams(ctx)
	state := k.GetReceiptState(ctx)
	authMiner, authProject := k.tokenomicsKeeper.AuthorizedReleases(ctx)

	toErc20 := func(atos math.Int) math.Int {
		if atos.IsNil() || !atos.IsPositive() || params.AtosPerErc20 == 0 {
			return math.ZeroInt()
		}
		return atos.QuoRaw(int64(params.AtosPerErc20))
	}

	bridge = toErc20(authMiner).Sub(state.AppliedToBridge)
	if bridge.IsNegative() {
		bridge = math.ZeroInt()
	}
	project = toErc20(authProject).Sub(state.AppliedToProject)
	if project.IsNegative() {
		project = math.ZeroInt()
	}
	return bridge, project
}
