# Web Setup Manager — руководство оператора и администратора

Документ описывает актуальную catalog-версию из
[PRODUCT_REQUIREMENTS.ru.md](PRODUCT_REQUIREMENTS.ru.md). Web Setup Manager
работает только внутри одного настроенного LinuxCNC `PROGRAM_PREFIX` и не даёт
произвольный доступ к остальной filesystem. Исторические managed `objects` и
SQLite нельзя изменять вручную во время migration/rollback window.

Статус на 2026-08-22: catalog release
`/opt/websetupmanager/releases/393ddb68a550` развёрнут из source commit
`393ddb68a550eeb5e65cc607032d23d9ab8cc0a1`; binary SHA-256 —
`294549740ffc2255720403c474beae5be01c652a8fddad93d020d4ec7b37bd48`.
Automated suites, frontend 17 files / 197 tests/build, cold backup/restore check,
live migration, integrity/hash, restart и HTTPS health/readiness прошли.
Production headless Firefox desktop/mobile visual и PAM login/logout smoke также
прошли. Сквозной keyboard-only integration flow закрывает login/tree/upload/
preview-search/line-jump/logout. Отдельный LAN client, DHCP reservation,
controlled target performance и ручной visual QtDragon walkthrough ниже
оставлены дополнительными проверками.

Production clean-profile smoke отдельно подтвердил browser cache/manifest,
header progress, пустой Toolpath, построенную Tool Table и улучшенную HTML-вёрстку.

## 1. Каталог и безопасный рабочий цикл

Setup содержит не более одной G-code-программы и не более одной PDF/HTML Setup
Sheet. Он может быть неполным. В текущей версии нет `draft/ready/attention`,
validation или `current setup`: приложение не оценивает технологическую
готовность и не блокирует upload из-за отсутствующей sheet.

На фактическом станке:

```text
LinuxCNC INI:   /home/user/linuxcnc/configs/corvuscnc/g540.ini
PROGRAM_PREFIX: /home/user/linuxcnc/nc_files
QtDragon:       User → <каталог> → <программа>.ngc
```

### Работа оператора

1. Слева раскройте compact tree folders/files или создайте нужный folder.
   Физические folders находятся под `PROGRAM_PREFIX`; `ngcgui_lib` зарезервирован
   LinuxCNC и не используется для пользовательских сетапов.
2. Выберите folder назначения и нажмите «Добавить». Системный file picker
   принимает ровно один `.ngc`, `.nc` или `.tap` G-code и необязательно одну
   PDF/HTML Setup Sheet. Выбор сразу начинает upload без отдельного create
   popup; имя Setup выводится из имени G-code.
3. Если Sheet пока нет, G-code остаётся обычной leaf-строкой; прикрепите Sheet
   позже прямой кнопкой `Setup Sheet`. После этого она появляется дочерней
   строкой под G-code. Вторая программа/sheet означает replace, а не добавление
   composition. Текущий folder назначения виден до picker, точный путь — после
   upload; Backend не принимает absolute path и не выходит выше root.
4. После успеха программа физически опубликована под
   `/home/user/linuxcnc/nc_files/<относительный каталог>/...`. Откройте штатный
   QtDragon file manager, перейдите из `User` по тому же дереву и выберите файл
   вручную.
5. Справа просматривайте выбранную программу с номерами строк, переходом и
   literal-поиском. Один version-bound prefix до 64 КиБ показывает начало до
   фонового Worker index; его фактический progress виден в editor header, а
   дальнейшее чтение остаётся bounded Range/ETag. Вкладка Toolpath пока является
   пустым placeholder. Tool Table показывает bounded lexical summary
   статических `T` и связанных `M6` и умеет вернуться к первой строке
   инструмента, но не заменяет проверку системного tool table. Дочерняя Setup
   Sheet открывается inline в той же editor surface как canvas PDF либо
   очищенный HTML в sandbox, не во всплывающем окне.
6. При version/revision conflict сначала обновите tree/viewer и сравните
   актуальный файл. Приложение не перезаписывает внешнее изменение молча.
7. Удаление/перемещение catalog entity меняет именованный файл/каталог только
   после явного подтверждения. Восстановление удалённых данных возможно из
   согласованной cold backup.

Upload, selection и preview **не открывают и не запускают программу в
LinuxCNC**. Перед обработкой оператор отдельно проверяет станок, оснастку,
tool table, ноль, траекторию и сам G-code. Security-проверки имени, типа,
размера и safe path не являются проверкой корректности обработки.

Для API automation действуют те же file preconditions, что отправляет UI:
create — ровно `If-None-Match: *`, replace/delete — ровно
`If-Match: "<version>"` из актуального catalog DTO. Одновременно обязательны
`expectedRevision`, `Idempotency-Key`, CSRF и session/Bearer authentication; не
подставляйте filename/path вместо opaque Setup ID.

### Browser cache, индекс и derived вкладки

После успешного upload программа сначала атомарно публикуется Backend. Затем
выбранный immutable browser `File` передаётся Web Worker: он строит sparse line
index и Tool Table и, если размер допускает, сохраняет полные chunks без
повторного download. Если файл уже существовал, был добавлен другим browser или
внешним процессом, Worker получает его exact-version Range blocks от Backend.
Первый viewport остаётся приоритетнее полного анализа.

Editor header показывает реальный progress текущего Worker-прохода от 0 до
100%. Прочитанные 100% bytes ещё не означают terminal result: короткое состояние
«Финализация…» отделяет завершение decode/index/Tool Table. После reload UI не
показывает незавершённый сохранённый процент как текущий; готовый analysis
принимается только после отдельного version-bound Worker result. Invalid UTF-8,
Worker failure и version conflict показывают конечную ошибку и retry, а не
бесконечный spinner.

