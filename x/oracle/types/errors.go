package types

import errorsmod "cosmossdk.io/errors"

var (
	ErrUnauthorizedFeeder = errorsmod.Register(ModuleName, 2, "unauthorized feeder")
	ErrInvalidPrice       = errorsmod.Register(ModuleName, 3, "invalid price")
	ErrInvalidVolume      = errorsmod.Register(ModuleName, 4, "invalid volume")
	ErrInvalidSource      = errorsmod.Register(ModuleName, 5, "invalid source")
	ErrPriceNotFound      = errorsmod.Register(ModuleName, 6, "price not found")
	ErrStalePrice         = errorsmod.Register(ModuleName, 7, "stale price data")
)
