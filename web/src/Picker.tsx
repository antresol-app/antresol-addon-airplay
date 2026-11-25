// «Воспроизвести на…»: the speakers of the home network, as Apple's AirPlay
// picker lists them: this browser first, then «Колонки и ТВ», a tick
// circle on each, and a volume slider in the row of each speaker that
// plays. Several ticked play together (multiroom). A speaker that asks for
// a PIN (a HomePod or an Apple TV pairing) gets the code field under it.
import { useCallback, useEffect, useRef, useState } from 'react'
import { Icon, ui, useAddonT, useNowPlaying, useUser, type IconName } from '@antresol/addon-sdk'
import { api, ID, speakers, type Status } from './remote'
import styles from './Picker.module.css'

interface Output {
  id: string
  name: string
  kind: 'airplay' | 'chromecast' | 'other'
  type: string
  selected: boolean
  volume: number
  needsPin?: boolean
  locked?: boolean
}

interface State {
  ready: boolean
  outputs: Output[]
  status: Status
  compose?: string
}

const REFRESH_MS = 2000

/** «Living room», «Living room и Shelf», «Living room и ещё 2». */
export function outputName(names: string[], t: (k: string, v?: Record<string, string | number>) => string): string {
  if (names.length <= 1) return names[0] ?? ''
  if (names.length === 2) return t('name.two', { a: names[0]!, b: names[1]! })
  return t('name.more', { a: names[0]!, n: names.length - 1 })
}

function deviceIcon(o: Output): IconName {
  if (o.kind === 'chromecast' || /\btv\b/i.test(o.name)) return 'tv'
  if (/homepod|\bpod\b/i.test(o.name)) return 'homepod'
  if (/\bmac\b|macbook|imac/i.test(o.name)) return 'laptop'
  return o.kind === 'airplay' ? 'speakerBox' : 'speaker'
}