Browser storage имеет следующие фиксированные границы:

- raw bytes сохраняются в origin-private Cache Storage только для одного G-code
  размером до 32 MiB, полными chunks по 1 MiB; общий raw budget приложения —
  128 MiB;
- raw chunks и analysis имеют TTL 30 суток, но browser quota policy может
  удалить их раньше; это cache, а не backup;
- сохраняется не более 48 analysis records, каждый JSON не более 4 MiB;
  `window.localStorage` содержит не raw G-code, а максимум 24 manifest records
  с version-bound progress/completion, line count и Tool Table;
- большой файл не получает полную persistent raw-копию; preview продолжает
  читать Range и держит в памяти не более восьми блоков примерно по 1 MiB;
- Tool Table ограничена 1024 уникальными статическими целочисленными tools
  `T0`–`T999999999`; строка длиннее 65 536 client string units пропускается для
  extraction и даёт явное предупреждение об усечении.

Отдельно от этих 24 preview records browser может хранить до 32 digest-only
auth/cache recovery markers. Они не содержат raw CSRF, cookie или password и
нужны только для fail-closed cleanup/revoke после race либо reload.

Cached identity включает principal/library scope, opaque artifact ID, exact
version и byte size. Поэтому другая версия или учётная запись не получает
старые chunks. При обычном повторном открытии первый prefix проверяется online
через exact ETag; полностью сохранённая точная версия используется при network
failure с заметной offline-пометкой. Не считайте offline copy подтверждением
того, что файл на станке не менялся.

Успешная кнопка «Выйти» сначала блокирует новые writes этого scope во всех
доступных вкладках, затем очищает его Cache Storage и localStorage manifest.
Если logout не удался и session осталась активной, scope снова разрешается.
Password, session cookie, CSRF, Bearer, absolute path и storage key в cache не
записываются, но сам cached G-code остаётся производственными данными. В local
loopback mode отдельной login/logout session нет; на общем компьютере очистите
данные сайта штатной функцией browser profile. Такая очистка удаляет только
browser cache/настройки и не удаляет файл из LinuxCNC `PROGRAM_PREFIX`.

Toolpath ничего не вычисляет и не обращается к LinuxCNC. Tool Table — только
read-only lexical подсказка: комментарии `;`/`(...)`, expressions, named
parameters и O-word labels исключаются; пробелы трактуются по правилам
LinuxCNC. Последнее динамическое или нецелое `T` сбрасывает неизвестный pending
tool. `M6`/`M06` относится только к последнему известному статическому `T` этой
строки либо к известному `T`, перенесённому с предыдущей строки. Таблица не знает фактическую геометрию, offsets,
износ/наличие инструмента или состояние spindle. Перед обработкой обязательно
сверьте штатный LinuxCNC tool table и станок.

HTML Setup Sheet получает нейтральную application-owned экранную/печатную
верстку для таблиц, заголовков и изображений. Исходные `<head>`, `<title>` и
`<style>` намеренно не сохраняются: внешний вид может отличаться от исходного,
зато документ не может перекрыть controls viewer или загрузить CSS/шрифты из
сети. Script, forms, navigation, iframe/object/SVG/MathML active subtrees и event
handlers удаляются; Sheet остаётся в iframe с пустым `sandbox` и отдельной
hash-only CSP. Не ослабляйте эти ограничения ради точного воспроизведения
небезопасного source HTML; для критичной фирменной верстки используйте PDF.

## 2. Установка

Production target — 64-bit Linux amd64/arm64 с filesystem, поддерживающей
atomic rename, `fsync`, sparse files и 64-bit offsets. Production binary
собирается с cgo/PAM и использует системный PAM runtime; Node и внешний сервер
БД на станке не нужны. Для Debian/Ubuntu build host установите C toolchain и
`libpam0g-dev`, а на target — `libpam0g`.

1. Получите release binary из доверенного источника и проверьте опубликованный
   checksum/signature.
2. Установите binary, выберите non-root Linux-пользователя оператора
   с PAM-паролем, state/legacy-library roots и существующий LinuxCNC
   `PROGRAM_PREFIX`. В
   примере это `cncoperator`; в remote mode процесс обязан работать от того же
   пользователя, который задан как `WEB_SETUP_MANAGER_ALLOWED_USER`.
   Встроенного логина/пароля Web Setup Manager не существует. Команды установки
   выполняются администратором; сам сервис root-права отвергает.

```bash
sudo install -o root -g root -m 0755 websetupmanager /usr/local/bin/websetupmanager
sudo useradd --create-home --shell /bin/bash cncoperator
sudo passwd cncoperator
sudo install -d -o cncoperator -g cncoperator -m 0750 /var/lib/websetupmanager
sudo install -d -o cncoperator -g cncoperator -m 0750 /srv/websetupmanager/library
sudo install -d -o cncoperator -g cncoperator -m 0750 /home/cncoperator/linuxcnc/nc_files
sudo install -o root -g root -m 0644 deploy/pam.d/websetupmanager /etc/pam.d/websetupmanager
```

Если учётная запись уже существует, `useradd` не выполняйте; задайте/смените её
пароль штатным `passwd` и проверьте локальную политику блокировки/срока действия.
Не передавайте пароль через env, unit, командную строку или config-файл.
Поставляемая PAM policy содержит `common-auth` и `common-account`:

```text
#%PAM-1.0
@include common-auth
@include common-account
```

На дистрибутивах без Debian-style `common-*` администратор должен создать
эквивалентную policy по правилам этого дистрибутива; ослаблять account checks
нельзя.

`library_dir`, `state_dir` и `PROGRAM_ROOT` обязаны быть absolute real
directories, writable пользователем сервиса и не symlink. `PROGRAM_ROOT` обязан
совпасть с `PROGRAM_PREFIX` active INI. Не используйте `/`, home другого
пользователя или каталог LinuxCNC как **legacy library root**; только отдельный
`PROGRAM_ROOT` намеренно указывает в LinuxCNC tree.

