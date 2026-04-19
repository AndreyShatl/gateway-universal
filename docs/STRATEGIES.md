# Zapret стратегии — как работают и как тюнить

Коротко: **не лезь сюда, если всё работает**. Текущие стратегии калиброваны под российские ТСПУ по состоянию на весну 2026. Для других ISP/стран, возможно, потребуется подгонка.

## Архитектура

Один unified nfqws процесс на TCP (queue 200) + один на UDP (queue 201). Внутри процесса — несколько «инстансов» стратегий через `--new`, каждый со своим фильтром.

```
nfqws --daemon --qnum=200 \
    <strategy 1: instagram TCP> \
  --new \
    <strategy 2: discord TCP> \
  --new \
    <strategy 3: general TCP> \
  --new \
    <strategy 4: youtube TCP>
```

Порядок важен — фильтры проверяются сверху вниз, первый совпавший применяется.

## Текущие стратегии

### Instagram TCP (порт 443)
```
--dpi-desync=fake,multidisorder
--dpi-desync-split-pos=1,midsld
--dpi-desync-repeats=11
--dpi-desync-fooling=md5sig,badseq
--dpi-desync-autottl=2:2-12
--dpi-desync-fake-tls=tls_clienthello_www_google_com.bin
```
Жёсткая стратегия с множественной фрагментацией. `multidisorder` + `split-pos=1,midsld` режет TLS ClientHello в нескольких местах. `fooling=md5sig,badseq` обманывает DPI фейковыми пакетами.

### Discord TCP (443, 2053, 2083, 2087, 2096, 8443)
```
--dpi-desync=fake,fakedsplit
--dpi-desync-repeats=6
--dpi-desync-fooling=ts
--dpi-desync-fake-tls=tls_clienthello_www_google_com.bin
```
Мягче чем Instagram. Discord ТСПУ чувствителен к `ts` fooling — бывало что `md5sig,badseq` (как у Instagram) ломал чат.

### General TCP (Twitch, ModDB — порт 80,443)
Та же стратегия что у Discord. `fake-tls` с google.com клиентхелло проскакивает большинство DPI.

### YouTube TCP (80, 443)
Без `fake-tls`, только `fake,fakedsplit` + `ts` fooling. YouTube ТСПУ легче обойти.

### Instagram UDP (QUIC, порт 443)
```
--dpi-desync=fake
--dpi-desync-repeats=6
--dpi-desync-fake-quic=quic_initial_www_google_com.bin
```
QUIC к Meta IP разрешён в iptables ACCEPT'ом перед DROP (см. zapret.sh, `META_IPS`). Без этого QUIC падает в DROP и браузер падает на TCP.

### General UDP (fallback для других сайтов c QUIC)
Та же стратегия что Instagram UDP, без hostlist — ловит любой QUIC на 443 который проскочил DROP (обычно не проскакивает, но на всякий).

### Discord UDP (голос: 19294-19344, 50000-50100)
```
--filter-l7=discord,stun
--dpi-desync=fake
--dpi-desync-repeats=6
```
**Ключевое — `filter-l7`**. Голосовые Discord-каналы матчатся по протоколу, не по домену (потому что UDP идёт на голосовые серверы Discord которых сотни). L7 фильтр парсит пакет и ловит WebRTC/STUN сигнатуры.

## Как тюнить под свой ISP

Если какой-то сервис не работает — **не меняй сразу стратегию**. Сначала проверь:

1. **Идёт ли трафик через NFQUEUE?**
   ```bash
   iptables -t mangle -L POSTROUTING -n -v | grep NFQUEUE
   ```
   Должны быть ненулевые counters (`pkts` column). Если нули — iptables правило не ловит твой трафик.

2. **Сбрасывает ли ТСПУ соединение?**
   ```bash
   # На клиенте: tcpdump
   tcpdump -i any -nn 'host www.instagram.com and port 443'
   ```
   Если видишь RST от ТСПУ — DPI сработал, надо менять стратегию.

3. **Попробуй отключить zapret и проверить без него:**
   ```bash
   systemctl stop zapret
   # с клиента: curl -v https://www.instagram.com
   # если работает без zapret — значит zapret ломает, а не спасает
   systemctl start zapret
   ```

### Параметры для экспериментов

| Параметр | Эффект |
|---|---|
| `--dpi-desync=fake,multidisorder` | Множественная фрагментация + фейк. Самое жёсткое. |
| `--dpi-desync=fake,fakedsplit` | Один фрагмент + фейк. Мягче, быстрее. |
| `--dpi-desync=split2` | Простой split, без fake. Работает на слабом DPI. |
| `--dpi-desync-split-pos=N` | Позиция разреза (байты от начала). `1` — после первого байта, `midsld` — в середине SNI. |
| `--dpi-desync-repeats=N` | Сколько раз послать фейк. Больше — надёжнее, но медленнее. `6-11` норма. |
| `--dpi-desync-fooling=ts` | Timestamp fooling. Лёгкий, работает на большинстве DPI. |
| `--dpi-desync-fooling=md5sig,badseq` | Жёсткий fooling — фейковая MD5 подпись + плохой sequence. Для упрямых DPI. |
| `--dpi-desync-autottl=2:2-12` | Автоматический TTL для фейков (минимум 2, от 2 до 12). |
| `--dpi-desync-fake-tls=FILE` | Файл с fake TLS ClientHello. В `/opt/zapret/files/fake/`. |
| `--dpi-desync-fake-quic=FILE` | Аналог для QUIC. |

### Порядок попыток если ничего не работает

1. Начни с самой мягкой: `--dpi-desync=split2 --dpi-desync-split-pos=1`
2. Добавь fooling: `--dpi-desync-fooling=ts`
3. Добавь fake: `--dpi-desync=fake,split2 --dpi-desync-fake-tls=...`
4. Усиль split: `--dpi-desync=fake,fakedsplit`
5. Совсем жёстко: `--dpi-desync=fake,multidisorder --dpi-desync-repeats=11 --dpi-desync-fooling=md5sig,badseq`

После каждого изменения: `systemctl restart zapret` и тест.

## Инструменты отладки

- `bol-van/zapret` репа: [Readme](https://github.com/bol-van/zapret/blob/master/docs/readme.md) — там полный список опций
- `nfqws --help` — все параметры с описаниями
- `journalctl -u zapret -f` — живые логи
- `/opt/zapret/files/fake/` — готовые fake TLS/QUIC бинарники от разных сайтов

## Где меняется стратегия

Шаблон: `zapret/zapret.sh` в репе (LAN подставляется installer'ом).
На шлюзе: `/opt/zapret-config/zapret.sh` (рендер из шаблона).

После правки:
```bash
systemctl restart zapret
/opt/zapret-config/zapret.sh status   # проверить что nfqws запустились
```