export function Picker({ onClose }: { onClose: () => void }) {
  const t = useAddonT(ID)
  const user = useUser()
  const now = useNowPlaying()
  const [state, setState] = useState<State | null>(null)
  const [failed, setFailed] = useState('')
  const [busy, setBusy] = useState<string | null>(null)
  const [pinFor, setPinFor] = useState<string | null>(null)
  const [pin, setPin] = useState('')
  const alive = useRef(true)

  const load = useCallback(async () => {
    try {
      const s = await api.get<State>('state')
      if (!alive.current) return
      setState(s)
      setFailed('')
    } catch (e) {
      if (alive.current) setFailed(e instanceof Error ? e.message : t('err.generic'))
    }
  }, [t])

  useEffect(() => {
    alive.current = true
    void load()
    const id = setInterval(() => void load(), REFRESH_MS)
    return () => {
      alive.current = false
      clearInterval(id)
    }
  }, [load])

  const here = !now?.output
  // a tick means «plays there now»: while this browser plays, none is ticked
  // (OwnTone may keep a speaker selected from before)
  const outputs = (state?.outputs ?? []).map((o) => (here ? { ...o, selected: false } : o))
  const selected = outputs.filter((o) => o.selected)
  const say = useCallback((k: string, v?: Record<string, string>) => t(k, v), [t])

  /** The player follows the ticks: on the speakers while one is ticked, else here. */
  const follow = (list: Output[]) => {
    const names = list.filter((o) => o.selected).map((o) => o.name)
    if (names.length > 0 && now) speakers.attach(outputName(names, t), say)
    else if (names.length === 0 && speakers.attached) speakers.release(now?.playing ?? false)
  }

  const toggle = async (o: Output) => {
    if (busy) return
    setBusy(o.id)
    try {
      const on = !o.selected
      // from the browser: this speaker alone; on the speakers: one more, or one less
      if (here) await api.put('outputs', { ids: [o.id] })
      else await api.put(`outputs/${encodeURIComponent(o.id)}`, { selected: on })
      const next = outputs.map((x) => (x.id === o.id ? { ...x, selected: on } : here ? { ...x, selected: false } : x))
      setState((s) => (s ? { ...s, outputs: next } : s))
      if (on && o.needsPin) setPinFor(o.id)
      follow(next)
    } catch (e) {
      setFailed(e instanceof Error ? e.message : t('err.generic'))
    } finally {
      setBusy(null)
    }
  }

  const sendPin = async (o: Output) => {
    setBusy(o.id)
    try {
      await api.post(`outputs/${encodeURIComponent(o.id)}/pin`, { pin })
      setPinFor(null)
      setPin('')
      void load()
    } catch (e) {
      setFailed(e instanceof Error ? e.message : t('err.generic'))
    } finally {
      setBusy(null)
    }
  }

  // a volume drag sends at most every 150 ms, and its last value
  const volTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const setVolume = (o: Output, v: number) => {
    setState((s) => (s ? { ...s, outputs: s.outputs.map((x) => (x.id === o.id ? { ...x, volume: v } : x)) } : s))
    if (volTimer.current) clearTimeout(volTimer.current)
    volTimer.current = setTimeout(() => void api.put(`outputs/${encodeURIComponent(o.id)}`, { volume: v }).catch(() => undefined), 150)
  }

  // the speakers stop and let go (the server deselects them with the stop)
  const playHere = () => {
    if (speakers.attached) speakers.release(now?.playing ?? false)
  }

  const st = state?.status
  const elsewhere = st && st.session && !st.mine && st.state !== 'stop' ? st.owner : null
  // this profile's listening plays on the speakers from another tab or before a reload
  const orphan = st && st.session && st.mine && !speakers.attached && st.state !== 'stop'

  return (
    <ui.Sheet title={t('picker.title')} size="wide" role="dialog" noButtons dismissOnOutside onCancel={onClose}>
      <div className={styles.picker}>
        {now && (
          <div className={styles.now}>
            <span className={styles.nowText}>
              <b>{now.title}</b>
              <span>{here ? t('picker.here') : t('picker.playingOn', { name: now.output ?? '' })}</span>
            </span>
          </div>
        )}

        {failed && <ui.Notice tone="destructive">{failed}</ui.Notice>}
        {elsewhere && <ui.Notice tone="warning">{t('picker.elsewhere', { name: elsewhere })}</ui.Notice>}
        {orphan && (
          <ui.Notice
            tone="accent"
            actions={
              <ui.Button size="small" variant="secondary" onClick={() => void api.post('session/stop', { session: st.session }).then(load, load)}>
                {t('picker.stopThere')}
              </ui.Button>
            }
          >
            {t('picker.orphan')}
          </ui.Notice>
        )}

        <div className={styles.group}>{t('picker.thisDevice')}</div>
        <div className={styles.list}>
          <button type="button" className={styles.row} aria-pressed={here} onClick={playHere}>
            <span className={styles.device} data-on={here || undefined}>
              <Icon name="laptop" size={24} />
            </span>
            <span className={styles.name}>{t('picker.browser')}</span>
            <Tick on={here} />
          </button>
        </div>

        <div className={styles.group}>{t('picker.speakers')}</div>
        {state === null && !failed ? (
          <div className={styles.empty}>{t('picker.looking')}</div>
        ) : !state?.ready ? (
          <ui.Notice tone="warning" title={t('picker.noService')}>
            {user?.isAdmin && state?.compose ? (
              <>
                {t('picker.noServiceAdmin')}
                <pre className={styles.compose}>{state.compose}</pre>
              </>
            ) : (
              t('picker.noServiceUser')
            )}
          </ui.Notice>
        ) : outputs.length === 0 ? (
          <div className={styles.empty}>{t('picker.none')}</div>
        ) : (
          <div className={styles.list}>
            {outputs.map((o) => (
              <div key={o.id} className={styles.item}>
                <div className={styles.line}>
                  <button
                    type="button"
                    className={styles.row}
                    aria-pressed={o.selected}
                    aria-busy={busy === o.id || undefined}
                    disabled={!now || (busy !== null && busy !== o.id)}
                    onClick={() => void toggle(o)}
                  >
                    <span className={styles.device} data-on={o.selected || undefined}>
                      <Icon name={deviceIcon(o)} size={24} />
                    </span>
                    <span className={styles.name}>
                      {o.name}
                      <small>{o.kind === 'chromecast' ? 'Chromecast' : o.type}</small>
                    </span>
                  </button>
                  {o.selected && (
                    <ui.Slider className={styles.volume} min={0} max={100} step={1} value={o.volume} label={t('picker.volume', { name: o.name })} onChange={(v) => setVolume(o, v)} />
                  )}
                  <button type="button" className={styles.tickButton} aria-hidden tabIndex={-1} disabled={!now} onClick={() => void toggle(o)}>
                    <Tick on={o.selected} busy={busy === o.id} />
                  </button>
                </div>
                {o.locked && <div className={styles.note}>{t('picker.locked')}</div>}
                {(pinFor === o.id || (o.needsPin && o.selected)) && (
                  <form
                    className={styles.pin}
                    onSubmit={(e) => {
                      e.preventDefault()
                      void sendPin(o)
                    }}
                  >
                    <ui.TextField
                      label={t('picker.pin', { name: o.name })}
                      inputMode="numeric"
                      autoComplete="one-time-code"
                      maxLength={4}
                      value={pin}
                      onChange={(e) => setPin(e.target.value.replace(/\D/g, ''))}
                      size="small"
                    />
                    <ui.Button size="small" type="submit" disabled={pin.length !== 4} loading={busy === o.id}>
                      {t('picker.pinSend')}
                    </ui.Button>
                  </form>
                )}
              </div>
            ))}
          </div>
        )}
        {!now && <div className={styles.note}>{t('picker.nothing')}</div>}
        {selected.length > 1 && <div className={styles.note}>{t('picker.multi')}</div>}
      </div>
    </ui.Sheet>
  )
}

function Tick({ on, busy }: { on: boolean; busy?: boolean }) {
  return (
    <span className={styles.tick} data-on={on || undefined} data-busy={busy || undefined} aria-hidden>
      {on && (
        <svg viewBox="0 0 24 24" width="14" height="14">
          <path d="m6.5 12.3 3.5 3.5 7.5-7.6" fill="none" stroke="currentColor" strokeWidth="2.4" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      )}
    </span>
  )
}
