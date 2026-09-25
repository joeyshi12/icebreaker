# icebreaker

A WebRTC rendezvous server for peer to peer browser apps: room codes, the SDP
handover between a host and its joiners, and the ICE servers they need.

An app using this puts one peer at the centre of a star: joiners connect to the host and
to nobody else. This server holds a code, passes the blobs, keeps no application state
and is never in the data path. Each peer holds one WebSocket for as long as it is in a
room, so an offer reaches the host when it is made and the server knows when somebody
leaves. One static binary from one dependency, `github.com/coder/websocket`.

```
cmd/icebreaker/    the binary: config, wiring, graceful shutdown
internal/api/      the socket the apps talk over, and the two http endpoints
internal/room/     rooms, seats, codes, expiry
internal/creds/    ephemeral TURN credentials for a relay elsewhere
internal/config/   settings from the environment
```

## Running it

```bash
make build && ./icebreaker   # or: make run
make test                    # 53 cases
make race
```

| Variable      | Default                        | Meaning                                       |
| ------------- | ------------------------------ | --------------------------------------------- |
| `PORT`        | 8001                           | http port                                     |
| `ORIGINS`     | any                            | comma separated allowlist                     |
| `STUN_URLS`   | stun:stun.l.google.com:19302   | advertised to clients                         |
| `TURN_SECRET` | none                           | the relay's shared secret, for minting        |
| `TURN_URLS`   | none                           | comma separated, advertised to clients        |
| `TURN_TTL`    | 1h                             | credential lifetime                           |
| `ROOM_TTL`    | 15m                            | backstop for a room whose host vanished       |
| `MAX_ROOMS`   | 500                            | across every app, because it guards memory    |
| `MAX_JOINERS` | 3                              | joiners a room takes, for apps `APPS` omits   |
| `APPS`        | none                           | optional: joiners per app, `arena:3,quiz:11`  |

STUN only is the default; relay credentials need both `TURN_SECRET` and `TURN_URLS`, and
startup reports which of the four combinations is in effect. A joiner count excludes the
host, who holds seat 0, so `arena:3` is a room of four.

`ROOM_TTL` rarely collects anything: a room goes when its host hangs up, and a peer's
ping every 30 seconds pushes the expiry back. It catches a host that went without the
connection noticing.

Behind a reverse proxy `/ws` needs the upgrade passed through. Caddy does that unasked;
nginx needs `proxy_set_header Upgrade $http_upgrade`, `proxy_set_header Connection
"upgrade"` and a `proxy_read_timeout` longer than the ping.

## The protocol

| Endpoint       | Purpose                                                    |
| -------------- | ---------------------------------------------------------- |
| `GET /ws`      | signalling: one connection per peer, JSON text messages     |
| `GET /ice`     | STUN and TURN servers, for a peer with no connection yet    |
| `GET /health`  | `{ok, rooms, version}`                                     |

`/ws` takes `?app=`: lowercased and trimmed, `a-z`, `0-9` and `-` only, 32 characters at
most, anything else refused before the upgrade. No key lands in an unnamed namespace. A
connection arrives with no role; its first message decides, and it keeps that role until
it hangs up.

```jsonc
// a client sends
{"type":"host"}                            // reserve a room
{"type":"join","code":"AB2C","offer":{…}}  // take a seat, leave an offer
{"type":"answer","seat":1,"answer":{…}}    // host only: reply to one seat
{"type":"ice"}                             // ask for the ICE servers again
{"type":"close"}                           // host only: break the room up

// the server sends
{"type":"hosted","code","app","max_joiners","expires_in","ice_servers"}  // host
{"type":"joined","seat","ice_servers"}                                   // joiner
{"type":"offer","seat","offer"}                                          // host
{"type":"answer","seat","answer"}                                        // joiner
{"type":"ice_servers","ice_servers"}
{"type":"closed","reason"}                                               // joiner
{"type":"error","reason","message"}
```

A client branches on `reason`, one of `no_room`, `full`, `no_seat`, `busy`,
`bad_message`, `wrong_role`; `message` is for a person. `no_room` is the one worth
handling: the room is gone rather than something having gone wrong, and it is what a live
code for the wrong app gets too. An error refuses one message and leaves the connection
up; a `closed` is followed by the connection closing.

The joiner offers and the host answers, because an offer belongs to one peer connection,
so the code exists before the host has gathered candidates.

Joiners are numbered from 1, and a seat is held only while its joiner holds its
connection, so a room players have passed through is joinable again. Numbers stay inside
the room's size rather than climbing, because an app may use one as an index. Losing the
host ends the room.

## Apps

A room is identified by an app and a code together, so several apps can share a
deployment and a live code for the wrong app gets the same `no_room` as a made up one.
Without that the join would succeed and the room would spend a seat on a peer that can
never use it.

`APPS` sets each app's joiner cap, and a key nobody named gets `MAX_JOINERS`, so an app
can be given a bigger lobby without stranding a client that predates app keys. An entry
`APPS` cannot parse is reported at startup, because it silently leaves that app on the
default otherwise. There is no allowlist, deliberately: an unrecognised key only means a
room nobody else can find, and `MAX_ROOMS` bounds the memory whatever keys exist. This is
namespacing, not authentication.

## Relays

Some routers assign a different external port per destination, so the address STUN
reports is useless to anyone else and hole punching fails. The fix is a relay both peers
connect out to, which published figures say 15 to 30 percent of consumer sessions need.

This server does not run one: a TURN allocation needs a port of its own, which cannot sit
behind an HTTP reverse proxy, and relayed traffic costs bandwidth. It does mint
credentials for a relay run elsewhere, the scheme coturn calls `use-auth-secret`: the
username is an expiry plus a name, the password its HMAC-SHA1 under a shared secret, and
the relay recomputes the HMAC. Nothing is stored and a leaked credential expires on its
own. Point `TURN_SECRET` and `TURN_URLS` at a coturn, a managed service, or nothing.
`turns:` on 443 survives networks that block UDP and unfamiliar ports.

## Docker

```bash
docker run -p 8001:8001 ghcr.io/joeyshi12/icebreaker:edge

docker run -p 8001:8001 \
  -e TURN_SECRET=... -e TURN_URLS=turns:relay.example:5349 \
  ghcr.io/joeyshi12/icebreaker:edge
```

`:edge` and the short commit are published on every push to main; `:latest` and a version
arrive with a `v*` tag. CI runs gofmt, vet, the suite and the race detector.