На текущем host `/home/user/linuxcnc/nc_files` обнаружен с mode `0775`. Catalog
Backend намеренно отвергает group/other-writable root, поэтому перед первым
catalog deployment администратор должен проверить владельца/пустоту и сузить
права без изменения содержимого:

```bash
sudo chown user:user /home/user/linuxcnc/nc_files
sudo chmod 0750 /home/user/linuxcnc/nc_files
```

LinuxCNC и Web Setup Manager работают от того же `user`, поэтому owner access
сохраняется. Не применяйте recursive `chmod/chown` к существующей библиотеке
без отдельного manifest/backup.

Пример `/etc/websetupmanager.env` (права `0600`, root-owned):

```text
WEB_SETUP_MANAGER_LIBRARY_DIR=/srv/websetupmanager/library
WEB_SETUP_MANAGER_STATE_DIR=/var/lib/websetupmanager
WEB_SETUP_MANAGER_PROGRAM_ROOT=/home/cncoperator/linuxcnc/nc_files
WEB_SETUP_MANAGER_LINUXCNC_INI=/home/cncoperator/linuxcnc/configs/machine/machine.ini
WEB_SETUP_MANAGER_PROGRAM_ROOT_DISPLAY=~/linuxcnc/nc_files
WEB_SETUP_MANAGER_LISTEN_ADDRESS=127.0.0.1:8080
WEB_SETUP_MANAGER_ARTIFACT_UPLOAD_LIMIT=0
WEB_SETUP_MANAGER_IMPORT_TOTAL_LIMIT=0
```

Это loopback local mode: UI считается локально доверенным и отдельный login не
показывается. Для remote mode используйте пример из раздела 4 и добавьте
`ALLOWED_USER`; один лишь non-loopback bind не включает небезопасный доступ.

Пример `/etc/systemd/system/websetupmanager.service`:

```ini
[Unit]
Description=Web Setup Manager
After=local-fs.target
RequiresMountsFor=/srv/websetupmanager/library /var/lib/websetupmanager /home/cncoperator/linuxcnc/nc_files

[Service]
Type=simple
User=cncoperator
Group=cncoperator
EnvironmentFile=/etc/websetupmanager.env
ExecStart=/usr/local/bin/websetupmanager
Restart=on-failure
RestartSec=3s
TimeoutStopSec=30s
PrivateTmp=true
ProtectSystem=strict
ProtectHome=read-only
ReadWritePaths=/srv/websetupmanager/library /var/lib/websetupmanager /home/cncoperator/linuxcnc/nc_files

[Install]
WantedBy=multi-user.target
```

Для фактического host используйте versioned templates
[websetupmanager.service](deploy/systemd/websetupmanager.service) и
[websetupmanager.env.example](deploy/systemd/websetupmanager.env.example), не
переписывая пути из generic примера вручную.

Эти templates описывают фактическую catalog production model: процесс `user`,
direct TLS на `10.0.1.136:443`, active `g540.ini`, writable только legacy/state
roots и `/home/user/linuxcnc/nc_files`. Текущая release generation установлена;
перед следующей schema/data migration снова выполните cold backup из раздела 5.

`NoNewPrivileges=true` намеренно отсутствует: распространённый `pam_unix`
использует привилегированный helper для безопасной проверки shadow password.
Не добавляйте hardening option без повторного интерактивного PAM smoke от имени
service user.

После изменения unit/env:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now websetupmanager
curl --fail --silent --cacert /path/to/ca.pem https://microb.int/healthz
curl --fail --silent --cacert /path/to/ca.pem https://microb.int/readyz
```

Оба запроса должны вернуть HTTP 200. `/healthz` — liveness процесса;
`/readyz` — SQLite/state, program root и совпадение active INI. Не направляйте
production процесс на каталог тестов, другой `PROGRAM_PREFIX` или другой legacy
library marker. Во время migration readiness не заменяет проверку migration
manifest; выполните отдельный сценарий из [MIGRATION_PLAN.md](MIGRATION_PLAN.md).

Для фактического direct-TLS deployment проверяйте HTTPS, доверяя установленному
CA/certificate, например `curl --cacert <ca.pem> https://microb.int/healthz` и
`.../readyz`. Backend не слушает port 80 и не перенаправляет HTTP: запрос
`http://microb.int/` должен получить connection refused, если отдельный proxy не
настроен. Browser может сам применить HTTPS-first/HSTS; это не server redirect.

## 3. Конфигурация

Backend читает настройки только при старте; API/UI не могут менять physical
roots. Byte limits задаются целым числом байт (`1073741824`, не `1GiB`), duration
— Go-форматом (`30s`, `5m`, `24h`), file mode — octal.

