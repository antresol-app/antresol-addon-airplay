# Расширение Antresol «Колонки дома»

«Воспроизвести на…» в музыкальном плеере: музыка и аудиокниги с любого
клиента (и из браузера на любой ОС) играют на колонках AirPlay и Chromecast
домашней сети — на одной или на нескольких сразу. Звук делает сервер, а
плеер в браузере становится пультом.

Работает через [OwnTone](https://github.com/owntone/owntone-server): он
находит колонки по mDNS, говорит на AirPlay 1/2 (HomePod — с кодом
сопряжения) и Chromecast и играет переданные ему адреса.

## Как включить

OwnTone нужна **сеть хоста** (колонки находятся по mDNS, а сеть compose его
не пропускает). Добавьте в `compose.yml` под `services:`:

```yaml
  owntone:
    image: owntone/owntone:29.3
    restart: unless-stopped
    network_mode: host
    volumes:
      - ./owntone/etc:/etc/owntone
      - ./owntone/cache:/var/cache/owntone   # владелец 1000:1000
```

Расширение ищет OwnTone по `ANTRESOL_AIRPLAY_OWNTONE_URL`, иначе по
`http://owntone:3689`, `http://127.0.0.1:3689` и
`http://host.docker.internal:3689` (сервер в сети bridge, с
`extra_hosts: ["host.docker.internal:host-gateway"]`).

Ограничения (подробно — в README.md):

- много сетей Docker на хосте — Avahi в контейнере не видит колонки
  (`IP_ADD_MEMBERSHIP failed`): ограничьте его интерфейсом локальной сети
  (`allow-interfaces=eth0`);
- порт 3689 OwnTone открыт всей сети: задайте `trusted_networks` и
  `admin_password` (пароль — в `ANTRESOL_AIRPLAY_OWNTONE_PASSWORD`);
- поток берётся по адресу, по которому сервер видит браузер (адрес из очереди
  не используется; сервер должен его сообщать: первая такая версия — `minCore`
  релиза); если колонки не достают поток по этому адресу или адрес нужно
  закрепить, задайте `ANTRESOL_AIRPLAY_STREAM_URL` — адрес сервера в
  домашней сети;
- видео и ТВ-каналы, скорость аудиокниг и обложки на колонках — не в этой
  версии.
