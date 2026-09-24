# icebreaker

A WebRTC rendezvous server for peer to peer browser apps: room codes, the SDP
handover between a host and its joiners, and the ICE servers they need. One static
binary, standard library only.

An app using this puts one peer at the centre of a star: joiners connect to the host
and to nobody else. This server holds a room code and passes the blobs. It keeps no
application state, drops everything when a room closes, and is never in the data
path.

```
cmd/icebreaker/    the binary: config, wiring, graceful shutdown
internal/api/      the http endpoints the apps talk to
internal/room/     rooms, seats, codes, expiry
internal/creds/    ephemeral TURN credentials for a relay elsewhere
internal/config/   settings from the environment
```

## Running it

```bash
make build && ./icebreaker   # or: make run
make test                    # 43 cases
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
| `ROOM_TTL`    | 15m                            | idle time before a room is dropped            |
| `MAX_ROOMS`   | 500                            | across every app, because it guards memory    |
| `MAX_JOINERS` | 3                              | joiners a room takes, for apps `APPS` omits   |
| `APPS`        | none                           | optional: joiners per app, `arena:3,quiz:11`  |

STUN only is the default. Relay credentials need both `TURN_SECRET` and `TURN_URLS`,
and startup reports which of the four combinations is in effect.

A joiner count excludes the host, who holds seat 0, so `arena:3` is a room of four.
`ROOM_TTL` is idle time: any request for a room pushes its expiry back, so a session
longer than the TTL keeps its room while someone is still polling.

## Endpoints

| Endpoint                    | Purpose                                                   |
| --------------------------- | --------------------------------------------------------- |
| `GET /ice`                  | STUN and TURN servers, fetched before candidate gathering  |
| `POST /host`                | reserve a room, returns a four character code              |
| `POST /join`                | leave an offer, returns a seat                             |
| `GET /offers/{code}`        | host collects new offers, each handed over once            |
| `POST /answer`              | host leaves its answer for a seat                          |
| `GET /answer/{code}/{seat}` | joiner collects its answer                                 |
| `POST /close`               | host drops the room once everyone is connected             |
| `GET /health`               | `{ok, rooms, version}`                                    |

Every endpoint except `/ice` and `/health` takes `?app=`. A key is lowercased and
trimmed, may hold only `a-z`, `0-9` and `-`, and is at most 32 characters; anything
else is a `400`. Sending no key is legal and lands in an unnamed namespace.

Seat 0 is the host, joiners are numbered from 1 in arrival order, and what a seat
entitles a peer to is the app's business. Both reads are destructive: an offer is
handed over once, an answer deleted when collected.

The joiner offers and the host answers. An offer belongs to one peer connection, so a
host cannot publish one offer for three joiners, and this way the room code exists
before the host has gathered candidates.

## Apps

A room is identified by an app and a code together, so several apps can share a
deployment and a peer holding a live code for the wrong app gets the same
`404 no room with that code` as a made up one. Without that the join would succeed
and the room would spend a seat on a peer that can never use it.

`APPS` sets each app's joiner cap. Any key is still accepted, and one nobody named
gets `MAX_JOINERS`, so an app can be given a bigger lobby without stranding a client
that predates app keys. An entry `APPS` cannot parse is reported at startup, because
it silently leaves that app on the default otherwise.

There is no allowlist, deliberately. An unrecognised key only ever means a room
nobody else can find, which the host learns the moment a friend reads the code back,
and `MAX_ROOMS` bounds the memory whatever keys exist.

Namespacing, not authentication: a modified client can claim any key.

## Relays

Some routers assign a different external port per destination, so the address STUN
reports is useless to anyone else and hole punching fails. The fix is a relay both
peers connect out to. Published figures put the share of consumer sessions needing
one at 15 to 30 percent.

This server does not run one: a TURN allocation needs a port of its own, which cannot
sit behind an HTTP reverse proxy, and relayed traffic costs bandwidth on the machine
carrying it.

It does mint credentials for a relay run elsewhere, the scheme coturn calls
`use-auth-secret`: the username is an expiry plus a name, the password its HMAC-SHA1
under a shared secret, and the relay recomputes the HMAC. Nothing is stored and a
leaked credential expires on its own. Point `TURN_SECRET` and `TURN_URLS` at a coturn,
a managed service, or nothing. `turns:` on 443 survives networks that block UDP and
unfamiliar ports.

## Docker

```bash
docker run -p 8001:8001 ghcr.io/joeyshi12/icebreaker:edge

docker run -p 8001:8001 \
  -e TURN_SECRET=... -e TURN_URLS=turns:relay.example:5349 \
  ghcr.io/joeyshi12/icebreaker:edge
```

`:edge` and the short commit are published on every push to main; `:latest` and a
version arrive with a `v*` tag. CI runs gofmt, vet, the suite and the race detector
on every push and pull request, and publishing uses the built-in `GITHUB_TOKEN`.
