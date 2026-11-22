package v20_5

import (
	"bytes"
	"context"
	"testing"
	"time"

	"cosmossdk.io/log"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/authz"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/stretchr/testify/require"

	vestingtypes "github.com/atoshi-chain/atoshi/v20/x/vesting/types"
)

type storedGrant struct {
	granter, grantee sdk.AccAddress
	grant            authz.Grant
}

// fakeAuthz keeps grants in insertion order, like a store iterated by key.
type fakeAuthz struct {
	grants  []storedGrant
	failFor sdk.AccAddress
}

func (f *fakeAuthz) IterateGrants(_ context.Context, h func(granter, grantee sdk.AccAddress, g authz.Grant) bool) {
	for _, g := range f.grants {
		if h(g.granter, g.grantee, g.grant) {
			return
		}
	}
}

func (f *fakeAuthz) DeleteGrant(_ context.Context, grantee, granter sdk.AccAddress, msgType string) error {
	if f.failFor != nil && bytes.Equal(grantee, f.failFor) {
		return authz.ErrNoAuthorizationFound
	}
	for i, g := range f.grants {
		a, _ := g.grant.GetAuthorization()
		if bytes.Equal(g.granter, granter) && bytes.Equal(g.grantee, grantee) && a.MsgTypeURL() == msgType {
			f.grants = append(f.grants[:i], f.grants[i+1:]...)
			return nil
		}
	}
	return authz.ErrNoAuthorizationFound
}

func (f *fakeAuthz) add(t *testing.T, granter, grantee sdk.AccAddress, a authz.Authorization) {
	exp := time.Now().Add(time.Hour)
	g, err := authz.NewGrant(time.Now(), a, &exp)
	require.NoError(t, err)
	f.grants = append(f.grants, storedGrant{granter, grantee, g})
}

func (f *fakeAuthz) types() []string {
	var out []string
	for _, g := range f.grants {
		a, _ := g.grant.GetAuthorization()
		out = append(out, a.MsgTypeURL())
	}
	return out
}

func addr(b byte) sdk.AccAddress { return sdk.AccAddress(bytes.Repeat([]byte{b}, 20)) }

func TestRevokeVestingGrants(t *testing.T) {
	ctx := sdk.Context{}.WithLogger(log.NewNopLogger())
	fund := sdk.MsgTypeURL(&vestingtypes.MsgFundVestingAccount{})
	clawback := sdk.MsgTypeURL(&vestingtypes.MsgClawback{})
	updateFunder := sdk.MsgTypeURL(&vestingtypes.MsgUpdateVestingFunder{})
	send := sdk.MsgTypeURL(&banktypes.MsgSend{})

	t.Run("revokes every vesting grant and nothing else", func(t *testing.T) {
		f := &fakeAuthz{}
		f.add(t, addr(1), addr(9), authz.NewGenericAuthorization(fund))
		f.add(t, addr(1), addr(9), authz.NewGenericAuthorization(send))
		f.add(t, addr(2), addr(8), authz.NewGenericAuthorization(clawback))
		f.add(t, addr(3), addr(7), authz.NewGenericAuthorization(updateFunder))

		revokeVestingGrants(ctx, f)

		require.Equal(t, []string{send}, f.types())
	})

	t.Run("a failed deletion is skipped, not fatal", func(t *testing.T) {
		f := &fakeAuthz{failFor: addr(9)}
		f.add(t, addr(1), addr(9), authz.NewGenericAuthorization(fund))
		f.add(t, addr(2), addr(8), authz.NewGenericAuthorization(fund))

		revokeVestingGrants(ctx, f)

		require.Len(t, f.grants, 1)
		require.Equal(t, addr(9), f.grants[0].grantee)
	})
}
