package atox

import (
	"fmt"
	"math/big"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"

	cmn "github.com/atoshi-chain/atoshi/v20/precompiles/common"
	atoxtypes "github.com/atoshi-chain/atoshi/v20/x/atox/types"
	"github.com/atoshi-chain/atoshi/v20/x/evm/core/vm"
)

const (
	// ClaimMethod converts the caller's outstanding ATOX claim into ATOS.
	ClaimMethod = "claim"
	// ClaimableMethod reports what claim() would pay right now.
	ClaimableMethod = "claimable"
	// BurnOnSettleMethod reports the ATOX a settlement would destroy right now.
	BurnOnSettleMethod = "burnOnSettle"
	// GlobalIndexMethod reports the cumulative liao owed per aatox, scaled 1e18.
	GlobalIndexMethod = "globalIndex"
)

// ErrCallerNotOrigin is returned when a contract tries to claim for the account
// that signed the transaction.
const ErrCallerNotOrigin = "claim must be called directly by the account whose ATOX is being converted: caller %s, signer %s"

// Claim converts the caller's outstanding ATOX claim into ATOS.
//
// The claimer is always evm.Origin -- the account that signed this transaction.
// There is no claimer argument and no authz path, for the same reason bridgeOut
// has neither: an argument would have to be checked against origin anyway, and
// letting a contract claim for a user would mean any contract they merely
// interact with could burn their ATOX. Nothing needs that today, so a contract
// caller is refused outright rather than half-supported.
//
// Reverting on "nothing to claim" is deliberate and comes from the keeper. A
// wallet is expected to call claimable() first; succeeding with zero would let a
// batched caller pay gas for a no-op and, worse, make a UI that offers the
// button unconditionally look like it worked.
func (p Precompile) Claim(
	ctx sdk.Context,
	origin common.Address,
	contract *vm.Contract,
	stateDB vm.StateDB,
	method *abi.Method,
	args []interface{},
) ([]byte, error) {
	if len(args) != 0 {
		return nil, fmt.Errorf(cmn.ErrInvalidNumberOfArgs, 0, len(args))
	}

	// A contract claiming on someone else's behalf is refused. See the doc
	// comment: this is the whole authorization model.
	if contract.CallerAddress != origin {
		return nil, fmt.Errorf(ErrCallerNotOrigin, contract.CallerAddress, origin)
	}

	claimer := sdk.AccAddress(origin.Bytes())

	// Zero minPayout: the holder asked for this explicitly, so the per-payout
	// floor that exists to keep the old automatic sweep from writing dust does
	// not apply. It would only turn a deliberate small claim into a revert.
	paid, err := p.atoxKeeper.PayoutPending(ctx, claimer, math.ZeroInt(), atoxtypes.TriggerClaim)
	if err != nil {
		return nil, err
	}
	if !paid.IsPositive() {
		return nil, atoxtypes.ErrNothingToClaim
	}

	// Burned equals paid by construction -- one aatox per liao. Reported
	// separately in the event so a reader does not have to know that rule to
	// account for the supply change.
	if err := p.EmitClaimAtosEvent(ctx, stateDB, origin, paid.BigInt(), paid.BigInt()); err != nil {
		return nil, err
	}

	return method.Outputs.Pack(paid.BigInt())
}

// Claimable reports what claim() would pay `account` right now, in aatos.
//
// Sums the settled and unsettled parts, because that is what the holder will
// actually receive and what the caller must compare against zero before
// offering the button.
func (p Precompile) Claimable(
	ctx sdk.Context,
	method *abi.Method,
	args []interface{},
) ([]byte, error) {
	account, err := addressArg(args)
	if err != nil {
		return nil, err
	}

	pending, unsettled := p.atoxKeeper.Claimable(ctx, account)
	return method.Outputs.Pack(pending.Add(unsettled).BigInt())
}

// BurnOnSettle reports the ATOX a settlement would destroy right now, in aatox.
//
// This is the unsettled part alone, not the total: the settled part was already
// paid for by ATOX that is already gone. A wallet needs it to size a "send max"
// of ATOX, since any transfer settles the sender first and so spends this much
// of the displayed balance before the transfer is applied.
func (p Precompile) BurnOnSettle(
	ctx sdk.Context,
	method *abi.Method,
	args []interface{},
) ([]byte, error) {
	account, err := addressArg(args)
	if err != nil {
		return nil, err
	}

	_, unsettled := p.atoxKeeper.Claimable(ctx, account)
	return method.Outputs.Pack(unsettled.BigInt())
}

// GlobalIndex reports the cumulative liao owed per aatox held, scaled by 1e18.
func (p Precompile) GlobalIndex(
	ctx sdk.Context,
	method *abi.Method,
	args []interface{},
) ([]byte, error) {
	if len(args) != 0 {
		return nil, fmt.Errorf(cmn.ErrInvalidNumberOfArgs, 0, len(args))
	}

	// LegacyDec is already an integer scaled by 1e18 internally, so BigInt()
	// yields the 1e18-scaled value the ABI promises without a second conversion
	// that could round.
	idx := p.atoxKeeper.GetGlobalState(ctx).GlobalIndex
	return method.Outputs.Pack(idx.BigInt())
}

// addressArg unpacks the single address argument the read methods share.
func addressArg(args []interface{}) (sdk.AccAddress, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf(cmn.ErrInvalidNumberOfArgs, 1, len(args))
	}
	addr, ok := args[0].(common.Address)
	if !ok {
		return nil, fmt.Errorf(cmn.ErrInvalidType, "account", common.Address{}, args[0])
	}
	return sdk.AccAddress(addr.Bytes()), nil
}

var _ = big.NewInt
