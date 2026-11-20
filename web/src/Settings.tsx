// «Настройки → Сервер → Колонки дома» (admins): whether OwnTone is found,
// how many speakers it sees, and the compose block to add when it is not.
import { useEffect, useState } from 'react'
import { ui, useAddonT } from '@antresol/addon-sdk'
import { api, ID } from './remote'
import styles from './Picker.module.css'

export function Settings() {
  const t = useAddonT(ID)
  const [st, setSt] = useState<{ ready: boolean; outputs: unknown[]; compose?: string } | null>(null)
  const [failed, setFailed] = useState('')
  useEffect(() => {
    api.get<{ ready: boolean; outputs: unknown[]; compose?: string }>('state').then(setSt, (e: unknown) => setFailed(e instanceof Error ? e.message : t('err.generic')))
  }, [t])
  return (
    <section className={styles.picker}>
      <h1 style={{ font: 'var(--t-large-title)', margin: 0 }}>{t('settings.title')}</h1>
      {failed && <ui.Notice tone="destructive">{failed}</ui.Notice>}
      {st && (st.ready ? <ui.Notice tone="accent">{t('settings.found', { n: st.outputs.length })}</ui.Notice> : <ui.Notice tone="warning">{t('settings.missing')}</ui.Notice>)}
      <p className={styles.note}>{t('settings.how')}</p>
      {st?.compose && <pre className={styles.compose}>{st.compose}</pre>}
      <p className={styles.note}>{t('settings.notes')}</p>
    </section>
  )
}
