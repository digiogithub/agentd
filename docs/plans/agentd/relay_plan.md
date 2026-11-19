# Plan: relay transport (agentd-relay, daemon link, desktop and mobile clients)

Design: [[design/relay-transport]]. Status: phases 1-6 done (2026-11-19); field test pending.

## Decisions (2026-11-18, owner said "go ahead", recommended options taken)

1. The host link runs inside the daemon (outbound only, no listening port).
2. Deps: `github.com/coder/websocket` (WebSocket, both binaries and tests) and `github.com/flynn/noise`
   (Noise). Mobile (Dart) implements the same Noise pattern over package `cryptography`; interop is pinned by
   vectors generated from Go (`internal/relaywire/testdata/noise_vectors.json`).
3. The relay asks the host for a fresh data connection per tunnel over the control link.
4. Scopes `view < operate < admin`.
5. `run` over the relay: `admin` only, harness/profile/target only (no `cmd`, `env`, `env_base`).
6. The relay requires a `join_token` to register a host unless started with `--open`.
7. Self-hosted binary only (no public relay).
8. Clients: agentd-desktop (this repo) and agentd-app (Flutter, `/www/MCP/agentd-app`).

## Protocol v1 (normative)

### Relay endpoints (WebSocket, subprotocol `agentd-relay.v1`)

- `GET /v1/host`: host control link. JSON text messages:
  - relay → host `{"t":"challenge","nonce":"<b64url 32 bytes>"}`
  - host → relay `{"t":"register","pub":"<b64url ed25519 pub>","sig":"<b64url ed25519 sig of 'agentd-relay/1 register ' + nonce>","join_token":"<tok or empty>"}`
  - relay → host `{"t":"ok","route":"<route>"}` | `{"t":"error","error":"…"}` (then close)
  - relay → host `{"t":"incoming","tunnel":"<b64url 16 bytes>"}`
  - one live control link per route: a new registration replaces the old one (old closed).
- `GET /v1/accept?tunnel=<id>`: host data connection for one tunnel (id only known to the registered host).
- `GET /v1/connect?route=<route>`: client tunnel. If no host is registered the relay closes with status 4404;
  if the host does not accept within 10 s, 4504. Once spliced, binary messages are forwarded unchanged both ways.
- `GET /healthz` → `ok`.
- Route id = lowercase base32 (no padding) of SHA-256(host ed25519 pub), first 26 chars.
- Limits: message 70 KiB, 64 tunnels per route, 16 pending connects per route, per-IP connect rate 30/min.

### Tunnel (binary WS messages, end to end between client and daemon)

- Noise `Noise_IK_25519_ChaChaPoly_SHA256` (paired devices) or `Noise_IKpsk2_25519_ChaChaPoly_SHA256`
  (invites, psk = the 32-byte invite code). Initiator = client. Responder static = host X25519 key.
  Prologue = `agentd-relay/1\n` + route id.
- msg1 (client → host) payload JSON: `{"v":1,"mode":"connect"|"pair","invite":"<invite id, pair only>","name":"…","platform":"…"}`.
- msg2 (host → client) payload JSON: `{"v":1,"ok":true,"host":"<label>","device":{"id","scope","sessions":[],"kind"}}`
  for `connect`; `{"v":1,"ok":true,"pending":true,"host":"<label>"}` for `pair`; on refusal
  `{"v":1,"ok":false,"code":"forbidden|invalid|…","error":"…"}` and the host closes.
- Transport messages: one Noise transport ciphertext per WS message.
- **pair**: client sends `{"t":"confirm"}` (its decryption proves the psk), the host waits for approval and answers
  `{"t":"paired","device":{"id","scope","sessions","kind"},"host":"<label>"}` or `{"t":"denied","error":"…"}`, then
  closes. The client then reconnects with `connect`.
- **connect**: each plaintext is one mux frame: `type u8 | stream u32 BE | payload`.
  - 1 OPEN (client only, odd ids from 1, increasing), 2 DATA (payload <= 32 KiB), 3 CLOSE (full close; the peer
    reads buffered data then EOF), 4 CREDIT (payload u32 BE: bytes the sender may send more on that stream).
  - Initial window 256 KiB per stream per direction; a receiver grants credit as the application reads.
  - Max 64 open streams per tunnel; an OPEN beyond is answered with CLOSE.
  - Each stream carries today's daemon socket protocol unchanged (JSON lines, attach frames, subscribe events).

### Invite link

`agentd://relay?v=1&u=<relay base url, https/wss>&r=<route>&k=<host x25519 pub b64url>&i=<invite id>&c=<code b64url>&h=<label>`.
The client checks every field and that `u` is https/wss (http/ws only for loopback). Device id =
`d` + first 12 chars of lowercase base32(SHA-256(device x25519 pub)).

### Daemon

- Config `[relay]`: `enabled` (false), `url`, `label` (default hostname), `join_token` / `join_token_env`,
  `max_scope` (`operate`), `max_devices` (16).
- State `$STATE/relay/`: `host_key.json` (0600: ed25519 seed + x25519 private), `devices.json` (0600).
- Op `relay {action: status|invite|pending|approve|cancel|devices|revoke}` humans only, refused inside sessions.
- CLI `agentd relay status | invite [--name] [--scope] [--sessions a,b] [--guest] [--ttl] [--expires] [--no-approve] | devices [list|revoke ID]`.
- Relay streams are served by `handleConn` with a preset `device:<id>` caller; an op gate before dispatch
  (scope table in the design §5) and the session filter.

## Phases

1. `internal/relaywire`: mux, Noise wrappers, link/keys/route helpers, vectors. Tests `-race`. 
2. `cmd/agentd-relay` + `internal/relayserver`. Tests with in-process WS host/client.
3. Daemon: config, keys, device store, link, gate, op `relay`, CLI. Integration test: fake agent through an in-process relay.
4. Desktop: relay transport in `core`/`remote`, add host by invite link, scope-aware actions.
5. Mobile (agentd-app): Dart Noise + mux + WS transport, add host by invite link, relay hosts in the inbox.
6. Docs: AGENTS.md contracts/deps/behaviour, README, release (agentd-relay binary).
