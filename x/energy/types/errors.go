package types

import errorsmod "cosmossdk.io/errors"

var (
	ErrInsufficientEnergy  = errorsmod.Register(ModuleName, 2, "insufficient energy")
	ErrInsufficientBalance = errorsmod.Register(ModuleName, 3, "insufficient balance to back delegation")
	ErrInvalidAmount       = errorsmod.Register(ModuleName, 4, "invalid amount")
	ErrInvalidDuration     = errorsmod.Register(ModuleName, 5, "invalid delegation duration")
	ErrDelegationNotFound  = errorsmod.Register(ModuleName, 6, "delegation not found")
	ErrUnauthorized        = errorsmod.Register(ModuleName, 7, "unauthorized")
	ErrEnergyDisabled      = errorsmod.Register(ModuleName, 8, "energy module is disabled")
	ErrSelfDelegation      = errorsmod.Register(ModuleName, 9, "cannot delegate to self")
)