| Переменная | Default | Назначение/ограничение |
|---|---:|---|
| `WEB_SETUP_MANAGER_PROGRAM_ROOT` | обязательно | canonical LinuxCNC `PROGRAM_PREFIX`; единственный writable catalog root |
| `WEB_SETUP_MANAGER_LINUXCNC_INI` | обязательно | active real regular INI; его `PROGRAM_PREFIX` должен совпасть с root |
| `WEB_SETUP_MANAGER_PROGRAM_ROOT_DISPLAY` | `~/linuxcnc/nc_files` | безопасный location hint для оператора |
| `WEB_SETUP_MANAGER_LIBRARY_DIR` | transition: обязательно | legacy objects для migration/rollback, не destination новых upload |
| `WEB_SETUP_MANAGER_STATE_DIR` | XDG/local state | SQLite, staging, indexes |
| `WEB_SETUP_MANAGER_LISTEN_ADDRESS` | `127.0.0.1:8080` | `host:port` |
| `WEB_SETUP_MANAGER_LIBRARY_ALIAS` | `Сетапы` | публичное имя библиотеки |
| `WEB_SETUP_MANAGER_GCODE_EXTENSIONS` | legacy default | catalog allowlist должен соответствовать active INI; текущий host: `.ngc,.nc,.tap` |
| `WEB_SETUP_MANAGER_RECENT_SETUPS_LIMIT` | `30` | `1..1000` |
| `WEB_SETUP_MANAGER_MAX_PARALLEL_HEAVY_JOBS` | `2` | `1..16` |
| `WEB_SETUP_MANAGER_ARTIFACT_UPLOAD_LIMIT` | `0` | максимум одного файла; 0 = без app-limit |
| `WEB_SETUP_MANAGER_IMPORT_TOTAL_LIMIT` | `0` | максимум сессии; 0 = без app-limit |
| `WEB_SETUP_MANAGER_REQUIRE_SETUP_SHEET_FOR_READY` | legacy only | catalog version не имеет ready/validation gate |
| `WEB_SETUP_MANAGER_ARTIFACT_FILE_MODE` | `0640` | execution bits запрещены |
| `WEB_SETUP_MANAGER_SHUTDOWN_TIMEOUT` | `15s` | graceful shutdown budget |
| `WEB_SETUP_MANAGER_READ_HEADER_TIMEOUT` | `10s` | HTTP headers |
| `WEB_SETUP_MANAGER_READ_TIMEOUT` | `30s` | sliding request-read/response-write I/O idle timeout |
| `WEB_SETUP_MANAGER_IDLE_TIMEOUT` | `2m` | keep-alive idle connection |
| `WEB_SETUP_MANAGER_MAX_HEADER_BYTES` | `16384` | `8192..1048576` |
| `WEB_SETUP_MANAGER_IDEMPOTENCY_TTL` | `24h` | replay window |
| `WEB_SETUP_MANAGER_DELETE_CONFIRMATION_TTL` | `5m` | irreversible-delete token |
| `WEB_SETUP_MANAGER_RECONCILE_INTERVAL` | `1m` | identity scan/cleanup/GC cadence; full SHA scrub также после старта и раз в сутки |
| `WEB_SETUP_MANAGER_IMPORT_SESSION_EXPIRY` | `24h` | abandoned staging lifetime |
| `WEB_SETUP_MANAGER_REMOTE_ACCESS` | `false` | включает browser login/auth policy; обязателен для non-loopback |
| `WEB_SETUP_MANAGER_ALLOWED_USER` | нет | обязательный non-root Linux user remote mode; должен совпадать с process user |
| `WEB_SETUP_MANAGER_PAM_SERVICE` | `websetupmanager` | имя root-owned policy в `/etc/pam.d` |
| `WEB_SETUP_MANAGER_AUTH_IDLE_TIMEOUT` | `30m` | idle lifetime обычной browser session |
| `WEB_SETUP_MANAGER_AUTH_ABSOLUTE_TIMEOUT` | `12h` | максимальная lifetime обычной browser session; не меньше idle |
| `WEB_SETUP_MANAGER_AUTH_REMEMBER_TIMEOUT` | `720h` | fixed lifetime «Запомнить меня» |
| `WEB_SETUP_MANAGER_AUTH_CONCURRENCY` | `4` | максимум одновременных PAM exchanges, `1..64` |
| `WEB_SETUP_MANAGER_LOGIN_ATTEMPTS` | `5` | лимит ошибок на IP и submitted username, `1..100` |
| `WEB_SETUP_MANAGER_LOGIN_WINDOW` | `10m` | окно login throttling |
| `WEB_SETUP_MANAGER_AUTH_SESSION_CAPACITY` | `128` | общий лимит sessions, `1..10000` |
| `WEB_SETUP_MANAGER_REMOTE_AUTH_TOKEN` | нет | optional Bearer для automation; если задан, минимум 32 символа |
| `WEB_SETUP_MANAGER_TLS_CERT_FILE` | нет | real regular PEM file, не symlink |
| `WEB_SETUP_MANAGER_TLS_KEY_FILE` | нет | real regular PEM file, не symlink |
| `WEB_SETUP_MANAGER_TRUSTED_TLS_PROXY` | `false` | proxy termination acknowledgment |

`WEB_SETUP_MANAGER_READ_TIMEOUT` не ограничивает общую длительность большого
upload или download: read/write deadline сдвигается после каждого блока request
body и response. Передача с прогрессом продолжается; stalled upload или клиент,
который перестал читать response, освобождается после timeout. Absolute
`http.Server.ReadTimeout` выключен, а `READ_HEADER_TIMEOUT` применяется отдельно.

## 4. TLS и аутентификация

Loopback — рекомендуемый kiosk mode. Для любого non-loopback listen startup
fail-closed, если одновременно не настроены:

1. `WEB_SETUP_MANAGER_REMOTE_ACCESS=true`;
2. существующий non-root `WEB_SETUP_MANAGER_ALLOWED_USER`, совпадающий с
   effective process user;
3. PAM-capable production binary и рабочая policy
   `/etc/pam.d/websetupmanager` (либо имя из `PAM_SERVICE`);
4. пара TLS cert/key либо `WEB_SETUP_MANAGER_TRUSTED_TLS_PROXY=true`.

Прямой TLS:

```text
WEB_SETUP_MANAGER_LISTEN_ADDRESS=192.0.2.10:8443
WEB_SETUP_MANAGER_REMOTE_ACCESS=true
WEB_SETUP_MANAGER_ALLOWED_USER=cncoperator
WEB_SETUP_MANAGER_PAM_SERVICE=websetupmanager
WEB_SETUP_MANAGER_TLS_CERT_FILE=/etc/websetupmanager/tls.crt
WEB_SETUP_MANAGER_TLS_KEY_FILE=/etc/websetupmanager/tls.key
```

