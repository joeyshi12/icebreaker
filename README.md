# icebreaker

A WebRTC rendezvous for peer to peer browser games: room codes, the SDP handover
between a host and its joiners, and the ICE servers they need. One static binary,
standard library only.

The name is the job. It hands out ICE servers, and it introduces peers who have
never met so they can talk among themselves.

A game using this puts one peer at the centre of a star: joiners connect to the host
and to nobody else. This service holds a room code, passes the blobs, and gets out
of the way. It sees no game state, keeps nothing once a room closes, and is never in
the data path.

```
cmd/icebreaker/    the binary: config, wiring, graceful shutdown
internal/api/      the http endpoints the games talk to
internal/room/     rooms, seats, codes, expiry
internal/creds/    ephemeral TURN credentials for a relay elsewhere
internal/config/   settings from the environment
```

## Running it

```bash
make build && ./icebreaker   # or: make run
make test                    # 41 cases
make race
```

STUN only is the default. Handing out relay credentials needs both a secret shared
with the relay and addresses to advertise, and startup reports which of the four
states the config is in.

| Variable      | Default                        | Meaning                                       |
| ------------- | ------------------------------ | --------------------------------------------- |
| `PORT`        | 8001                           | http port                                     |
| `ORIGINS`     | any                            | comma separated allowlist                     |
| `STUN_URLS`   | stun:stun.l.google.com:19302   | advertised to clients                         |
| `TURN_SECRET` | none                           | the relay's shared secret, for minting        |
| `TURN_URLS`   | none                           | comma separated, advertised to clients        |
| `TURN_TTL`    | 1h                             | credential lifetime                           |
| `ROOM_TTL`    | 15m                            | how long an unused room lives                 |
| `MAX_ROOMS`   | 500                            | across every game, because it guards memory   |
| `MAX_JOINERS` | 3                              | joiners per room, for games `GAMES` omits     |
| `GAMES`       | none                           | `arena:3,quiz:11`, which also closes the set   |

## Endpoints

| Endpoint                    | Purpose                                                   |
| --------------------------- | --------------------------------------------------------- |
| `GET /ice`                  | STUN and TURN servers, fetched before candidate gathering  |
| `POST /host`                | reserve a room, returns a four character code              |
| `POST /join`                | leave an offer, returns a seat                             |
| `GET /offers/{code}`        | host collects new offers, each handed over once            |
| `POST /answer`              | host leaves its answer for a seat                          |
| `GET /answer/{code}/{seat}` | joiner collects its answer                                 |
| `POST /close`               | host drops the room once the match starts                  |
| `GET /health`               | `{ok, rooms, version}`                                    |

Seat 0 is the host; joiners are numbered from 1 in arrival order, and what a seat
entitles a peer to is the game's business. Both reads are destructive: an offer is
handed over once and an answer is deleted when collected, so a client that loses a
response cannot ask again.

The joiner offers and the host answers, rather than the other way round. An offer
belongs to one peer connection, so a host cannot publish one offer for three
joiners, and this way the room code exists before the host has gathered candidates.

## Games

A room is identified by a game and a code together, so several games can share a
deployment. A peer holding a live code for the wrong game is told
`404 no room with that code`, the same thing a made up code gets. Without that the
join would succeed, the offer would land in the other game's mailbox, and the
mismatch would surface only when the first game message proved unreadable, by which
point the room has spent a seat it never gets back.

Every endpoint except `/ice` and `/health` takes the game as `?game=`. A key is
lowercased and trimmed, may hold only `a-z`, `0-9` and `-`, and is at most 32
characters; anything else is a `400`. Sending no key is legal and lands in an
unnamed namespace, which is what lets a client written before games existed keep
working.

`GAMES` sets each game's joiner cap and closes the set of keys, so a typo becomes an
error rather than a namespace of its own. Mind the ordering: closing the set leaves
the unnamed namespace unconfigured, so name the games only once the clients are
sending keys.

This is namespacing, not authentication. A modified client can claim any key it
likes. It prevents accidents and collisions in a shared code space, nothing more.

## Relays

Some routers assign a different external port per destination, so the address a peer
learns from STUN is useless to anyone else and hole punching cannot work. The only
fix is a relay both peers connect out to, which copies packets between them.
Published figures put the share of consumer sessions needing one at 15 to 30
percent, with friends on home broadband at the bottom of that range.

This service does not run one. A TURN allocation listens on a port of its own, which
cannot sit behind an HTTP reverse proxy, and relayed traffic costs bandwidth on the
machine carrying it.

It does mint the credentials for a relay run elsewhere, the scheme coturn calls
`use-auth-secret`: the username is an expiry plus a name, the password its HMAC-SHA1
under a shared secret, and the relay recomputes the HMAC rather than looking
anything up. Nothing is stored and a leaked credential expires on its own. Point
`TURN_SECRET` and `TURN_URLS` at a coturn you run, a managed service, or nothing.
`turns:` on 443 is the variant worth having, since it survives networks that block
UDP and unfamiliar ports.

## Docker

```bash
docker run -p 8001:8001 ghcr.io/joeyshi12/icebreaker:edge

docker run -p 8001:8001 \
  -e TURN_SECRET=... -e TURN_URLS=turns:relay.example:5349 \
  ghcr.io/joeyshi12/icebreaker:edge
```

`:edge` and the short commit are published on every push to main; `:latest` and a
version arrive with a `v*` tag, so until the first release `:edge` is the only
moving tag. CI runs gofmt, vet, the suite and the race detector on every push and
pull request, and publishing uses the built-in `GITHUB_TOKEN`, so there are no
registry secrets.
