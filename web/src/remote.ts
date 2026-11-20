// The speakers as the music player's remote (attachAudioRemote): every
// wish of the player goes to the addon's server, which has OwnTone play the
// queue; once a second the server says where OwnTone is, and the player
// follows. One per tab.
import { addonApi, attachAudioRemote, type AudioRemote, type RemoteHandle, type RemoteSnapshot } from '@antresol/addon-sdk'

export const ID = 'airplay'
export const api = addonApi(ID)

/** GET session: the listening on the speakers. */
export interface Status {
  session: string
  owner?: string
  mine: boolean
  state: 'play' | 'pause' | 'stop'
  key?: string
  positionMs: number
  durationMs: number
  volume: number
  ended?: boolean
}

/** Polls of the status in a row that may fail before the browser takes the listening back. */
const MAX_FAILS = 5
const POLL_MS = 1000

type Say = (key: string, vars?: Record<string, string>) => string

class Speakers implements AudioRemote {
  private handle: RemoteHandle | null = null
  private session = ''
  private timer: ReturnType<typeof setInterval> | null = null
  private fails = 0
  private say: Say = (k) => k

  /** Whether this tab's player plays on the speakers now. */
  get attached(): boolean {
    return this.handle?.active ?? false
  }

  /** Takes the listening (or renames it: the speakers picked changed). */
  attach(name: string, say: Say) {
    this.say = say
    if (this.attached) {
      this.handle!.update({ name })
      return
    }
    this.session = ''
    this.fails = 0
    this.handle = attachAudioRemote(this, name)
    if (!this.handle.active) return
    this.timer = setInterval(() => void this.poll(), POLL_MS)
  }

  /** The browser plays on itself, from where the speakers were (`resume`: playing). */
  release(resume: boolean) {
    this.handle?.release({ resume })
  }

  private send(path: string, body: Record<string, unknown> = {}) {
    return api.post(path, { session: this.session, ...body }).catch((e: unknown) => this.failed(e))
  }

  private failed(e: unknown) {
    console.warn('airplay:', e)
    if (!this.attached) return
    const msg = e instanceof Error && e.message ? e.message : this.say('err.generic')
    this.handle!.notice(msg)
  }

  sync(s: RemoteSnapshot, jump: boolean) {
    api
      .post<{ session: string }>('session', { snapshot: s, jump })
      .then((r) => {
        this.session = r.session
      })
      .catch((e: unknown) => {
        this.failed(e)
        // nothing plays on the speakers: the browser takes it back where it was
        if (jump) this.handle?.release({ resume: false })
      })
  }
  play() {
    void this.send('session/play')
  }
  pause() {
    void this.send('session/pause')
  }
  seek(sec: number) {
    void this.send('session/seek', { ms: Math.round(sec * 1000) })
  }
  volume(v: number) {
    void this.send('session/volume', { volume: Math.round(v * 100) })
  }
  stop() {
    if (this.timer) clearInterval(this.timer)
    this.timer = null
    const session = this.session
    this.handle = null
    this.session = ''
    if (session) void api.post('session/stop', { session }).catch(() => undefined)
  }

  private async poll() {
    const h = this.handle
    if (!h?.active) return
    let st: Status
    try {
      st = await api.get<Status>('session')
    } catch {
      if (++this.fails >= MAX_FAILS && h.active) {
        h.notice(this.say('err.lost'))
        h.release({ resume: false })
      }
      return
    }
    this.fails = 0
    if (!h.active || !this.session) return
    if (st.session !== this.session) {
      // another profile (or this one elsewhere) took the speakers
      h.notice(st.owner && !st.mine ? this.say('err.taken', { name: st.owner }) : this.say('err.ended'))
      h.release({ resume: false })
      return
    }
    h.update({
      key: st.key,
      positionSec: st.positionMs / 1000,
      durationSec: st.durationMs > 0 ? st.durationMs / 1000 : undefined,
      playing: st.state === 'play' && !st.ended,
      buffering: false,
      volume: st.volume / 100,
    })
  }
}

export const speakers = new Speakers()
