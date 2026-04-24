// SPDX-License-Identifier: MIT
pragma solidity ^0.8.23;

import "forge-std/Script.sol";
import "../src/SignetPaymaster.sol";

/// @notice Deploy SignetPaymaster to a deterministic address via CREATE2.
///
/// Uses Forge's built-in CREATE2 (new{salt: ...}) so the broadcasting EOA
/// is msg.sender in the constructor and becomes the owner.
///
/// Required env vars:
///   VERIFYING_SIGNER   — Bundler's hot-key address (signs sponsorship approvals)
///   DEPLOY_SALT        — bytes32 salt for CREATE2 (e.g. 0x0000...0001)
///
/// Optional env vars:
///   ENTRY_POINT        — EntryPoint address (default: v0.7 @ 0x0000000071727De22E5E9d8BAf0edAc6f37da032)
///   FACTORY            — SignetAccountFactory address (default: 0xB4c55139db4ad9c481DAA82B249F934CBbB73b91)
///
/// Usage:
///   forge script DeployPaymaster \
///     --rpc-url $RPC_URL \
///     --private-key $DEPLOYER_KEY \
///     --broadcast \
///     --root contracts
contract DeployPaymaster is Script {
    address constant ENTRYPOINT_V07 = 0x0000000071727De22E5E9d8BAf0edAc6f37da032;
    address constant SIGNET_FACTORY = 0xB4c55139db4ad9c481DAA82B249F934CBbB73b91;

    function run() external {
        address entryPoint = vm.envOr("ENTRY_POINT", ENTRYPOINT_V07);
        address verifyingSigner = vm.envAddress("VERIFYING_SIGNER");
        address factory = vm.envOr("FACTORY", SIGNET_FACTORY);
        bytes32 salt = vm.envBytes32("DEPLOY_SALT");

        vm.startBroadcast();

        SignetPaymaster paymaster = new SignetPaymaster{salt: salt}(
            IEntryPoint(entryPoint),
            verifyingSigner,
            ISignetFactory(factory)
        );

        vm.stopBroadcast();

        console.log("Deployed SignetPaymaster at:", address(paymaster));
        console.log("Owner:", msg.sender);
    }
}
