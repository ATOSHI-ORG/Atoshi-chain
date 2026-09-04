// SPDX-License-Identifier: LGPL-3.0-only
pragma solidity >=0.8.17;

/// @dev The BridgeAdapter contract's address.
address constant BRIDGEADAPTER_PRECOMPILE_ADDRESS = 0x0000000000000000000000000000000000000808;

/// @dev The BridgeAdapter contract's instance.
IBridgeAdapter constant BRIDGEADAPTER_CONTRACT = IBridgeAdapter(BRIDGEADAPTER_PRECOMPILE_ADDRESS);

/// @author Atoshi
/// @title BridgeAdapter Precompile
/// @dev Lets an EVM wallet bridge ATOS out to Ethereum.
///
/// Without this, bridging out is unreachable from MetaMask and every other EVM
/// wallet: MsgBridgeOut is a Cosmos message, and those wallets only sign EVM
/// transactions. Staking works from a wallet for exactly this reason -- it has
/// a precompile of its own.
interface IBridgeAdapter {
    /// @dev Emitted when ATOS is locked and the matching ERC20 is requested on
    /// Ethereum.
    /// @param sender The Atoshi account whose ATOS was locked.
    /// @param recipient The Hyperlane-encoded Ethereum address being credited.
    /// @param amount ATOS locked, in the chain's base unit.
    /// @param erc20Amount ERC20 requested on Ethereum, in ERC20 units.
    /// @param messageId The Hyperlane message id, for tracking delivery.
    event BridgeOut(
        address indexed sender,
        bytes32 indexed recipient,
        uint256 amount,
        uint256 erc20Amount,
        bytes32 messageId
    );

    /// @dev Locks ATOS and dispatches a Hyperlane message so the matching ERC20
    /// is released on Ethereum.
    ///
    /// The ATOS moves into the migration pool rather than being burned: neither
    /// ATOS nor the ERC20 can be minted, so the bridge is lock-and-unlock on
    /// both sides and the pool is the counterparty for inbound transfers.
    ///
    /// The caller pays. There is no sender argument and no delegated form: the
    /// sender is always the account that signed this transaction, and a contract
    /// cannot call this on a user's behalf. Any other arrangement would let a
    /// contract a user merely interacts with move their ATOS across a chain
    /// boundary, which cannot be undone.
    ///
    /// Reverts if the amount is not an exact multiple of the ATOS/ERC20 peg. A
    /// remainder could not be represented on the Ethereum side and would be
    /// silently confiscated.
    ///
    /// @param recipient The Ethereum address to credit, left-padded to 32 bytes.
    /// @param amount ATOS to bridge out, in the chain's base unit.
    /// @param maxFeeAmount Upper bound on the interchain gas payment, in the
    /// chain's base unit. Pass 0 to let the chain charge whatever the message
    /// costs; pass a bound to fail rather than overpay.
    /// @return messageId The Hyperlane message id.
    /// @return erc20Amount The ERC20 amount requested on Ethereum.
    function bridgeOut(
        bytes32 recipient,
        uint256 amount,
        uint256 maxFeeAmount
    ) external returns (bytes32 messageId, uint256 erc20Amount);
}
