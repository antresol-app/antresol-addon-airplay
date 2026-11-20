// The «Колонки дома» addon's web part: «Воспроизвести на…» in the music
// player (audioOutputs), and «Колонки дома» in «Настройки → Сервер» for
// admins (whether OwnTone is found, and the compose block when not).
import { registerAddon } from '@antresol/addon-sdk'
import { Picker } from './Picker'
import { Settings } from './Settings'
import { ID } from './remote'
import { strings } from './strings'

const lang = document.documentElement.lang === 'en' ? 'en' : 'ru'

registerAddon(ID, {
  audioOutputs: [{ label: 'output', icon: 'airplayAudio', run: (close) => <Picker onClose={close} /> }],
  routes: [{ path: 'settings', title: strings[lang]['settings.title'], element: Settings, admin: true }],
  manageTiles: [{ label: 'settings.title', icon: 'antenna', tint: 'indigo', group: 'playback', to: 'settings', admin: true }],
  strings,
})
