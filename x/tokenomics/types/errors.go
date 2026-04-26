package types

import errorsmod "cosmossdk.io/errors"

var (
	ErrInsufficientClaimable = errorsmod.Register(ModuleName, 2, "insufficient claimable balance")
	ErrNothingToClaim        = errorsmod.Register(ModuleName, 3, "nothing to claim")
	ErrUnauthorized          = errorsmod.Register(ModuleName, 4, "unauthorized")
	ErrInvalidMerkleProof    = errorsmod.Register(ModuleName, 5, "invalid merkle proof")
	ErrAlreadyClaimed        = errorsmod.Register(ModuleName, 6, "already claimed migration tokens")
	ErrMinerPoolExhausted    = errorsmod.Register(ModuleName, 7, "miner pool exhausted")
	ErrNoProjectAddress      = errorsmod.Register(ModuleName, 8, "project treasury address not set")
)