Backend принимает только TLS 1.3+. Cert/key должны быть regular files и не
symlink; key ограничьте `0640` и группой сервиса. Откройте HTTPS URL: публичные
SPA assets и `GET /api/v1/auth/session` позволят показать форму входа. Введите
точно `ALLOWED_USER` и его Linux/PAM password. Backend всегда выполняет PAM
authentication и account-management check только для настроенной учётной
записи; неизвестное имя получает ту же общую ошибку и не выбирает другой OS
account. Login защищён лимитами по client IP и submitted username.

На фактическом host адрес — `https://microb.int/`, configured account — `user`.
Используется Linux/PAM password этого account. Отдельного application login или
предустановленного Web Setup Manager password нет; password нельзя помещать в
env, unit, URL, shell history или support log. Catalog binary должен пройти этот
smoke заново после deployment — исторический PAM smoke старой версии не является
его заменой.

Успешный login создаёт opaque cookie
`__Host-websetupmanager_session` с `Secure`, `HttpOnly`, `SameSite=Strict` и
`Path=/`. Обычная session ограничена idle и absolute timeout. Флажок
«Запомнить меня» задаёт fixed remember deadline и сохраняет в SQLite только
SHA-256 hash случайного cookie token, CSRF token, username, timestamps и
deployment scope; password и raw cookie token не сохраняются. Logout отзывает
эту session. Каждая browser mutation требует точные HTTPS `Host`/`Origin` и
индивидуальный session CSRF token. Local loopback mode остаётся implicit и
использует startup CSRF без login.

Remote UI не монтирует workspace сразу после ответа login. Сначала browser
записывает bounded digest-only quarantine marker, затем точный cookie+CSRF
активирует provisional server session через `POST /api/v1/auth/activate`,
получает capabilities и подтверждает cache generation; только затем exact
marker финализируется. Activation endpoint не меняет cookie, а remembered
activation записывается в SQLite. Поэтому закрытие browser/server сразу после
login не превращает незавершённую session в аутентифицированную после restart.
Если Cache Storage и localStorage недоступны либо continuation стала stale, UI
условно отзывает только эту session и возвращается к форме входа. Сообщение
«Браузер не смог надёжно защитить новую сессию» означает fail-closed browser
storage condition, а не неверный PAM password: разрешите origin-private site
storage либо используйте управляемый browser profile и повторите вход.

При crash/reload после не подтверждённого revoke hash-only marker заставляет
следующий session probe повторить exact revocation. Journal ограничен 32
markers и не содержит raw CSRF/cookie/password. Не очищайте отдельные journal
keys вручную, чтобы «починить» вход: штатная очистка всех site data допустима
только как осознанный сброс browser cache/session на этом client.

HTTP Basic не поддерживается и browser password prompt не используется. Для
неинтерактивной automation можно отдельно задать случайный
`WEB_SETUP_MANAGER_REMOTE_AUTH_TOKEN` длиной минимум 32 символа и отправлять
`Authorization: Bearer <token>`; это необязательный второй credential path, а
не пароль формы. Не помещайте Bearer secret в URL, shell history, proxy access
log или frontend bundle; ротация требует рестарта.

Self-signed certificate шифрует transport, но browser не считает его доверенным
автоматически. Установите certificate/его CA в доверенное хранилище управляемых
clients либо используйте certificate от принятой локальной/public CA; не
приучайте оператора постоянно обходить TLS warning.

При TLS termination proxy оставьте Backend по возможности на отдельном
loopback/Unix-isolated network и ограничьте доступ firewall. Trusted flag лишь
подтверждает deployment policy: он не проверяет proxy сам. Proxy обязан
отбрасывать client-supplied forwarding/auth headers, передавать session cookie
и optional Bearer, сохранять внешний `Host` и обеспечивать точное совпадение
HTTPS `Origin` с ним для login/logout/mutation checks. Сначала проверьте guest
session response, login, capabilities, upload disposable G-code в отдельный
тестовый folder и logout; не
отключайте Host/Origin/CSRF проверки.

## 5. Резервное копирование

Самый безопасный backup — **cold copy state, legacy library и всего program
root как одной generation**. Не копируйте только `websetupmanager.sqlite3`:
catalog rows ссылаются на именованные файлы, а migration history — на legacy
objects/library marker. Не используйте catalog API как backup API.

1. Убедитесь, что upload/move/delete завершены или отменены.
2. Остановите сервис и дождитесь clean exit.
3. Скопируйте целиком `state_dir`, legacy `library_dir`, `PROGRAM_ROOT`, active
   INI и service config в отдельный versioned каталог, сохранив ownership,
   permissions, xattrs и timestamps.
4. Проверьте checksum/manifest каждого regular file и отдельно перечислите
   symlink/special entries; затем запустите сервис и проверьте readiness.

Перед первым запуском catalog binary этот backup обязателен: startup сам
выполняет legacy→catalog migration до listener. Не запускайте новый binary на
единственной копии state/library/program-root и не продолжайте при
`manual_review`.

```bash
sudo systemctl stop websetupmanager
sudo rsync -aHAX --numeric-ids /var/lib/websetupmanager/ /backup/wsm-2026-08-21/state/
sudo rsync -aHAX --numeric-ids /srv/websetupmanager/library/ /backup/wsm-2026-08-21/library/
sudo rsync -aHAX --numeric-ids /home/cncoperator/linuxcnc/nc_files/ /backup/wsm-2026-08-21/program-root/
sudo install -D -m 0640 /home/cncoperator/linuxcnc/configs/machine/machine.ini /backup/wsm-2026-08-21/config/machine.ini
sudo systemctl start websetupmanager
curl --fail --silent http://127.0.0.1:8080/readyz
```

