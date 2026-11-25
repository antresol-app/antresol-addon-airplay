# Antresol home speakers addon («Колонки дома»)

«Воспроизвести на…» for the music player: music and audiobooks from any
client (a browser on any OS included) play on the AirPlay and Chromecast
speakers of the home network, one or several at once. The server makes
the sound, not the device: the web player becomes a remote.

It is built on [OwnTone](https://github.com/owntone/owntone-server), which
finds the speakers over mDNS, speaks AirPlay 1/2 (HomePod with pairing) and
Chromecast, and plays URLs it is given. Contract 1
(`docs/addons/contract.md` in the core): the `audioOutputs` slot, the SDK's
`attachAudioRemote`, a helper service, the `helpers.status` hook; no
permission.

## How it works

1. The music player shows «Воспроизвести на…» in «Сейчас играет». The
   picker lists OwnTone's speakers (its own sound card is left out).
2. Ticking a speaker selects it in OwnTone and attaches the addon as the
   player's remote: the browser falls silent and hands over its queue, each
   item an absolute stream URL of the server (`/api/tracks/<id>/stream`,
   `/api/audio/<id>`) carrying the profile's web media key (`mk=`, 4 h,
   ends with the session).
3. The addon accepts only such URLs, and gives OwnTone a window of the
   queue (the current item and four after it, tagged with title and
   artist): OwnTone probes every URL it is given, so a whole playlist is
   never added. Once a second it asks OwnTone where it is, moves the window
   on and drops played items, so the music goes on with the tab closed.
4. The browser polls the addon once a second and follows: the current
   track, the position, play or pause, the volume. Its controls (and the
   system media controls) go to OwnTone: play, pause, seek (OwnTone seeks
   in the file by Range), next/previous (the browser's queue rules decide),
   volume (OwnTone's master; each speaker has its own slider in the picker).
5. «Этот браузер» gives the listening back where the speakers were; the
   speakers stop and are deselected.

One listening plays at a time (one OwnTone): another profile picking the
speakers takes them over, and the first one's browser plays on itself.

## Running OwnTone

OwnTone must be on the **host's network**: it finds the speakers by mDNS,
which a compose network does not carry. So it is not a plain helper on the
project's network; the settings page (and the picker, for admins) shows
this block to add under `services:`:

```yaml
  owntone:
    image: owntone/owntone:29.3
    restart: unless-stopped
    network_mode: host
    volumes:
      - ./owntone/etc:/etc/owntone
      - ./owntone/cache:/var/cache/owntone   # chown 1000:1000 (the image's owntone user)
```

The addon looks for OwnTone at `ANTRESOL_AIRPLAY_OWNTONE_URL` when it is
set, else at `http://owntone:3689`, `http://127.0.0.1:3689` (the server on
the host's network too) and `http://host.docker.internal:3689` (the server
on a bridge network, with `extra_hosts: ["host.docker.internal:host-gateway"]`
in its service).

Known constraints, found on a real rehearsal:

- **Many Docker networks.** Avahi inside the OwnTone container joins the
  mDNS group on every interface of the host; past about 20 (each compose
  network and container adds some) the kernel refuses
  (`IP_ADD_MEMBERSHIP failed: No buffer space available`) and the LAN's
  speakers are never seen. Limit Avahi to the LAN interface:
  `allow-interfaces=eth0` under `[server]` of `/etc/avahi/avahi-daemon.conf`
  (mount your own copy), or raise `net.ipv4.igmp_max_memberships`.
- **AirPlay 2 timing (PTP, UDP 319/320).** OwnTone 29 runs its own PTP
  service; on a host where another AirPlay program (shairport-sync's nqptp)
  holds those ports it logs «only NTP will be available» and still plays
  to AirPlay 2 speakers (a HomePod) over NTP timing.
- **Its port is on the LAN.** With the host network OwnTone's web UI and
  JSON API listen on 3689 for the whole LAN, and its default
  `trusted_networks` lets any client in; its queue shows the stream URLs
  (with the media key). Set `trusted_networks = { "localhost" }` with an
  `admin_password` (give the addon `ANTRESOL_AIRPLAY_OWNTONE_PASSWORD`), or
  `bind_address`, in `owntone.conf`.
- **Where OwnTone fetches the stream.** By default the address the server
  says the browser reached it at; a host named in the queue is never used.
  The server must tell it: the release's `minCore` is the first version
  that does. A browser on `localhost`, or outside the home network (the
  public address: the server may then cap the bitrate and seeking stops
  working), is not what OwnTone should use: set
  `ANTRESOL_AIRPLAY_STREAM_URL` to the server's address on the home network
  (it also pins the address whatever a request says).
- **Multiroom** is OwnTone's: AirPlay 2 speakers stay in sync, AirPlay 1 and
  Chromecast ones may drift slightly.

## Not in this version

- Video and TV channels (the speakers take audio only; the web player's
  own AirPlay of video in Safari stays as it is).
- Audiobook speed (OwnTone plays at 1×) and covers on the speakers (the
  server's images need a session).
- Taking control again after a reload: the picker says the music still
  plays on the speakers and offers «Остановить».
- With the tab closed, play counts and an audiobook's saved position stop
  moving (the browser keeps them).
- The native apps: they keep their own AirPlay/Cast; they can use the same
  API (`/api/addons/airplay/x/…`) later.

## API (`/api/addons/airplay/x/…`, adult profiles)

| Path | What |
| --- | --- |
| `GET state` | `{ready, outputs: [{id, name, kind, type, selected, volume, needsPin?, locked?}], status, compose?}` |
| `PUT outputs` `{ids}` | exactly these speakers on |
| `PUT outputs/{id}` `{selected?, volume?}` | one speaker on or off, its volume (0-100) |
| `POST outputs/{id}/pin` `{pin}` | the 4-digit code a HomePod or Apple TV shows |
| `POST session` `{snapshot, jump}` | the queue (`RemoteSnapshot`) → `{session}` |
| `GET session` | `{session, owner, mine, state, key, positionMs, durationMs, volume, ended?}` |
| `POST session/play`, `pause`, `stop`, `seek {ms}`, `volume {volume}` | with `{session}` |

## Build

`make build` (three platforms, the web part, the manifest), `make local`
for a development server, `make test`.

## Licence

Licence: MIT

The MIT licence (see `LICENSE`), Copyright (c) 2026 Antresol Contributors.
OwnTone, which runs beside the addon as its own program, is under its own
licence (GPL-2.0).
