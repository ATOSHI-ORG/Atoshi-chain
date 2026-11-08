package keeper

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	tokenomicstypes "github.com/atoshi-chain/atoshi/v20/x/tokenomics/types"
)

type msgServer struct {
	Keeper
}

func NewMsgServerImpl(keeper Keeper) tokenomicstypes.MsgServer {
	return &msgServer{Keeper: keeper}
}

var _ tokenomicstypes.MsgServer = msgServer{}

func (k msgServer) ClaimMigrationTokens(goCtx context.Context, msg *tokenomicstypes.MsgClaimMigrationTokens) (*tokenomicstypes.MsgClaimMigrationTokensResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	if k.HasMigrationClaimed(ctx, msg.Claimer) {
		return nil, tokenomicstypes.ErrAlreadyClaimed
	}

	params := k.GetParams(ctx)
	if params.MigrationMerkleRoot == "" {
		return nil, tokenomicstypes.ErrInvalidMerkleProof
	}

	if params.MigrationClaimEndTimeUnix > 0 && ctx.BlockTime().Unix() > params.MigrationClaimEndTimeUnix {
		return nil, tokenomicstypes.ErrUnauthorized
	}

	ok := verifyMerkleClaim(msg.Claimer, msg.Amount, msg.MerkleProof, params.MigrationMerkleRoot)
	if !ok {
		return nil, tokenomicstypes.ErrInvalidMerkleProof
	}

	recipient, err := sdk.AccAddressFromBech32(msg.Claimer)
	if err != nil {
		return nil, err
	}
	coin := sdk.NewCoin(k.baseDenom(), msg.Amount)
	poolAddr := k.accountKeeper.GetModuleAddress(tokenomicstypes.MigrationPoolName)
	available := k.bankKeeper.GetBalance(ctx, poolAddr, k.baseDenom()).Amount
	if available.LT(msg.Amount) {
		return nil, tokenomicstypes.ErrInsufficientClaimable
	}
	if err := k.bankKeeper.SendCoinsFromModuleToAccount(ctx, tokenomicstypes.MigrationPoolName, recipient, sdk.NewCoins(coin)); err != nil {
		return nil, err
	}

	k.SetMigrationClaimed(ctx, msg.Claimer)

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			tokenomicstypes.EventTypeClaimMigration,
			sdk.NewAttribute(tokenomicstypes.AttributeKeyRecipient, msg.Claimer),
			sdk.NewAttribute(tokenomicstypes.AttributeKeyAmount, msg.Amount.String()),
		),
	)

	return &tokenomicstypes.MsgClaimMigrationTokensResponse{}, nil
}

func (k msgServer) UpdateParams(goCtx context.Context, msg *tokenomicstypes.MsgUpdateParams) (*tokenomicstypes.MsgUpdateParamsResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	if k.GetAuthority() != msg.Authority {
		return nil, fmt.Errorf("unauthorized: expected %s, got %s", k.GetAuthority(), msg.Authority)
	}
	if err := k.SetParams(ctx, msg.Params); err != nil {
		return nil, err
	}
	ctx.EventManager().EmitEvent(sdk.NewEvent(tokenomicstypes.EventTypeUpdateParams))
	return &tokenomicstypes.MsgUpdateParamsResponse{}, nil
}