Храните backup на другом носителе и защищайте как производственный G-code.
SQLite online backup используется самим migration layer перед необратимой
миграцией, но отдельного публичного operator CLI для online full-library backup
нет; поэтому hot copy не документируется как согласованная процедура.
Origin-private browser cache не входит в cold generation и не используется для
восстановления: он может быть очищен без изменения catalog/program files.
State backup содержит hash-only remembered-session rows. Защищайте его как
authentication state: password/raw cookie там нет, но matching browser token и
тот же deployment scope могут восстановить session после cold restore.

## 6. Восстановление

1. Остановите Backend и сохраните повреждённые roots отдельно для анализа.
2. Восстанавливайте matching `state`, `library` и `program-root` из одной backup
   generation. Не смешивайте DB одной generation с program files/objects другой.
3. Восстановите владельца/права, убедитесь, что roots и TLS files не symlink.
4. Проверьте, что restored root совпадает с `PROGRAM_PREFIX` active INI, затем
   запустите сервис. Startup выполняет SQLite `quick_check`, checksummed schema
   migrations, legacy journal/import recovery, legacy identity inspection,
   catalog operation recovery и idempotent legacy→catalog migration.
5. Проверьте `/readyz`, дерево folders, один G-code Range preview, Setup Sheet и
   фактический файл в QtDragon. При копировании inode/ctime меняются; Backend
   принимает новую physical identity только после совпадения ожидаемого полного
   SHA-256. Несовпавший или отсутствующий файл остаётся явным conflict и не
   заменяется объектом с тем же именем.

```bash
sudo systemctl stop websetupmanager
sudo mv /var/lib/websetupmanager /var/lib/websetupmanager.failed
sudo mv /srv/websetupmanager/library /srv/websetupmanager/library.failed
sudo mv /home/cncoperator/linuxcnc/nc_files /home/cncoperator/linuxcnc/nc_files.failed
sudo install -d -o cncoperator -g cncoperator -m 0750 /var/lib/websetupmanager /srv/websetupmanager/library /home/cncoperator/linuxcnc/nc_files
sudo rsync -aHAX --numeric-ids /backup/wsm-2026-08-21/state/ /var/lib/websetupmanager/
sudo rsync -aHAX --numeric-ids /backup/wsm-2026-08-21/library/ /srv/websetupmanager/library/
sudo rsync -aHAX --numeric-ids /backup/wsm-2026-08-21/program-root/ /home/cncoperator/linuxcnc/nc_files/
sudo chown -R cncoperator:cncoperator /var/lib/websetupmanager /srv/websetupmanager/library /home/cncoperator/linuxcnc/nc_files
sudo systemctl start websetupmanager
```

Если `quick_check`, migration checksum, library fingerprint или process lock не
проходит, сервис намеренно остаётся unready/не запускается. Не удаляйте БД, lock
или marker и не создавайте пустую БД поверх старой; верните последний проверенный
backup либо передайте сохранённые roots разработчику.

## 7. Recovery, reconciliation и GC

Startup до listener удаляет только собственные безопасно распознанные staging
остатки и строго выполняет: legacy operation recovery → legacy import recovery →
legacy content inspection → catalog operation recovery → legacy catalog
migration. Любая ошибка и persisted `manual_review` блокируют listener. Completed
per-source mapping повторно сверяется с source artifact/object provenance,
manifest, catalog linkage и physical identity при возобновлении общего
`pending`/`running` run; общий terminal `completed` затем является startup
no-op. Migration-owned folder привязан unique source key. Неизвестный same-name
folder не усваивается автоматически.

Startup не удаляет неизвестные files/folders под `PROGRAM_ROOT`. Periodic
reconcile сравнивает ожидаемую relative path и file identity; дорогой digest
нужен при неоднозначной смене identity, а не для validation G-code.

Новые upload создают hidden exclusive temp в target filesystem, синхронизируют
bytes и публикуют atomic rename. До rename конечное имя отсутствует/содержит
старую полную версию; после rename наблюдается новая полная версия. Crash до
SQLite commit при restart приводит к deterministic recovery/conflict, а не к
тихому success. Повторите операцию только после проверки tree, relative path и
фактических bytes.

Legacy `objects/*`, `.object-staging`, `staging`, `indexes` и старые rows
сохраняются до завершения [MIGRATION_PLAN.md](MIGRATION_PLAN.md). Legacy GC не
должен удалять object, упомянутый migration manifest/rollback generation. Не
запускайте ручной cleanup этих каталогов.

## 8. Логи и аудит

Backend пишет newline-delimited JSON в stderr. Под systemd:

```bash
journalctl -u websetupmanager --since today --output=cat
journalctl -u websetupmanager -p warning..alert --output=cat
journalctl -u websetupmanager -f --output=cat
```

HTTP записи содержат безопасные request/entity/job IDs, route, status,
duration, bytes и stable error code. Catalog folder/setup create/rename/move/
delete и program/sheet add/replace/delete имеют audit events в SQLite. Legacy
import/validation/current/archive events могут оставаться только историей.
Логи не должны содержать password, raw session cookie, Bearer token, G-code,
SQL, storage key или absolute path.
Если это обнаружено, ограничьте доступ к журналу, сохраните forensic copy и
ротируйте secret. Не вставляйте пользовательское содержимое в командную строку.

## 9. Upgrade и rollback

1. Прочитайте release notes и убедитесь, что target architecture поддержан.
2. Сделайте проверенный cold backup всей generation: state, legacy library и
   program root.
3. Остановите сервис; сохраните старый binary.
4. Атомарно установите новый binary и запустите сервис.
5. Проверьте health/readiness, logs, catalog tree, destination, preview и
   тестовый no-replace upload.

