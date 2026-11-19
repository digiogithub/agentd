# Relay transport: agentd-relay, daemon link, device permissions

Implements [[design/relay-transport]] under the plan [[agentd/plans/relay_plan.md]]. Not to be confused with
[[design/cross-host-relay]] (mail bridged through a client). The KB, code-index and Context7 tools named in
AGENTS.md were not available in this session, so this record is a repository file.

## What changed

- New self-hosted server `cmd/agentd-relay` (`internal/relayserver`): hosts register a route with a signed
  challenge + join token; each client tunnel gets a fresh host data connection and the relay forwards opaque
  WebSocket messages. Flags: TLS files, ACME (`--acme-domain`), `--insecure-http` behind a proxy, `--open`,
  `--join-token-file` / `$AGENTD_RELAY_JOIN_TOKEN`, `--trust-forwarded`, `--max-tunnels`. Limits: 64 tunnels
  and 16 pending connects per route, 30 connects/min per IP, 70 KiB messages, accept timeout 10 s.
- `internal/relaywire`: host key (ed25519 route key + X25519 Noise key), route and device ids, invite link,
  Noise IK / IKpsk2 handshakes (`ServerHandshake` tries IK then IKpsk2 on msg1 and sets the psk once the
  invite is known), `Secure`, stream `Mux` (credit flow control, deadlines, `net.Conn` streams), WebSocket
  adapter, `RunHostLink`. Interop vectors for the Dart client in `testdata/noise_vectors.json`.
- `internal/relayclient`: `Pair`, `Connect`, `Dialer` for Go clients.
- Daemon: `[relay]` config (`internal/config/relay.go`), `config.RelayDir`, `internal/daemon/relay.go`
  (host key, devices.json, invites with approval, tunnels, op `relay`, audit log) and `relaygate.go` (deny-by-
  default op gate per scope, session filter, result filtering, audit of side effects). `serveConn` split out
  of `handleConn` so relay streams are served with `peerInfo.device`; `resolveOnce` resolves devices to
  `human:<user>` via `relay`; the event filter drops events outside the device's sessions; `whoami` adds
  `device`. Shutdown stops the relay after the bus said bye.
- Proto: `OpRelay`, `RelayArgs`/`RelayResult`/`RelayDeviceInfo`/`RelayInviteInfo`/`RelayRequest`,
  `WhoamiResult.Device`. Client: `(*Client).Relay`.
- CLI: `agentd relay status | invite | devices | revoke`.
- Build/release: `make relay`, release asset `agentd-relay_<v>_linux_<arch>.tar.gz`,
  `packaging/agentd-relay.service`. Deps added: `github.com/coder/websocket` v1.8.15, `github.com/flynn/noise`
  v1.1.0 (AGENTS.md Toolchain updated).
- Docs: AGENTS.md (binaries, deps, package rows, "Relay transport", ops, identity, paths), README "Relay",
  ROADMAP Phase 9, design status.

## Why

The owner wants clients (desktop and mobile) to reach hosts either over SSH or through a relay, with a clear
permission model and invite tokens like Herdr clients have (Alma's e2e relay, herdrup guests). Design choices:
the code in the invite only bootstraps a device key (revoking one device does not re-pair the rest); the relay
is blind; the daemon enforces scopes because many ops never checked identity (every local caller is the same uid).

## How it was verified

- `go test -race ./internal/relaywire ./internal/relayserver`: handshakes (connect, refused, pair, wrong code,
  mode mismatch), mux (4 concurrent 3 MiB echoes past the window, close, deadlines), invite parsing, vectors,
  relay echo through `httptest`, host offline (4404), refused join token, relink replacing a link.
- `go test -race -run TestRelay ./internal/daemon`: denied pairing; view device limited to `codex@*` sees one
  session, `dump` outside the filter is `not_found`, `act/type/kill/shutdown/relay/run` are `forbidden`, two
  streams on one tunnel, revoke closes the stream and refuses reconnect; admin `run` with `cmd` refused, audit
  log written, `devices.json` is 0600, device listed online.
- `go test -run TestRelay ./cmd/agentd`: `agentd relay invite` prints the link, a device pairs, the prompt is
  approved, "Paired" printed.
- Full `go test ./...` and `-race` on daemon/session/delivery/relay packages: see the session report.
- `go list -deps ./cmd/agentd | grep -c mygo` = 0.

## Pending

Field test on a VPS (relay behind TLS, host behind NAT, real phone). agentd-app relay transport is done in its repo (`lib/src/relay/`, Noise vectors reproduced byte for byte, live loopback run against agentd-relay + daemon). Desktop relay hosts are done (`internal/desktopui/core/relay.go`, `backend.PairRelay`, `Host.Allows`; see `internal/desktopui/README.md` "Relay hosts").
