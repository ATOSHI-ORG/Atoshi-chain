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

func (k msgServer) ClaimMinerLockedReward(goCtx context.Context, msg *tokenomicstypes.MsgClaimMinerLockedReward) (*tokenomicstypes.MsgClaimMinerLockedRewardResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	bal := k.GetMinerLockedBalance(ctx, msg.ValidatorAddress)
	if !bal.LockedClaimable.IsPositive() {
		return nil, tokenomicstypes.ErrNothingToClaim
	}

	recipient, err := k.validatorToAccAddress(msg.ValidatorAddress)
	if err != nil {
		return nil, err
	}

	coin := sdk.NewCoin(k.baseDenom(), bal.LockedClaimable)
	poolAddr := k.accountKeeper.GetModuleAddress(tokenomicstypes.MinerLockedPoolName)
	available := k.bankKeeper.GetBalance(ctx, poolAddr, k.baseDenom()).Amount
	if available.LT(bal.LockedClaimable) {
		return nil, tokenomicstypes.ErrInsufficientClaimable
	}
	if err := k.bankKeeper.SendCoinsFromModuleToAccount(ctx, tokenomicstypes.MinerLockedPoolName, recipient, sdk.NewCoins(coin)); err != nil {
		return nil, err
	}

	claimed := bal.LockedClaimable
	bal.LockedClaimed = bal.LockedClaimed.Add(claimed)
	bal.LockedClaimable = math.ZeroInt()
	if err := k.SetMinerLockedBalance(ctx, bal); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			tokenomicstypes.EventTypeClaimMinerReward,
			sdk.NewAttribute(tokenomicstypes.AttributeKeyValidator, msg.ValidatorAddress),
			sdk.NewAttribute(tokenomicstypes.AttributeKeyAmount, claimed.String()),
		),
	)

	return &tokenomicstypes.MsgClaimMinerLockedRewardResponse{}, nil
}

func (k msgServer) ClaimProjectTreasuryReward(goCtx context.Context, msg *tokenomicstypes.MsgClaimProjectTreasuryReward) (*tokenomicstypes.MsgClaimProjectTreasuryRewardResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	params := k.GetParams(ctx)
	if params.ProjectTreasuryAddress == "" {
		return nil, tokenomicstypes.ErrNoProjectAddress
	}
	if msg.Authority != params.ProjectTreasuryAddress {
		return nil, tokenomicstypes.ErrUnauthorized
	}

	claimable := k.GetProjectClaimable(ctx)
	if !claimable.IsPositive() {
		return nil, tokenomicstypes.ErrNothingToClaim
	}

	recipient, err := sdk.AccAddressFromBech32(params.ProjectTreasuryAddress)
	if err != nil {
		return nil, err
	}
	coin := sdk.NewCoin(k.baseDenom(), claimable)
	poolAddr := k.accountKeeper.GetModuleAddress(tokenomicstypes.ProjectPoolName)
	available := k.bankKeeper.GetBalance(ctx, poolAddr, k.baseDenom()).Amount
	if available.LT(claimable) {
		return nil, tokenomicstypes.ErrInsufficientClaimable
	}
	if err := k.bankKeeper.SendCoinsFromModuleToAccount(ctx, tokenomicstypes.ProjectPoolName, recipient, sdk.NewCoins(coin)); err != nil {
		return nil, err
	}

	k.SetProjectClaimable(ctx, math.ZeroInt())

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			tokenomicstypes.EventTypeClaimProjectReward,
			sdk.NewAttribute(tokenomicstypes.AttributeKeyRecipient, params.ProjectTreasuryAddress),
			sdk.NewAttribute(tokenomicstypes.AttributeKeyAmount, claimable.String()),
		),
	)

	return &tokenomicstypes.MsgClaimProjectTreasuryRewardResponse{}, nil
}

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