// SpendSlashPool pays confiscated validator stake out of slash_pool.
//
// The pool fills from x/staking's Slash override, which confiscates a
// validator's own stake instead of burning a share of everyone's. Nothing else
// writes to it and nothing else reads from it, so its balance is exactly
// "confiscated, not yet returned" and this handler is the only way that figure
// goes down.
//
// Deliberately thin: no business rules about who may be paid or what for. The
// proposal text carries that, and a rule compiled in here would need an upgrade
// to change at the moment it most needed changing.
func (k msgServer) SpendSlashPool(goCtx context.Context, msg *tokenomicstypes.MsgSpendSlashPool) (*tokenomicstypes.MsgSpendSlashPoolResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	if k.GetAuthority() != msg.Authority {
		return nil, fmt.Errorf("unauthorized: expected %s, got %s", k.GetAuthority(), msg.Authority)
	}
	recipient, err := sdk.AccAddressFromBech32(msg.Recipient)
	if err != nil {
		return nil, fmt.Errorf("invalid recipient %q: %w", msg.Recipient, err)
	}
	// A blocked address is one the bank refuses to credit -- a module account or
	// a precompile. Caught here so the proposal fails on submission rather than
	// after a week of voting.
	if k.bankKeeper.BlockedAddr(recipient) {
		return nil, fmt.Errorf("recipient %s is not allowed to receive funds", msg.Recipient)
	}
	if !msg.Amount.IsValid() || !msg.Amount.IsAllPositive() {
		return nil, fmt.Errorf("amount must be a valid positive coin set, got %s", msg.Amount)
	}

	poolAddr := k.accountKeeper.GetModuleAddress(tokenomicstypes.SlashPoolName)
	if poolAddr == nil {
		return nil, fmt.Errorf("%s module account does not exist", tokenomicstypes.SlashPoolName)
	}
	// SendCoinsFromModuleToAccount would fail on its own, but its error names a
	// bech32 module address and a denom, which is not enough to tell a proposal
	// that asked for too much from one that named the wrong denom entirely.
	balance := k.bankKeeper.GetAllBalances(ctx, poolAddr)
	if !balance.IsAllGTE(msg.Amount) {
		return nil, fmt.Errorf("slash pool holds %s, cannot pay %s", balance, msg.Amount)
	}
	if err := k.bankKeeper.SendCoinsFromModuleToAccount(
		ctx, tokenomicstypes.SlashPoolName, recipient, msg.Amount,
	); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		tokenomicstypes.EventTypeSpendSlashPool,
		sdk.NewAttribute(tokenomicstypes.AttributeKeyRecipient, msg.Recipient),
		sdk.NewAttribute(tokenomicstypes.AttributeKeyAmount, msg.Amount.String()),
		sdk.NewAttribute(tokenomicstypes.AttributeKeyRemaining, balance.Sub(msg.Amount...).String()),
	))
	return &tokenomicstypes.MsgSpendSlashPoolResponse{}, nil
}

// migrationLeafHash computes the canonical Merkle leaf for a migration claim.
// Encoding (length-prefixed, double-hashed; OpenZeppelin-style):
//
//	payload  = uvarint(len(claimer)) || claimer || uvarint(len(amountBytes)) || amountBytes
//	inner    = sha256(payload)
//	leaf     = sha256(inner)
//
// Length prefixes prevent boundary ambiguity (e.g. ("a","b1") vs ("ab","1")).
// Double-hashing prevents second-preimage attacks where a crafted leaf could
// collide with an internal node hash.
func migrationLeafHash(claimer string, amount math.Int) []byte {
	amountBytes := []byte(amount.String())
	var buf []byte
	var lenBuf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(lenBuf[:], uint64(len(claimer)))
	buf = append(buf, lenBuf[:n]...)
	buf = append(buf, claimer...)
	n = binary.PutUvarint(lenBuf[:], uint64(len(amountBytes)))
	buf = append(buf, lenBuf[:n]...)
	buf = append(buf, amountBytes...)
	inner := sha256.Sum256(buf)
	leaf := sha256.Sum256(inner[:])
	return leaf[:]
}

func verifyMerkleClaim(claimer string, amount math.Int, proof [][]byte, rootHex string) bool {
	root, err := hex.DecodeString(rootHex)
	if err != nil || len(root) == 0 {
		return false
	}

	current := migrationLeafHash(claimer, amount)
	for _, sibling := range proof {
		if len(sibling) != sha256.Size {
			return false
		}
		combined := make([]byte, 0, len(current)+len(sibling))
		// Sort the pair so the proof is direction-agnostic (OpenZeppelin convention).
		if bytes.Compare(current, sibling) <= 0 {
			combined = append(combined, current...)
			combined = append(combined, sibling...)
		} else {
			combined = append(combined, sibling...)
			combined = append(combined, current...)
		}
		h := sha256.Sum256(combined)
		current = h[:]
	}
	return bytes.Equal(current, root)
}
