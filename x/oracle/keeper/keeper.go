package keeper

import (
	"fmt"

	"cosmossdk.io/log"
	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/atoshi-chain/atoshi/v20/x/oracle/types"
)

type Keeper struct {
	storeKey  storetypes.StoreKey
	cdc       codec.BinaryCodec
	authority sdk.AccAddress
}

func NewKeeper(
	storeKey storetypes.StoreKey,
	cdc codec.BinaryCodec,
	authority sdk.AccAddress,
) Keeper {
	if err := sdk.VerifyAddressFormat(authority); err != nil {
		panic(err)
	}
	return Keeper{
		storeKey:  storeKey,
		cdc:       cdc,
		authority: authority,
	}
}

func (k Keeper) Logger(ctx sdk.Context) log.Logger {
	return ctx.Logger().With("module", "x/"+types.ModuleName)
}

// --- Params ---

func (k Keeper) GetParams(ctx sdk.Context) types.Params {
	store := ctx.KVStore(k.storeKey)
	bz := store.Get(types.KeyPrefixParams)
	if bz == nil {
		return types.DefaultParams()
	}
	var params types.Params
	if err := k.cdc.Unmarshal(bz, &params); err != nil {
		panic(fmt.Errorf("failed to unmarshal oracle params: %w", err))
	}
	return params
}

func (k Keeper) SetParams(ctx sdk.Context, params types.Params) error {
	if err := params.Validate(); err != nil {
		return err
	}
	store := ctx.KVStore(k.storeKey)
	bz, err := k.cdc.Marshal(&params)
	if err != nil {
		return err
	}
	store.Set(types.KeyPrefixParams, bz)
	return nil
}

// --- Price Data ---

func (k Keeper) SetCurrentPrice(ctx sdk.Context, price types.PriceData) error {
	store := ctx.KVStore(k.storeKey)
	bz, err := k.cdc.Marshal(&price)
	if err != nil {
		return err
	}
	store.Set(types.KeyPrefixCurrentPrice, bz)
	return nil
}

func (k Keeper) GetCurrentPrice(ctx sdk.Context) (types.PriceData, error) {
	store := ctx.KVStore(k.storeKey)
	bz := store.Get(types.KeyPrefixCurrentPrice)
	if bz == nil {
		return types.PriceData{}, types.ErrPriceNotFound
	}
	var price types.PriceData
	if err := k.cdc.Unmarshal(bz, &price); err != nil {
		return types.PriceData{}, err
	}
	return price, nil
}

// --- Price History ---

func (k Keeper) AppendPriceHistory(ctx sdk.Context, price types.PriceData) error {
	store := ctx.KVStore(k.storeKey)
	// Key includes feeder so concurrent same-block reports from
	// different feeders don't overwrite each other (surfaced during
	// the audit Issue 4 fix; see PriceHistoryKey doc-comment).
	key := types.PriceHistoryKey(price.Timestamp, price.Feeder)
	bz, err := k.cdc.Marshal(&price)
	if err != nil {
		return err
	}
	store.Set(key, bz)
	return nil
}

// GetPriceHistory returns price data entries within a time range, newest first.
func (k Keeper) GetPriceHistory(ctx sdk.Context, limit uint32) []types.PriceData {
	store := ctx.KVStore(k.storeKey)
	// Use the typed constant so the prefix stays in sync with keys.go.
	// Audit Issue 9: hardcoded byte(2) pointed at prefixCurrentPrice, not
	// prefixPriceHistory (= 3), causing history queries to return the
	// single current-price entry instead of the actual history slice.
	iter := storetypes.KVStoreReversePrefixIterator(store, types.KeyPrefixPriceHistory)
	defer iter.Close()

	var result []types.PriceData
	count := uint32(0)
	for ; iter.Valid() && count < limit; iter.Next() {
		var pd types.PriceData
		if err := k.cdc.Unmarshal(iter.Value(), &pd); err != nil {
			continue
		}
		result = append(result, pd)
		count++
	}
	return result
}

// GetPricesSince returns all price entries since the given timestamp.
// Range bounds use empty feeder so they sort lexicographically below
// any actual entry at that timestamp — the iterator covers the full
// (timestamp, feeder) range across all feeders for each block.
func (k Keeper) GetPricesSince(ctx sdk.Context, sinceTimestamp int64) []types.PriceData {
	store := ctx.KVStore(k.storeKey)
	startKey := types.PriceHistoryKey(sinceTimestamp, "")
	endKey := types.PriceHistoryKey(ctx.BlockTime().Unix()+1, "")

	iter := store.Iterator(startKey, endKey)
	defer iter.Close()

	var result []types.PriceData
	for ; iter.Valid(); iter.Next() {
		var pd types.PriceData
		if err := k.cdc.Unmarshal(iter.Value(), &pd); err != nil {
			continue
		}
		result = append(result, pd)
	}
	return result
}

// CalculateTWAP computes the time-weighted average price over the lookback window.
func (k Keeper) CalculateTWAP(ctx sdk.Context, lookbackSeconds uint64) (math.LegacyDec, math.LegacyDec, error) {
	if lookbackSeconds == 0 {
		return math.LegacyZeroDec(), math.LegacyZeroDec(), fmt.Errorf("lookback cannot be zero")
	}

	now := ctx.BlockTime().Unix()
	since := now - int64(lookbackSeconds)
	prices := k.GetPricesSince(ctx, since)

	if len(prices) == 0 {
		return math.LegacyZeroDec(), math.LegacyZeroDec(), types.ErrPriceNotFound
	}

	totalWeight := math.LegacyZeroDec()
	weightedPriceSum := math.LegacyZeroDec()
	totalVolume := math.LegacyZeroDec()

	for i := 0; i < len(prices); i++ {
		var duration int64
		if i < len(prices)-1 {
			duration = prices[i+1].Timestamp - prices[i].Timestamp
		} else {
			duration = now - prices[i].Timestamp
		}
		if duration <= 0 {
			duration = 1
		}

		weight := math.LegacyNewDec(duration)
		totalWeight = totalWeight.Add(weight)
		weightedPriceSum = weightedPriceSum.Add(prices[i].Price.Mul(weight))
		totalVolume = totalVolume.Add(prices[i].Volume24h)
	}

	if totalWeight.IsZero() {
		return math.LegacyZeroDec(), math.LegacyZeroDec(), types.ErrPriceNotFound
	}

	twapPrice := weightedPriceSum.Quo(totalWeight)
	avgVolume := totalVolume.Quo(math.LegacyNewDec(int64(len(prices))))

	return twapPrice, avgVolume, nil
}

// --- Authority ---

func (k Keeper) GetAuthority() string {
	return k.authority.String()
}