```bash
sudo systemctl stop websetupmanager
sudo cp -a /usr/local/bin/websetupmanager /usr/local/bin/websetupmanager.previous
sudo install -o root -g root -m 0755 websetupmanager.new /usr/local/bin/websetupmanager
sudo systemctl start websetupmanager
curl --fail --silent http://127.0.0.1:8080/readyz
```

Migrations embedded, checksummed и выполняются до ready. Автоматического
downgrade нет. Если новая версия успела применить несовместимую migration,
нельзя просто вернуть старый binary: остановите сервис и восстановите всю
generation из pre-upgrade backup. Старый binary без restore допустим только когда release
notes явно подтверждают schema compatibility.

Для первого catalog rollout старый binary, unit/env и cold generation должны
оставаться доступными до проверки migration manifest, browser/PAM smoke и
QtDragon lookup. Rollback восстанавливает вместе state, legacy library и
program root; частичный откат одной SQLite или только binary запрещён.

## 10. Incident runbook

| Симптом | Безопасное действие |
|---|---|
| `/healthz` недоступен | `systemctl status`, logs, unit/env, порт; не удалять данные |
| health 200, ready не 200 | проверить mount/права/free space/SQLite logs; остановить при повторе |
| remote startup сообщает `AUTHENTICATION_UNAVAILABLE` | проверить PAM production build/libpam, `/etc/pam.d/websetupmanager` и account status; не включать обход login |
| startup сообщает account mismatch | сделать `User=`/`Group=` unit равными `ALLOWED_USER`; не запускать от root или другого account |
| login отклонён/ограничен | проверить точное имя `ALLOWED_USER`, Linux password/account policy, HTTPS Origin и дождаться throttle window; не логировать password |
| UI сообщает, что browser не смог защитить новую session | проверить, что site storage/Cache Storage разрешены и не исчерпана quota; повторить вход в управляемом profile; не отключать conditional revoke и не переносить CSRF в обычное storage |
| после crash login снова возвращается к форме | дождаться exact stale-session revoke и повторить вход; при повторении собрать status/request ID без cookie/CSRF/password и проверить browser storage/PAM logs |
| startup сообщает второй процесс | найти владельца unit/lock; не удалять lock при живом процессе |
| upload/download завис | проверить network/disk; отменить request/job; sliding I/O idle timeout завершит stall/slow client |
| `INSUFFICIENT_STORAGE` | освободить место вне `PROGRAM_ROOT`/state или расширить FS; повторить operation |
| program/sheet показывает conflict | обновить tree/viewer, сравнить relative path/version и только затем явно replace |
| HTML upload вернул `INVALID_CONTENT` | проверить standalone UTF-8 HTML, завершённый stream и bounded nesting; разбить text/attribute token >1 MiB на bounded elements; не отключать sanitizer |
| revision conflict | обновить tree/setup, проверить новое имя/место/состав, повторить сохранённый ввод |
| corrupt/migration checksum | остановить, сохранить roots, восстановить matching backup; не пересоздавать DB |
| после crash job имеет `PROCESS_INTERRUPTED`/`conflict` | сверить setup revision, terminal job и audit; повторить с новым key только незавершённую operation; при сомнении остановить сервис и restore backup |
| подозрение на symlink/FIFO/socket | остановить, сохранить forensic copy; не открывать объект shell-инструментом от root |
| optional Bearer раскрыт | сменить secret в root-only env, рестарт, очистить proxy/journal exposure, проверить audit |
| browser session скомпрометирована | выполнить logout в доступной session; при недоступности временно остановить remote endpoint и провести credential/session incident procedure до возобновления |
| TLS certificate истекает | установить новую regular-file пару, проверить ownership, controlled restart |

После любого восстановления запишите время, версию binary, backup generation,
результат readiness, проверенные setup IDs/relative paths и один фактический
QtDragon lookup. Readiness не заменяет просмотр программы оператором.

## 11. Границы production qualification

Новая catalog automation покрывает direct named-file publish, folder/setup CRUD,
singular components, exact HTTP preconditions, path/race substitution, durable
catalog journal, настоящий subprocess SIGKILL, sparse 10 GiB metadata/tail Range,
0/1/N migration, sheet fan-out, completed provenance и collision/manual-review.
Frontend lint/typecheck, 17 files / 197 tests и Vite production build прошли;
полный keyboard-only integration flow и component focus regressions включены.
Production visual smoke отдельно подтверждает реальный layout.

До смены product direction PAM integration, обычные/PAM-tagged Go suites,
race/vet прошли; отдельный
non-PAM remote binary подтвердил fail-closed
`AUTHENTICATION_UNAVAILABLE`. Frontend lint/typecheck, 17 files/197 tests и Vite
production build прошли; baseline `scripts/build.sh` прошёл целиком, а для
финального focus-only release все gates повторены отдельно и clean detached
worktree выполнил `npm ci`/Vite/PAM Go build. Production binary
собран с tags `production,pam`. На текущем amd64 host (2026-08-22)
catalog `websetupmanager.service` enabled/active от Linux-пользователя `user` и
слушает `https://microb.int:443`. Live direct-TLS проверка зафиксировала
health/ready 200 и отсутствие listener на port 80. Установлена release
`/opt/websetupmanager/releases/393ddb68a550` из commit
`393ddb68a550eeb5e65cc607032d23d9ab8cc0a1`; SHA-256 binary:
`294549740ffc2255720403c474beae5be01c652a8fddad93d020d4ec7b37bd48`.
Remote login использует PAM account `user` и текущий системный Linux password;
его значение нельзя записывать в этот документ, env или командную строку.
Optional Bearer в production env не задан.
Текущий certificate self-signed с SAN `microb.int` и `10.0.1.136`; его доверие
на управляемом browser/client остаётся отдельным deployment действием.

