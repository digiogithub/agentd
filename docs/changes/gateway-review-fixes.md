# Gateway code-review fixes (AGED-US-0016 / 0017 / 0018 / 0021)

Builds on [[AGED-US-0016-serve-skeleton]], [[AGED-US-0017-rest-endpoints]], [[AGED-US-0018-sse-streams]],
[[AGED-US-0021-device-tokens]]; hardening story: [[AGED-US-0024-hardening]]. Design: [[design/api/security-remote]],
[[design/api/http-api]].

Each item lists the change (files under `internal/httpapi` unless noted) and its regression test
(`hardening_test.go` unless noted).

1. **Act timeout / retry** - `pool.go`: `ActTimeout` = 40 s (daemon act budget: 10 s input lock + 3 s key steps),
   `Server.WithDaemonWrite`. Lost answers after dispatch are `502`/`504 outcome_unknown`, `retryable:false`,
   act adds the fresh view + ETag (`sessions.go actError`). Tests: `TestActNoRetryAndOutcomeUnknown`,
   `TestActDeadlineAboveDaemonAndHangIs504Unknown`.
2. **Idempotency** - `idempotency.go`: phases in-flight/done/unknown; forget only pre-dispatch errors or definite
   refusals (`definiteFailure`, dispatch tracker in the context); panics finalised by `defer`; `If-Match` in the hash.
   Tests: `TestIdempotencyOutcomeUnknownBlocksReexecution`, `...ForgetsOnlyPreDispatchErrors`,
   `TestIdempotentPanicFinalizesEntry`, `TestIdempotencyIncludesIfMatch`.
3. **Pool retry** - writes use a fresh connection and never retry; reads may retry once on a stale pooled one.
   Test: `TestWritesNeverRetryAndUseFreshConnections` (+ existing `TestPoolReuseAndStaleRetry`).
4. **Health / concurrency** - `/v1/health` cached 1 s (`Server.ping`); semaphore of 32 daemon calls, 250 ms wait then
   `503 busy`. Tests: `TestHealthCachesPing`, `TestInflightCapAnswersBusy`.
5. **Host / Origin** - `guard.go`: allow-list (loopback names, bind IP / all interface IPs, hostname, `tls_hosts`,
   `allowed_hosts`) => `421 misdirected_request` before auth/routing; `Origin` must be in `cors_origins` else 403,
   `null` never. Tests: `TestHostAllowListDefaultsAnd421BeforeAuth`, `TestOriginPolicy`, `cmd/agentd/hardening_test.go`.
6. **Server limits, SSE caps, rate limits** - `Server.httpServer` (ReadHeaderTimeout 10 s, IdleTimeout 120 s,
   MaxHeaderBytes 16 KiB); `acquireStream` (8/device, 64 total, `429 too_many_streams`); token buckets per device
   (20/s burst 40, act 5/s) and 10 failed auths/min per address (`429`). Tests: `TestHTTPServerLimits`,
   `TestStreamCaps`, `TestStreamCapsUnit`, `TestRateLimitPerDeviceAndAct`, `TestFailedAuthRateLimitPerAddress`.
7. **`args` needs admin** - `sessions.go handleRunSession`; docs + openapi. Test: `TestRunArgsRequireAdmin`,
   `TestRESTRunHarnessPolicy` (updated).
8. **`?mailbox=` needs admin** - `mailbox.go mailboxParam`. Test: `TestMailboxPeekRequiresAdmin`.
9. **Idempotency quota / LRU** - two intrusive LRU lists (global 4096, per device 256), O(1) eviction, in-flight
   entries never evicted (`idemFull` => 503 busy). Test: `TestIdempotencyPerDeviceQuotaAndLRU`.
10. Merged into 2.
11. **Prompt size** - `PromptMaxChars` = 16000 runes enforced in `handleAct` (413), advertised in `/v1/info`;
    16000 runes <= 64000 bytes < `proto.MaxActText`. Test: `TestActPromptMaxChars`.
12. **View cache** - `viewCache` LRU (128 entries, 8 MiB budget); `tail` fetched in buckets 50/100/200/500/5000 and
    trimmed per stream (`quantTail`, `trimView`). Tests: `TestQuantTailAndTrimView`, `TestViewCacheLRUAndByteBudget`.
13. **Session instance in ETag / event ids** - `e<epoch>-i<instance>-r<rev>` (instance = session start time, base 36);
    a re-created session gets a full `view`, ETags of another incarnation never match; cache cleared on resync/removal.
    Test: `TestViewStreamResumeAcrossSessionRecreation`, `TestIfMatchStarAndListOnAct`.
14. **Hub resync floor** - `hub.floor`: ids at or below the last daemon-side resync get `resync`. Test:
    `TestHubResyncFloorAndNonAutostartingDialer`.
15. **Non-autostarting hub dialer** - `Server.HubDial`; `cmd/agentd/serve.go` passes a plain `client.Dial`;
    `[api] autostart_daemon` (default true) controls request-time autostart. Same test + `TestAPIConfigHardeningDefaults`.
16. **If-Match / If-None-Match (RFC 9110)** - `splitETagList`, `etagMatches`, `resolveIfMatch` (`*`, lists, weak =
    no match for If-Match; weak compare for If-None-Match). Tests: `TestETagListsAndWildcards`, `TestIfMatchStarAndListOnAct`.
17. **HEAD** - 405 + `Allow: GET` on events, view stream and mailbox routes (`wrapRoute`). Test: `TestHeadRejectedOnStreamAndMailboxRoutes`.
18. **Error envelope** - `FromDaemonError`: internal/unknown daemon errors => `500 internal`, "internal error", cause logged.
    Test: `TestInternalDaemonErrorsAreMasked`, `TestErrorEnvelopeMapping` (updated).
19. **Headers / media type** - `nosniff`, `no-referrer`, JSON `no-store`; `415` for a POST body that is not
    `application/json`. Test: `TestSecurityHeadersAndContentType`.
20. **OpenAPI drift** - act example `label`, `epoch` added next to `revision`, key `esc` (was `Escape`), run example
    marks `args` admin, new responses 415/421/429/502, ETag format; YAML parses; `TestRoutesMatchOpenAPI` passes.
21. **apitoken Guard** - `Server.Guard` runs inside request-id/access-log/recover (its 400 is logged); query rejected
    only for token-shaped values (`agd_[0-9A-Z]{10}_[A-Za-z0-9_-]{43}`) or token-ish parameter names. Tests (apitoken):
    `TestQueryTokenShapeNotPrefix`, `TestGuardRejectionIsLoggedWithRequestID`.
22. **Async last_seen** - `Store.persistLastSeen` queues (coalesced per device, max 256) to one worker;
    `FlushTouches` for tests/shutdown. Test: `TestLastSeenWriteIsAsyncAndCoalesced` (Verify returns while the flock is held).
- **`[api] pairing`** (default true) mounts `/v1/pair`; test `TestServePairingToggleHostAndAudit`.
- **Nil `Dial` panic** at `cmd/agentd/serve.go` (`o.Dial()` in a test that gave no dialer): `runServe` now returns
  "serve: no daemon dialer configured" up front and tolerates a nil logger; `TestRunServeBadFiles` supplies a dialer;
  test `TestRunServeNilDialIsAnError`.

Verification: `gofmt -l` (only the unrelated `internal/testutil/testutil.go` of another agent is listed), `go vet ./...`,
`go test -race -count=1 ./internal/httpapi ./internal/apitoken ./internal/tlsutil ./internal/config ./cmd/...` all pass.
