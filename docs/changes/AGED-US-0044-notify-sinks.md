# AGED-US-0044: daemon notification sinks (ntfy, webhook)

Plan: [[plans/agentd/dashboard_plan]] (Contract > Notifications). Note: the KB tools were unavailable when this
change was made, so this file is the record (not a `kb_add_document` entry).

## What changed

- New stdlib-only package `internal/notify`: ntfy and webhook sinks (HMAC `X-Agentd-Signature`), bounded queue (256,
  drop oldest), one worker, 10 s timeout, 3 retries on 429/5xx/network errors, no redirects, debounce per
  session+reason, payload builder, `CleanText` sanitizer (160 runes). Never logs URLs, secrets or bodies.
- `internal/httpapi/notify.go` is now a thin adapter: `mapEvent` (hub event -> reason) and the preview lookup stay,
  delivery moved. `NotifyConfig`, `Sign`, `SignatureHeader` keep their API; gateway behaviour is unchanged.
- New daemon table `[notify]` (`config.Config.Notify`, same `NotifyConfig` shape and secret rules as `[api.notify]`),
  with reasons `question`, `waiting`, `blocked`, `turn_done`, `exited`; default
  `on = ["question","waiting","blocked","exited"]`; off by default.
- New `internal/daemon/notify.go`: in-process bus subscriber started in `Serve` (`startNotify`) and stopped first in
  `Shutdown` (`stopNotify`, before the bus closes). Mapping of `attention.changed`, `session.exited`, `mail.queued`
  (`kind == "question"` to `human:*`) and `task.changed` (entry newly `blocked`) onto `notify.Event`. `kind` and
  `changed[]` are read defensively from the event data (other stories add those producers).

## Files and symbols

- `internal/notify/notify.go`, `notify_test.go` (`Notifier`, `Config`, `Event`, `Payload`, `Offer`, `Close`, `Sign`).
- `internal/httpapi/notify.go`, `notify_test.go` (adapter; queue/overflow/clean-text tests moved to `internal/notify`).
- `internal/config/api.go` (`validateAs`, `ResolveSecretsFor`, `applyDefaultsOn`, `DefaultDaemonNotifyOn`),
  `config.go` (`Config.Notify`, init), `daemon_notify_test.go`.
- `internal/daemon/notify.go`, `notify_test.go`; one field (`ntf`) and two hook lines in `server.go`.
- `AGENTS.md`: package table (`internal/notify`, httpapi row) and "Daemon notifications" in Key behaviours.

## Why

A phone gets alerts today through the ntfy app with no backend; native push comes later.

## Choices beyond the brief

- The tap uses `bus.subscribe` (a normal human-identity subscriber) instead of a new hook in `events.go`, so a slow
  sink can never block a publish and the other agents' edits to `events.go` do not conflict.
- `question` text is the message body, read from the mailbox store by the event id (mail events never carry bodies), only with `include_preview` (coordinator follow-up, `TestNotifyQuestionTextFromStore`).
- Config errors in `[notify]` are reported with the `notify.` prefix; a secret problem at start leaves the tap off and
  logs the cause instead of failing the daemon.

## Verification

`gofmt -l .`, `go vet ./...`, `go test -race ./internal/notify/... ./internal/httpapi/... ./internal/config/...
./internal/daemon/...` (results in the report).
