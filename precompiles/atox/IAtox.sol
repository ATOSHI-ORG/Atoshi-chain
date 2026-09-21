// SPDX-License-Identifier: LGPL-3.0-only
pragma solidity >=0.8.18;

address constant ATOX_PRECOMPILE_ADDRESS = 0x0000000000000000000000000000000000000809;

IAtox constant ATOX_CONTRACT = IAtox(ATOX_PRECOMPILE_ADDRESS);

/**
 * @title IAtox
 * @dev Converts mined ATOX into ATOS from an EVM wallet.
 *
 * ATOX accrues a claim on the exchange pool as tier releases fund it. Claiming
 * pays the ATOS and burns the matching ATOX, so a holder's ATOX shrinks as their
 * ATOS arrives and the two together stay constant.
 *
 * Claiming used to happen by itself: an EndBlocker swept every account after
 * each release. That sweep was removed because it scales with the number of
 * holders, not with the number who want their ATOS -- at a million accounts one
 * pass took the better part of a day of unmetered EndBlocker work, and most of
 * it was spent on accounts with nothing to collect. Collection is now something
 * the holder asks for, which is what this precompile is for: MsgClaimAtos is a
 * Cosmos message and an EVM wallet cannot sign one.
 */
interface IAtox {
    /// @dev Emitted when ATOX is converted into ATOS.
    /// @param claimer The account that claimed.
    /// @param atosPaid ATOS paid out, in aatos (18 decimals).
    /// @param atoxBurned ATOX destroyed for it, in aatox. Equal to atosPaid.
    event ClaimAtos(address indexed claimer, uint256 atosPaid, uint256 atoxBurned);

    /**
     * @dev Converts the caller's outstanding ATOX claim into ATOS.
     *
     * Reverts when there is nothing to claim rather than succeeding with zero,
     * so a wallet that batches this after another call cannot silently pay gas
     * for a no-op. Check claimable() first.
     *
     * @return atosPaid ATOS credited to the caller, in aatos.
     */
    function claim() external returns (uint256 atosPaid);

    /**
     * @dev What claim() would pay right now, in aatos.
     *
     * Includes both the already-settled amount and what settling at the current
     * index would add, so it is the number to show a user and the number to test
     * against zero before offering the button.
     */
    function claimable(address account) external view returns (uint256);

    /**
     * @dev The ATOX a settlement would destroy right now, in aatox.
     *
     * Equal to the unsettled part of claimable(). A wallet needs it to size a
     * "send max" of ATOX: any transfer settles the sender first, so the spendable
     * balance is balanceOf(ATOX) - burnOnSettle(account).
     */
    function burnOnSettle(address account) external view returns (uint256);

    /**
     * @dev Cumulative liao owed per aatox held, scaled by 1e18.
     *
     * Runs from 0 to 1e18 over the life of the unlock, since the ATOX cap and
     * the exchange pool are the same size. Multiply by an ATOX balance and
     * divide by 1e18 to get that holder's total entitlement to date.
     */
    function globalIndex() external view returns (uint256);
}
