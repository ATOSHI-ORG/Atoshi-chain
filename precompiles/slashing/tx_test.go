// Copyright Tharsis Labs Ltd.(Evmos)
// SPDX-License-Identifier:ENCL-1.0(https://github.com/evmos/evmos/blob/main/LICENSE)

package slashing_test

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/ethereum/go-ethereum/common"
	cmn "github.com/atoshi-chain/atoshi/v20/precompiles/common"
	"github.com/atoshi-chain/atoshi/v20/precompiles/slashing"
	"github.com/atoshi-chain/atoshi/v20/precompiles/testutil"
	utiltx "github.com/atoshi-chain/atoshi/v20/testutil/tx"
)

func (s *PrecompileTestSuite) TestUnjail() {
	method := s.precompile.Methods[slashing.UnjailMethod]
	// unknownAddr is used as both the caller and the validator argument of the
	// "validator not found" case, so the call passes the caller check and
	// reaches the staking lookup.
	unknownAddr := utiltx.GenerateAddress()
	testCases := []struct {
		name        string
		caller      func() common.Address // defaults to keyring account 0
		malleate    func() []interface{}
		postCheck   func()
		gas         uint64
		expError    bool
		errContains string
	}{
		{
			"fail - empty input args",
			nil,
			func() []interface{} {
				return []interface{}{}
			},
			func() {},
			200000,
			true,
			fmt.Sprintf(cmn.ErrInvalidNumberOfArgs, 1, 0),
		},
		{
			"fail - invalid validator address",
			nil,
			func() []interface{} {
				return []interface{}{
					"",
				}
			},
			func() {},
			200000,
			true,
			"invalid validator hex address",
		},
		{
			"fail - invalid validator address (empty address) is rejected as a different caller",
			nil,
			func() []interface{} {
				return []interface{}{
					common.Address{},
				}
			},
			func() {},
			200000,
			true,
			"unjail must be called by the validator",
		},
		{
			"fail - validator not found (called by its own address)",
			func() common.Address { return unknownAddr },
			func() []interface{} {
				return []interface{}{
					unknownAddr,
				}
			},
			func() {},
			200000,
			true,
			"validator does not exist",
		},
		{
			"fail - validator not jailed",
			nil,
			func() []interface{} {
				return []interface{}{
					s.keyring.GetAddr(0),
				}
			},
			func() {},
			200000,
			true,
			"validator not jailed",
		},
		{
			"fail - jailed validator cannot be unjailed by another account",
			func() common.Address { return s.keyring.GetAddr(1) },
			func() []interface{} {
				validator, err := s.network.App.StakingKeeper.GetValidator(s.network.GetContext(), sdk.ValAddress(s.keyring.GetAccAddr(0)))
				s.Require().NoError(err)
				valConsAddr, err := validator.GetConsAddr()
				s.Require().NoError(err)
				s.Require().NoError(s.network.App.SlashingKeeper.Jail(s.network.GetContext(), valConsAddr))

				return []interface{}{
					s.keyring.GetAddr(0),
				}
			},
			func() {},
			200000,
			true,
			"unjail must be called by the validator",
		},
		{
			"success - validator unjailed",
			nil,
			func() []interface{} {
				validator, err := s.network.App.StakingKeeper.GetValidator(s.network.GetContext(), sdk.ValAddress(s.keyring.GetAccAddr(0)))
				s.Require().NoError(err)

				valConsAddr, err := validator.GetConsAddr()
				s.Require().NoError(err)
				err = s.network.App.SlashingKeeper.Jail(
					s.network.GetContext(),
					valConsAddr,
				)
				s.Require().NoError(err)

				validatorAfterJail, err := s.network.App.StakingKeeper.GetValidator(s.network.GetContext(), sdk.ValAddress(s.keyring.GetAddr(0).Bytes()))
				s.Require().NoError(err)
				s.Require().True(validatorAfterJail.IsJailed())

				return []interface{}{
					s.keyring.GetAddr(0),
				}
			},
			func() {
				validatorAfterUnjail, err := s.network.App.StakingKeeper.GetValidator(s.network.GetContext(), sdk.ValAddress(s.keyring.GetAddr(0).Bytes()))
				s.Require().NoError(err)
				s.Require().False(validatorAfterUnjail.IsJailed())
			},
			200000,
			false,
			"",
		},
	}

	for _, tc := range testCases {
		s.Run(tc.name, func() {
			s.SetupTest()

			caller := s.keyring.GetAddr(0)
			if tc.caller != nil {
				caller = tc.caller()
			}
			args := tc.malleate()

			contract, ctx := testutil.NewPrecompileContract(
				s.T(),
				s.network.GetContext(),
				caller,
				s.precompile,
				tc.gas,
			)

			res, err := s.precompile.Unjail(ctx, &method, s.network.GetStateDB(), contract, args)

			if tc.expError {
				s.Require().ErrorContains(err, tc.errContains)
				if tc.caller != nil && tc.errContains == "unjail must be called by the validator" {
					// the validator must still be jailed
					val, err := s.network.App.StakingKeeper.GetValidator(s.network.GetContext(), sdk.ValAddress(s.keyring.GetAddr(0).Bytes()))
					s.Require().NoError(err)
					s.Require().True(val.IsJailed(), "validator must stay jailed after a rejected unjail")
				}
			} else {
				s.Require().NoError(err)
				s.Require().Equal(cmn.TrueValue, res)
				tc.postCheck()
			}
		})
	}
}
