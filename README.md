# icebreaker

A WebRTC rendezvous server for peer to peer browser apps: room codes, the SDP
handover between a host and its joiners, and the ICE servers they need. One static
binary, one dependency.

An app using this puts one peer at the centre of a star: joiners connect to the host
and to nobody else. This server holds a room code and passes the blobs. It keeps no
application state, drops everything when a room closes, and is never in the data
path.

Each peer holds one WebSocket for as long as it is in a room, so an offer reaches the
host when it is made and the server knows when somebody leaves.

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

STUN only is the default. Relay credentials need both `TURN_SECRET` and `TURN_URLS`,
and startup reports which of the four combinations is in effect.

A joiner count excludes the host, who holds seat 0, so `arena:3` is a room of four.

`ROOM_TTL` is rarely what collects a room. A peer's connection is pinged every 30
seconds and that pushes the expiry back, so a room lives as long as somebody is in it,
and a room is dropped outright when its host hangs up. What the TTL still catches is a
host that went away without the connection noticing, which a half open TCP connection
can do.

## Talking to it

| Endpoint       | Purpose                                                    |
| -------------- | ---------------------------------------------------------- |
| `GET /ws`      | signalling: one connection per peer, JSON text messages     |
| `GET /ice`     | STUN and TURN servers, for a peer with no connection yet    |
| `GET /health`  | `{ok, rooms, version}`                                     |

`/ws` takes `?app=`. A key is lowercased and trimmed, may hold only `a-z`, `0-9` and
`-`, and is at most 32 characters; anything else is refused before the upgrade. Sending
no key is legal and lands in an unnamed namespace.

A connection arrives with no role and the first message it sends decides: `host` makes
it a host, `join` makes it a joiner, and it keeps that role until it hangs up.

What a client sends:

| Message                                        | Meaning                              |
| ---------------------------------------------- | ------------------------------------ |
| `{"type":"host"}`                              | reserve a room                        |
| `{"type":"join","code":…,"offer":{…}}`         | take a seat and leave an offer        |
| `{"type":"answer","seat":n,"answer":{…}}`      | host only: reply to one seat          |
| `{"type":"ice"}`                                | ask for the ICE servers again         |
| `{"type":"close"}`                              | host only: break the room up now      |

What the server sends:

| Message                                                                  | To     |
| ------------------------------------------------------------------------ | ------ |
| `{"type":"hosted","code":…,"app":…,"max_joiners":n,"expires_in":s,"ice_servers":[…]}` | host |
| `{"type":"joined","seat":n,"ice_servers":[…]}`                            | joiner |
| `{"type":"offer","seat":n,"offer":{…}}`                                   | host   |
| `{"type":"answer","seat":n,"answer":{…}}`                                 | joiner |
| `{"type":"ice_servers","ice_servers":[…]}`                               | either |
| `{"type":"closed","reason":…}`                                            | joiner |
| `{"type":"error","reason":…,"message":…}`                                 | either |

`reason` on an error is what a client branches on, and `message` is for a person to
read. The reasons are `no_room`, `full`, `no_seat`, `busy`, `bad_message` and
`wrong_role`. `no_room` is the one worth handling: it means the room is gone rather
than that something went wrong, and it is also what a live code for the wrong app gets.

An error refuses one message and leaves the connection up. A `closed` is followed by the
connection closing, because there is nothing left to signal about.

Seat 0 is the host, joiners are numbered from 1, and what a seat entitles a peer to is
the app's business. A seat belongs to its joiner for as long as that joiner holds its
connection and is given to the next arrival once it lets go, so a room that players have
passed through is still joinable — which is what lets somebody who dropped mid-match
signal their way back in. Seat numbers stay inside the room's size rather than climbing,
because an app is entitled to use one as an index.

The joiner offers and the host answers. An offer belongs to one peer connection, so a
host cannot publish one offer for three joiners, and this way the room code exists
before the host has gathered candidates.

Losing the host ends the room: the joiners are told `closed` and the code is free again.

## Apps

A room is identified by an app and a code together, so several apps can share a
deployment and a peer holding a live code for the wrong app gets the same
`no_room` as a made up one. Without that the join would succeed and the room would
spend a seat on a peer that can never use it.

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

## The dependency

`github.com/coder/websocket`, which has no dependencies of its own, so the build is
still one static binary from one module download. Go has no WebSocket in its standard
library and the framing is not worth hand-rolling to keep a boast.

Behind a reverse proxy, `/ws` needs the upgrade passed through. Caddy does this
without being asked; nginx needs `proxy_set_header Upgrade $http_upgrade` and
`proxy_set_header Connection "upgrade"`, and a `proxy_read_timeout` longer than the
30 second ping or it will cut idle lobbies off.

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
