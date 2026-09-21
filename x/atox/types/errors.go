package types

import errorsmod "cosmossdk.io/errors"

var (
	ErrAtoxDisabled     = errorsmod.Register(ModuleName, 2, "atox module is disabled")
	ErrNothingToClaim   = errorsmod.Register(ModuleName, 3, "nothing to claim")
	ErrUnauthorized     = errorsmod.Register(ModuleName, 4, "unauthorized")
	ErrInvalidAmount    = errorsmod.Register(ModuleName, 5, "invalid amount")
	ErrSupplyCapReached = errorsmod.Register(ModuleName, 6, "atox supply cap reached")
	ErrPoolInsolvent    = errorsmod.Register(ModuleName, 7, "exchange pool balance below settled obligations")
	ErrInvalidIndex     = errorsmod.Register(ModuleName, 8, "invalid conversion index")
)