Headless Firefox ESR через WebDriver BiDi прошёл production HTTPS guest/login,
PAM session, ready/catalog/UI и logout. Desktop `1366x768` и mobile `390x844`
screenshots находятся в `/tmp/wsm-catalog-evidence.ZSP7Ft`. Первый run выявил
нулевую высоту code viewport; release commit
`18411e613b380c4b73837003b96c949a21661041` заменил editor grid→flex, а повторный
run визуально подтвердил подсвеченный G-code и tree. Desktop assert: 37 virtual
rows, первая `%`, viewport `1030x516` для G-code 1.7 MiB.

Следующий commit `12aa6a2adf3c9908a2120c03ed310aa40ac1fecc` исправил
focus return до portal `autoFocus` и remount line-jump input. Сквозной test
прошёл только keyboard events через login, tree, upload, preview search/line
jump и logout; после установки release service снова active с `NRestarts=0`,
`/healthz=200`, `/readyz=200` и guest PAM contract.

Refinement release `266917d3ed04` прошла новый production PAM smoke после
deploy: login/catalog/logout, 37 virtual rows, первая `%`, viewport `1030x625`,
desktop/mobile screenshots. Отдельный disposable Firefox smoke подтвердил
реально видимый inline HTML Sheet, mobile `inert`, Tab wrap и focus return.

Workbench release `393ddb68a550` прошла повторный production PAM smoke:
clean profile сохранил 2 raw chunks, 1 analysis и 1 complete manifest, header
показал `Индекс 100%`, Toolpath остался пустым, Tool Table построила T1/T8/T10,
а readable HTML Sheet отрисовалась в sandboxed blob iframe `1028x623`.

Read-only QtDragon audit подтвердил running `g540.ini`, local
`_CORVUS_FILE_MANAGER`, совпадающий `PROGRAM_PREFIX` и доступную его
`QFileSystemModel` цепочку `linuxcnc/nc_files/Импортировано/adssad` со строками
`1002.ngc` и `1003.ngc`. Вкладка File не открывалась и LinuxCNC selection не
менялся; ручной screenshot скрытой вкладки остаётся только дополнительным
operator walkthrough.

Cold generation `/var/backups/websetupmanager/pre-catalog-20260821T145214Z`
прошла проверку всех четырёх archive SHA-256 и полный extract/diff
`RESTORE_CHECK_OK`. Live migration завершила schema v4 и catalog state:
2 folders, 2 setups, 4 files, 2 completed mappings, 4 copied manifests;
SQLite integrity/FK, exact source/target hashes/sizes, отсутствие temp remnants,
сохранность legacy data и idempotent restart проверены. LinuxCNC snapshot не
изменился: `file=""`, `state/mode/interp/exec=1/1/1/2`.

До признания target qualification завершённой отдельно выполните:

- arm64 runtime, если целевая архитектура arm64;
- cold start/RSS/CPU/stream memory/10k-library/first-viewport measurements;
- controlled 10 GiB preview/search performance и malicious PDF/HTML
  network/console walkthrough;
- trusted reverse-proxy variant, если он будет использоваться, и provisioning
  доверия к выбранному production certificate на browser clients;
- полный controlled-browser PAM walkthrough, включая expiry/throttling и
  optional Bearer deployment только если automation credential будет включён;
- повторный cold backup/restore drill перед следующей несовместимой migration;
- SIGKILL/power-loss fault drill с последующей recovery-проверкой.

## 12. Запись финального deployment evidence

Зафиксированное evidence текущей generation:

| Evidence | Результат 2026-08-22 |
|---|---|
| Release | `/opt/websetupmanager/releases/393ddb68a550`; source `393ddb68a550eeb5e65cc607032d23d9ab8cc0a1`; SHA-256 `294549740ffc2255720403c474beae5be01c652a8fddad93d020d4ec7b37bd48` |
| Cold generation | `/var/backups/websetupmanager/pre-workbench-393ddb68a550-20260822T125008Z`; state/library/program-root archive hashes matched; prior restore-qualified generation retained |
| Deployment | enabled/active unit, `User=user`, direct HTTPS `10.0.1.136:443`; TCP/80 absent |
| Migration | schema v5/completed; migration 005 automatic pre-v5 SQLite backup present; catalog/legacy content retained |
| Runtime | `/healthz=200`, `/readyz=200`; SQLite `quick_check=ok`, FK violations 0; temp remnants absent |
| Authentication | Linux/PAM account `user`, current system password (value never recorded); optional Bearer unset |
| Browser | production Firefox BiDi guest/PAM login/logout; 37 G-code rows; header `Индекс 100%`; Cache Storage 2 chunks + 1 analysis; localStorage 1 complete manifest; empty Toolpath; Tool Table T1/T8/T10; HTML sandboxed blob iframe `1028x623` |
| Screenshot SHA-256 | login desktop/mobile `fbf1e313ec372d6f87473860a8e87263c4682868e0357a4903426088d2087773` / `cdb6610b62b4c4bcd4812efa50d6ebf13e81a278cb8616ecc1cb259db368f0ae`; Tool Table / readable HTML Sheet `9ba5d3a313a7fdf024881e375170687128b85f86828f30cff6c04b4181b62a08` / `dda443d4edd71573740d70c2214cf9ce5d03ab2d2debc6f5d96aa5c0422a30fd` |
| LinuxCNC | stat unchanged: `file=""`, `state/mode/interp/exec=1/1/1/2`; read-only Qt model shows `1002.ngc`/`1003.ngc` under the configured root; hidden-tab screenshot remains optional |
| Remaining qualification | LAN client, DHCP reservation, controlled target/browser performance and manual visual QtDragon walkthrough; not part of current `CAT-AC-12` |
