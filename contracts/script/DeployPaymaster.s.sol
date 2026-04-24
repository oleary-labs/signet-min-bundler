// SPDX-License-Identifier: MIT
pragma solidity ^0.8.23;

import "forge-std/Script.sol";
import "../src/SignetPaymaster.sol";

/// @notice Deploy SignetPaymaster to a deterministic address via CREATE2.
///
/// Uses the canonical Arachnid CREATE2 deployer (0x4e59b44847b379578588920cA78FbF26c0B4956C)
/// which is available on every major chain. The same salt + constructor args
/// produce the same address on every chain, regardless of the deployer EOA.
///
/// Required env vars:
///   VERIFYING_SIGNER   — Bundler's hot-key address (signs sponsorship approvals)
///   DEPLOY_SALT        — bytes32 salt for CREATE2 (e.g. 0x0000...0001)
///
/// Optional env vars:
///   ENTRY_POINT        — EntryPoint address (default: v0.7 @ 0x0000000071727De22E5E9d8BAf0edAc6f37da032)
///   FACTORY            — SignetAccountFactory address (default: 0x56d42B4710aACCed8ab50D157076ce45a1cb0c91)
///
/// Usage:
///   forge script script/DeployPaymaster.s.sol \
///     --rpc-url $RPC_URL \
///     --private-key $DEPLOYER_KEY \
///     --broadcast \
///     --root contracts
///
/// To predict the address without deploying, omit --broadcast.
contract DeployPaymaster is Script {
    address constant CREATE2_DEPLOYER = 0x4e59b44847b379578588920cA78FbF26c0B4956C;
    address constant ENTRYPOINT_V07 = 0x0000000071727De22E5E9d8BAf0edAc6f37da032;
    address constant SIGNET_FACTORY = 0x56d42B4710aACCed8ab50D157076ce45a1cb0c91;

    function run() external {
        address entryPoint = vm.envOr("ENTRY_POINT", ENTRYPOINT_V07);
        address verifyingSigner = vm.envAddress("VERIFYING_SIGNER");
        address factory = vm.envOr("FACTORY", SIGNET_FACTORY);
        bytes32 salt = vm.envBytes32("DEPLOY_SALT");

        bytes memory creationCode = abi.encodePacked(
            type(SignetPaymaster).creationCode,
            abi.encode(IEntryPoint(entryPoint), verifyingSigner, ISignetFactory(factory))
        );

        address predicted = _predictCreate2(salt, creationCode);
        console.log("Predicted address:", predicted);

        if (predicted.code.length > 0) {
            console.log("Already deployed, skipping.");
            return;
        }

        // The Arachnid proxy expects: salt (32 bytes) ++ creation code.
        bytes memory payload = abi.encodePacked(salt, creationCode);

        vm.startBroadcast();
        (bool success,) = CREATE2_DEPLOYER.call(payload);
        require(success, "CREATE2 deploy failed");
        vm.stopBroadcast();

        require(predicted.code.length > 0, "deployment did not produce code");
        console.log("Deployed SignetPaymaster at:", predicted);
    }

    function _predictCreate2(bytes32 salt, bytes memory creationCode) internal pure returns (address) {
        return address(uint160(uint256(keccak256(abi.encodePacked(
            bytes1(0xff),
            CREATE2_DEPLOYER,
            salt,
            keccak256(creationCode)
        )))));
    }
}
