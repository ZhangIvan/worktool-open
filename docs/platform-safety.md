# Platform safety and execution boundaries

This note defines acceptance boundaries for the own-fixture accessibility candidate. **Android status: NOT_BUILT / NOT_DEVICE_TESTED.** The controls described below are release requirements or source-level safeguards, not proof of production readiness, platform approval or freedom from account restrictions.

## Name each execution path

Publishing a self-owned HTTP API or implementing an Apifox-compatible request shape does not make that API a Tencent official API. Each capability must identify its actual path, exact target, authorization requirements, supported version and verification evidence. Unknown paths stay unavailable rather than inheriting an “official” label.

- `official_api`: reserved for an individually reviewed official provider endpoint and documented supported use case. Verify the actual tenant/account entitlement, permissions, quotas and current provider terms before enabling it. No Tencent official execution adapter is claimed in this candidate.
- `accessibility`: UI actions through an explicitly enabled Android service. Only secure debug/release can explicitly select `fixture.ui.submit_text`, negotiated as protocol `1.1`; lab has no service/driver path. This capability is restricted to its own installed package and `FixtureActivity`. It does not authorize another app, account or conversation.
- `synthetic`: controlled local examples with no real recipient. Current `message.send_text` and developer type `203` write a synthetic inbox; `file.export` hashes an approved bundled synthetic note. API compatibility does not change these effects.

Prefer an official API when the specific business scenario is supported and authorized. Where it is not, record the gap; do not silently switch to a UI driver. Any future real WeCom driver needs a separate scope, review and acceptance gate. Without explicit authorization for the concrete account, conversation/recipient and action, real sending remains prohibited. Finding a target in a dry run is not permission to send.

## Local consent and operational controls

The source candidate separates system service enablement, an active own-app foreground lease, a local action switch, server device/operator scope and one-task confirmation. A successful connection or an administrator's role cannot replace these gates. Root-package mismatch must stop inspection before traversing external nodes; arbitrary selectors, scripts and coordinates are not supported. The new service is declared with protected system binding and narrow configuration, following the [Android AccessibilityService model](https://developer.android.com/guide/topics/ui/accessibility/service); declaration alone does not prove safe operation.

Before extending any capability beyond the fixture, its acceptance record must cover:

1. A visible risk notice and affirmative consent describing the exact target, data accessed, effect and any sharing. Device-owner authorization alone does not establish permission to read or message other people's accounts or data.
2. Least-privilege account and target allowlists, bounded content, revocable authority, and dry-run discovery separated from effectful execution.
3. Per-target rate limits, bounded queues and expiry, duplicate protection, backpressure/circuit breaking, and a clear manual stop. Provider rate limits and business consent must both be respected; no “safe frequency” guarantees avoidance of restrictions.
4. Minimal, access-controlled audit evidence linking actor, intent, authorization and observed result without recording secrets or unnecessary content. A click/ACK alone is not success; cancellation or process death may leave `execution_unknown`, with no automatic resend.

These are verification gates, not a claim that every production control is implemented. Do not add stealth, anti-detection, identity spoofing, account rotation, challenge bypass or hidden permission enabling. Throttling, consent and audits reduce some risks but cannot guarantee continued account access, immunity from platform enforcement or absence of harm.

## Distribution policy is a separate gate

Google Play restricts AccessibilityService usage and the `isAccessibilityTool` designation. Non-accessibility-tool apps need prominent in-app disclosure and affirmative consent, and applicable Play declaration/review. Its automation guidance requires a narrow, clearly understood purpose and distinguishes deterministic human-defined rules from prohibited autonomous initiation/planning/execution. This candidate sets `isAccessibilityTool=false`; it has not obtained Play approval, and its disclosure flow has not been validated against distribution requirements. See [Google Play AccessibilityService policy](https://support.google.com/googleplay/android-developer/answer/10964491) (checked 2026-11-02).

Google Play policy is a distribution constraint, not a legal determination, Tencent endorsement or authorization to automate WeCom. Passing an Android build, obtaining a user's system permission, self-hosting or distributing outside Play does not establish those separate permissions. Sideloading is not an exemption from applicable privacy, contractual or platform obligations. Resolve those questions for the actual deployment before enabling real-account behavior; this candidate makes no compliance certification.
