package types

import errorsmod "cosmossdk.io/errors"

var (
	ErrDisabled           = errorsmod.Register(ModuleName, 2, "bridge adapter is disabled")
	ErrUnauthorized       = errorsmod.Register(ModuleName, 3, "unauthorized")
	ErrWrongOrigin        = errorsmod.Register(ModuleName, 4, "message did not originate from the configured Ethereum domain")
	ErrWrongSender        = errorsmod.Register(ModuleName, 5, "message sender is not the configured tier release vault")
	ErrInvalidPayload     = errorsmod.Register(ModuleName, 6, "invalid receipt payload")
	ErrTargetWentBackward = errorsmod.Register(ModuleName, 7, "cumulative target is below what was already applied")
	ErrExceedsAuthorized  = errorsmod.Register(ModuleName, 8, "receipt reports more than tier judgments authorized")
	ErrUnknownRecipient   = errorsmod.Register(ModuleName, 9, "unknown recipient address")
	ErrAppNotInitialized  = errorsmod.Register(ModuleName, 10, "app id has not been assigned")
)
