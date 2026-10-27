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

	// Asset bridge and rate limiting.
	ErrBridgeDisabled    = errorsmod.Register(ModuleName, 11, "asset bridge is disabled")
	ErrInvalidAmount     = errorsmod.Register(ModuleName, 12, "invalid amount")
	ErrBelowMinimum      = errorsmod.Register(ModuleName, 13, "amount below the per-transfer minimum")
	ErrDailyCapReached   = errorsmod.Register(ModuleName, 14, "global daily bridge cap reached")
	ErrInboundCapReached = errorsmod.Register(ModuleName, 21, "inbound daily bridge cap reached")
	ErrLargeQuotaReached = errorsmod.Register(ModuleName, 15, "large-transfer budget exhausted; remainder reserved for small transfers")
	ErrAddressCapReached = errorsmod.Register(ModuleName, 16, "per-address daily bridge cap reached")
	ErrCrisisMode        = errorsmod.Register(ModuleName, 17, "migration pool below crisis floor; small transfers only")
	ErrIndivisibleAmount = errorsmod.Register(ModuleName, 18, "amount is not a multiple of the ATOS/ERC20 peg")
	ErrPoolInsufficient  = errorsmod.Register(ModuleName, 19, "migration pool cannot cover the inbound transfer")
	ErrNotConfigured     = errorsmod.Register(ModuleName, 20, "bridge is not fully configured")

	// ErrWrongMailbox rejects a message that did not arrive through the mailbox
	// named in params. Creating a mailbox is permissionless and x/core
	// de-duplicates on (mailboxId, messageId), so without this check one
	// genuine ISM-verified message can be replayed once per mailbox an
	// attacker cares to create.
	ErrWrongMailbox = errorsmod.Register(ModuleName, 22, "message did not arrive through the configured mailbox")

	// ErrIsmNotConfigured is returned by ReceiverIsmId when params.IsmId is
	// unset. It must be neither nil (upstream dereferences the pointer and
	// panics) nor ErrNoReceiverISM (upstream then falls back to the delivering
	// mailbox's default ISM, which on this chain may belong to an attacker).
	ErrIsmNotConfigured = errorsmod.Register(ModuleName, 23, "interchain security module is not configured")
)
