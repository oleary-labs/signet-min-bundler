// SPDX-License-Identifier: MIT
pragma solidity ^0.8.23;

import "forge-std/Script.sol";
import "../src/SignetPaymaster.sol";

/// @notice Deploy SignetPaymaster to a deterministic address via CREATE2.
///
/// Ownership note: Forge routes `new X{salt: ...}` through the deterministic
/// CREATE2 deployer (0x4e59...4956C), so `msg.sender` inside the constructor is
/// that proxy — NOT the broadcasting EOA. SignetPaymaster therefore takes the
/// owner as an explicit constructor argument. Deploying a version that relies on
/// `Ownable(msg.sender)` burns ownership permanently (this happened on Sepolia:
/// 0x50Cc2c0a... and 0xA320eAC9... are both owned by the deployer proxy).
///
/// Required env vars:
///   VERIFYING_SIGNER   — Bundler's hot-key address (signs sponsorship approvals)
///   PAYMASTER_OWNER    — Address that can call setFactory / withdraw the deposit
///   FACTORY            — SignetFactory proxy address
///   DEPLOY_SALT        — bytes32 salt for CREATE2 (e.g. 0x0000...0001)
///
/// Optional env vars:
///   ENTRY_POINT        — EntryPoint address (default: v0.7 @ 0x0000000071727De22E5E9d8BAf0edAc6f37da032)
///
/// Usage:
///   VERIFYING_SIGNER=0x... PAYMASTER_OWNER=0x... FACTORY=0x... DEPLOY_SALT=0x...01 \
///   forge script DeployPaymaster \
///     --rpc-url $RPC_URL \
///     --private-key $DEPLOYER_KEY \
///     --broadcast \
///     --root contracts
contract DeployPaymaster is Script {
    address constant ENTRYPOINT_V07 = 0x0000000071727De22E5E9d8BAf0edAc6f37da032;

    function run() external {
        address entryPoint = vm.envOr("ENTRY_POINT", ENTRYPOINT_V07);
        address verifyingSigner = vm.envAddress("VERIFYING_SIGNER");
        address owner = vm.envAddress("PAYMASTER_OWNER");
        address factory = vm.envAddress("FACTORY");
        bytes32 salt = vm.envBytes32("DEPLOY_SALT");

        // The factory must actually implement isGroup — the paymaster calls it on
        // every sponsored op, so a wrong address bricks sponsorship silently.
        require(factory.code.length > 0, "FACTORY has no code on this chain");
        ISignetFactory(factory).isGroup(address(1));

        console.log("entryPoint      :", entryPoint);
        console.log("verifyingSigner :", verifyingSigner);
        console.log("owner           :", owner);
        console.log("factory         :", factory);

        vm.startBroadcast();

        SignetPaymaster paymaster = new SignetPaymaster{salt: salt}(
            IEntryPoint(entryPoint),
            verifyingSigner,
            ISignetFactory(factory),
            owner
        );

        vm.stopBroadcast();

        require(paymaster.owner() == owner, "ownership not transferred");

        console.log("Deployed SignetPaymaster at:", address(paymaster));
        console.log(string.concat("DEPLOY:paymaster=", vm.toString(address(paymaster))));
    }
}
