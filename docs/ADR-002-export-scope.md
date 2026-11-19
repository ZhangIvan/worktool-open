# ADR-002 Export approval scope binding (pre-release security correction)
The frozen design requires approval bound to the requester, device and exact scope. Integration review found the initial implementation only checked tenant/exportId/approvalId/scopeDigest/expiry. That would allow a different operator or device in the same tenant to reuse the approval.

Correction before acceptance: request scope is `{deviceId, referenceIds}`; digest includes both. Export request must name an active device in the authenticated tenant. Both command admission and dispatch recheck `requester == command.actor`, `scope.deviceId == command.deviceId`, approved state, exact digest/approvalId and expiry. UI explicitly selects and displays the bound device; tests reject another operator and a second device. No new capability is enabled. This is implementation of the existing security requirement, not a claim about the original APK.

Status: implemented only after final regression evidence in the test report. No public release preceded this correction.
