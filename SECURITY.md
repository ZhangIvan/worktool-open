# Security boundary
This is a local synthetic prototype, not a production-ready automation platform. Do not use real personal data, enterprise accounts, private keys or production credentials. The Android executor is not implemented or validated here.

Never report vulnerabilities with live credentials or personal data in a public issue. Until a maintainer establishes a private reporting channel, share only a sanitized reproducer and arrange a private disclosure path. Do not claim the repository has a monitored security mailbox.

Authentication, authorization, durable state, export approval and replay prevention require the executable tests recorded in the test report. Passing schema/model tests alone does not prove them. Unknown side effects must not be retried automatically. Telemetry and outbound callbacks are not enabled. No default credentials are checked in; bootstrap uses generated temporary synthetic identities.
