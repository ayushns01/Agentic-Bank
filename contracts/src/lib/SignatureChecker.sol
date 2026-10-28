// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

interface IERC1271 {
    function isValidSignature(bytes32 hash, bytes calldata signature) external view returns (bytes4);
}

/// @notice ECDSA recovery (65-byte signatures, low-s enforced) plus EIP-1271
///         verification for contract signers such as a Safe.
library SignatureChecker {
    bytes4 internal constant ERC1271_MAGIC = 0x1626ba7e;
    uint256 private constant HALF_N = 0x7FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF5D576E7357A4501DDFE92F46681B20A0;

    /// @return signer the recovered address, or address(0) if invalid.
    function recover(bytes32 digest, bytes memory sig) internal pure returns (address signer) {
        if (sig.length != 65) return address(0);
        bytes32 r;
        bytes32 s;
        uint8 v;
        assembly {
            r := mload(add(sig, 0x20))
            s := mload(add(sig, 0x40))
            v := byte(0, mload(add(sig, 0x60)))
        }
        if (uint256(s) > HALF_N) return address(0);
        if (v != 27 && v != 28) return address(0);
        signer = ecrecover(digest, v, r, s);
    }

    /// @notice True if `sig` is a valid signature of `digest` by `signer`,
    ///         either an EOA (ECDSA) or a contract (EIP-1271).
    function isValid(address signer, bytes32 digest, bytes memory sig) internal view returns (bool) {
        if (signer == address(0)) return false;
        if (signer.code.length == 0) return recover(digest, sig) == signer;
        (bool ok, bytes memory ret) =
            signer.staticcall(abi.encodeCall(IERC1271.isValidSignature, (digest, sig)));
        return ok && ret.length >= 32 && abi.decode(ret, (bytes4)) == ERC1271_MAGIC;
    }
}
