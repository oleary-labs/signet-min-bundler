// SPDX-License-Identifier: MIT
pragma solidity ^0.8.23;

import "account-abstraction/core/BasePaymaster.sol";
import "account-abstraction/core/UserOperationLib.sol";
import "account-abstraction/core/Helpers.sol";
import "@openzeppelin/contracts/utils/cryptography/ECDSA.sol";
import "@openzeppelin/contracts/utils/cryptography/MessageHashUtils.sol";

/// @notice Interface for checking whether an address is a group deployed
///         by the SignetAccountFactory.
interface ISignetFactory {
    function isGroup(address group) external view returns (bool);
}

/// @title SignetPaymaster
/// @notice Paymaster for Signet wallets. Verifies an off-chain signer's
///         approval (like VerifyingPaymaster) and enforces that the call
///         target is the factory, a factory-deployed group, or a self-call.
///
/// @dev    Based on VerifyingPaymaster but adds on-chain target validation.
///         Cannot subclass VerifyingPaymaster directly because its
///         _validatePaymasterUserOp is not virtual.
contract SignetPaymaster is BasePaymaster {
    using UserOperationLib for PackedUserOperation;

    address public immutable verifyingSigner;
    ISignetFactory public factory;

    /// @dev execute(address,uint256,bytes) selector = 0xb61d27f6
    bytes4 private constant EXECUTE_SELECTOR = 0xb61d27f6;

    uint256 private constant VALID_TIMESTAMP_OFFSET = PAYMASTER_DATA_OFFSET;
    uint256 private constant SIGNATURE_OFFSET = VALID_TIMESTAMP_OFFSET + 64;

    event FactoryUpdated(address indexed oldFactory, address indexed newFactory);

    constructor(
        IEntryPoint _entryPoint,
        address _verifyingSigner,
        ISignetFactory _factory
    ) BasePaymaster(_entryPoint) {
        verifyingSigner = _verifyingSigner;
        factory = _factory;
    }

    /// @notice Update the factory address. Only callable by the owner.
    function setFactory(ISignetFactory _factory) external onlyOwner {
        emit FactoryUpdated(address(factory), address(_factory));
        factory = _factory;
    }

    // ── Hash computation (same as VerifyingPaymaster) ───────────────────

    function getHash(
        PackedUserOperation calldata userOp,
        uint48 validUntil,
        uint48 validAfter
    ) public view returns (bytes32) {
        address sender = userOp.getSender();
        return keccak256(
            abi.encode(
                sender,
                userOp.nonce,
                keccak256(userOp.initCode),
                keccak256(userOp.callData),
                userOp.accountGasLimits,
                uint256(bytes32(userOp.paymasterAndData[PAYMASTER_VALIDATION_GAS_OFFSET:PAYMASTER_DATA_OFFSET])),
                userOp.preVerificationGas,
                userOp.gasFees,
                block.chainid,
                address(this),
                validUntil,
                validAfter
            )
        );
    }

    function parsePaymasterAndData(
        bytes calldata paymasterAndData
    ) public pure returns (uint48 validUntil, uint48 validAfter, bytes calldata signature) {
        (validUntil, validAfter) = abi.decode(paymasterAndData[VALID_TIMESTAMP_OFFSET:], (uint48, uint48));
        signature = paymasterAndData[SIGNATURE_OFFSET:];
    }

    // ── Off-chain sponsorship check ───────────────────────────────────

    /// @notice Called off-chain by the bundler (via eth_call) before signing
    ///         paymaster data. Returns true if this op should be sponsored.
    ///         Allows app-specific policy without full EVM simulation.
    /// @dev    TODO: Add policy checks. Candidates:
    ///         - For group calls: verify sender is owner/operator of the group
    ///         - For factory calls: whitelist or rate-limit account creation
    ///         Currently returns true for any op that passes target validation.
    function shouldSponsor(PackedUserOperation calldata userOp) external view returns (bool) {
        return _isAllowedTarget(userOp.getSender(), userOp.callData);
    }

    // ── Validation ──────────────────────────────────────────────────────

    function _validatePaymasterUserOp(
        PackedUserOperation calldata userOp,
        bytes32, /* userOpHash */
        uint256 /* requiredPreFund */
    ) internal view override returns (bytes memory context, uint256 validationData) {
        (uint48 validUntil, uint48 validAfter, bytes calldata signature) =
            parsePaymasterAndData(userOp.paymasterAndData);

        require(
            signature.length == 64 || signature.length == 65,
            "SignetPaymaster: invalid signature length"
        );

        // 1. Verify off-chain signer's signature.
        bytes32 hash = MessageHashUtils.toEthSignedMessageHash(getHash(userOp, validUntil, validAfter));
        bool sigFailed = verifyingSigner != ECDSA.recover(hash, signature);

        if (sigFailed) {
            return ("", _packValidationData(true, validUntil, validAfter));
        }

        // 2. Validate call target.
        if (!_isAllowedTarget(userOp.getSender(), userOp.callData)) {
            return ("", _packValidationData(true, validUntil, validAfter));
        }

        return ("", _packValidationData(false, validUntil, validAfter));
    }

    /// @dev Checks whether the call target is allowed:
    ///      - Self-calls (target == sender): always (key rotation, validator mgmt)
    ///      - Calls to the factory: group creation
    ///      - Calls to a factory-deployed group: normal group operations
    function _isAllowedTarget(address sender, bytes calldata callData) internal view returns (bool) {
        if (callData.length < 36) {
            return false;
        }
        if (bytes4(callData[:4]) != EXECUTE_SELECTOR) {
            return false;
        }

        address target = address(uint160(uint256(bytes32(callData[4:36]))));

        if (target == sender) {
            return true;
        }
        if (target == address(factory)) {
            return true;
        }

        return factory.isGroup(target);
    }
}
