# Web Setup Manager — руководство оператора и администратора

Документ относится к первой production-версии. Web Setup Manager управляет
сетапами, а не произвольными файлами. Никогда не изменяйте внутренние
`objects`, `staging`, SQLite или library marker вручную при запущенном сервисе.

## 1. Состояния и безопасный рабочий цикл

- `draft` — новый или изменённый сетап; его нужно проверить;
- `ready` — конкретная revision прошла встроенные проверки текста/доступности;
- `attention` — содержимое исчезло, повреждено или изменено извне;
- `archived` — сетап скрыт из рабочего списка, но сохранён для восстановления.

`ready` не означает, что LinuxCNC проверил траекторию или безопасность обработки.
Выбор текущего сетапа только закрепляет ссылку в UI/audit. Он ничего не запускает
и не копирует. Нормальный цикл: создать/импортировать → проверить → изучить
G-code и Setup Sheet → явно подтвердить current. После изменения состава снова
выполните validation.

### Работа оператора

1. На экране библиотеки используйте поиск, фильтры и сортировку по сетапам.
   Кнопка «Создать сетап» создаёт пустой `draft`; мастер импорта принимает сразу
   несколько G-code и не более одной общей PDF/HTML Setup Sheet. Перед загрузкой
   проверьте предложенные basename/роли и устраните показанные совпадения имён.
2. Во время импорта и других долгих операций следите за persistent job и
   byte/item progress. Операцию можно отменить. Для частично загруженного
   импорта явно выберите: повторить ошибочные файлы, исключить их, сохранить
   подтверждённый частичный `draft` или отменить всю сессию.
3. В карточке задайте основную программу, при необходимости переименуйте,
   замените или удалите программы и Setup Sheet. Изменения выполняются для
   показанной revision; при конфликте сначала загрузите актуальную карточку и
   проверьте сохранённый ввод. Любое изменение готового сетапа снова требует
   validation.
4. Запустите validation и исправьте перечисленные причины неготовности. Только
   успешная проверка неизменившейся revision переводит сетап в `ready`.
   `attention` нельзя снимать редактированием метаданных: проверьте/замените
   изменённый artifact и повторите полную проверку.
5. Просматривайте G-code с номерами строк, переходом и literal-поиском; большие
   программы загружаются Range-блоками. Setup Sheet открывается как canvas PDF
   либо очищенный HTML в sandbox. Сообщение об изменившейся версии означает,
   что нужно закрыть viewer, обновить карточку и проверить содержимое заново.
   HTML любого допустимого общего размера читается потоково; один структурный
   token больше 1 MiB отклоняется до публикации с `INVALID_CONTENT`.
6. «Выбрать текущим» требует явного подтверждения и только сохраняет ссылку:
   LinuxCNC и G-code не запускаются. Перед обработкой оператор отдельно
   проверяет станок, оснастку, ноль, траекторию и применимую программу.
7. Обычное удаление — архивирование. Архив можно восстановить; если содержимое
   изменилось, он вернётся с `attention`. Необратимое удаление требует сначала
   отдельный delete-plan/token, затем точный ввод отображаемого имени. После
   подтверждения восстановление возможно только из согласованной cold backup.

## 2. Установка

Production target — 64-bit Linux amd64/arm64 с filesystem, поддерживающей
atomic rename, `fsync`, sparse files и 64-bit offsets. Production binary
собирается с cgo/PAM и использует системный PAM runtime; Node и внешний сервер
БД на станке не нужны. Для Debian/Ubuntu build host установите C toolchain и
`libpam0g-dev`, а на target — `libpam0g`.

1. Получите release binary из доверенного источника и проверьте опубликованный
   checksum/signature.
2. Установите binary, выберите отдельного non-root Linux-пользователя оператора
   с PAM-паролем и создайте два существующих непересекающихся каталога. В
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

`library_dir` и `state_dir` обязаны быть absolute real directories, writable
пользователем сервиса, не symlink, не совпадать и не быть вложенными друг в
друга. Не используйте `/`, home другого пользователя или каталог LinuxCNC как
root библиотеки.

Пример `/etc/websetupmanager.env` (права `0600`, root-owned):

```text
WEB_SETUP_MANAGER_LIBRARY_DIR=/srv/websetupmanager/library
WEB_SETUP_MANAGER_STATE_DIR=/var/lib/websetupmanager
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
RequiresMountsFor=/srv/websetupmanager/library /var/lib/websetupmanager

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
ProtectHome=true
ReadWritePaths=/srv/websetupmanager/library /var/lib/websetupmanager

[Install]
WantedBy=multi-user.target
```

`NoNewPrivileges=true` намеренно отсутствует: распространённый `pam_unix`
использует привилегированный helper для безопасной проверки shadow password.
Не добавляйте hardening option без повторного интерактивного PAM smoke от имени
service user.

После изменения unit/env:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now websetupmanager
curl --fail --silent http://127.0.0.1:8080/healthz
curl --fail --silent http://127.0.0.1:8080/readyz
```

Оба запроса должны вернуть HTTP 200. `/healthz` — liveness процесса;
`/readyz` — SQLite и оба удерживаемых storage roots. Не направляйте production
процесс на каталоги тестов или другой library marker. Readiness не ожидает
первый background SHA-256/cleanup/GC cycle; для maintenance/restore дождитесь
JSON-log `operation=reconcile,result=succeeded`.

## 3. Конфигурация

Backend читает настройки только при старте; API/UI не могут менять physical
roots. Byte limits задаются целым числом байт (`1073741824`, не `1GiB`), duration
— Go-форматом (`30s`, `5m`, `24h`), file mode — octal.

| Переменная | Default | Назначение/ограничение |
|---|---:|---|
| `WEB_SETUP_MANAGER_LIBRARY_DIR` | обязательно | managed immutable content |
| `WEB_SETUP_MANAGER_STATE_DIR` | XDG/local state | SQLite, staging, indexes |
| `WEB_SETUP_MANAGER_LISTEN_ADDRESS` | `127.0.0.1:8080` | `host:port` |
| `WEB_SETUP_MANAGER_LIBRARY_ALIAS` | `Сетапы` | публичное имя библиотеки |
| `WEB_SETUP_MANAGER_GCODE_EXTENSIONS` | `.gcode,.nc,.ngc,.tap,.cnc` | comma-separated hints |
| `WEB_SETUP_MANAGER_RECENT_SETUPS_LIMIT` | `30` | `1..1000` |
| `WEB_SETUP_MANAGER_MAX_PARALLEL_HEAVY_JOBS` | `2` | `1..16` |
| `WEB_SETUP_MANAGER_ARTIFACT_UPLOAD_LIMIT` | `0` | максимум одного файла; 0 = без app-limit |
| `WEB_SETUP_MANAGER_IMPORT_TOTAL_LIMIT` | `0` | максимум сессии; 0 = без app-limit |
| `WEB_SETUP_MANAGER_REQUIRE_SETUP_SHEET_FOR_READY` | `false` | sheet блокирует ready при `true` |
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

Успешный login создаёт opaque cookie
`__Host-websetupmanager_session` с `Secure`, `HttpOnly`, `SameSite=Strict` и
`Path=/`. Обычная session ограничена idle и absolute timeout. Флажок
«Запомнить меня» задаёт fixed remember deadline и сохраняет в SQLite только
SHA-256 hash случайного cookie token, CSRF token, username, timestamps и
deployment scope; password и raw cookie token не сохраняются. Logout отзывает
эту session. Каждая browser mutation требует точные HTTPS `Host`/`Origin` и
индивидуальный session CSRF token. Local loopback mode остаётся implicit и
использует startup CSRF без login.

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
session response, login, capabilities, тестовую draft mutation и logout; не
отключайте Host/Origin/CSRF проверки.

## 5. Резервное копирование

Самый безопасный P0 backup — **cold copy двух roots как одной версии**. Не
копируйте только `websetupmanager.sqlite3`: база ссылается на objects и library
identity marker. Не используйте файловый API приложения как backup API.

1. Убедитесь, что jobs завершены или отменены.
2. Остановите сервис и дождитесь clean exit.
3. Скопируйте целиком `state_dir` и `library_dir` в отдельный versioned каталог,
   сохранив ownership, permissions и timestamps.
4. Проверьте backup checksum/manifest и возможность чтения; затем запустите
   сервис и проверьте readiness.

```bash
sudo systemctl stop websetupmanager
sudo rsync -aHAX --numeric-ids /var/lib/websetupmanager/ /backup/wsm-2026-08-20/state/
sudo rsync -aHAX --numeric-ids /srv/websetupmanager/library/ /backup/wsm-2026-08-20/library/
sudo systemctl start websetupmanager
curl --fail --silent http://127.0.0.1:8080/readyz
```

Храните backup на другом носителе и защищайте как производственный G-code.
SQLite online backup используется самим migration layer перед необратимой
миграцией, но отдельного публичного operator CLI для online full-library backup
нет; поэтому hot copy не документируется как согласованная процедура.
State backup содержит hash-only remembered-session rows. Защищайте его как
authentication state: password/raw cookie там нет, но matching browser token и
тот же deployment scope могут восстановить session после cold restore.

## 6. Восстановление

1. Остановите Backend и сохраните повреждённые roots отдельно для анализа.
2. Восстанавливайте matching `state` и `library` из одного backup generation в
   пустые disjoint каталоги. Не смешивайте DB от одного backup с objects другого.
3. Восстановите владельца/права, убедитесь, что roots и TLS files не symlink.
4. Запустите сервис. До открытия listener он выполнит SQLite `quick_check`,
   migrations, import/journal recovery и bounded identity-проверку managed
   ссылок. Сразу после listener в фоне запускаются полная SHA-256 сверка,
   expired cleanup и reference-safe GC; они не блокируют `/healthz`.
5. Проверьте `/readyz`, библиотеку, current setup, один G-code Range preview и
   Setup Sheet. При копировании в новые каталоги inode/ctime закономерно
   меняются: первый background reconcile повторно привязывает объект только
   после совпадения полного SHA-256 и оставляет затронутый сетап `attention`.
   Дождитесь JSON-log `operation=reconcile,result=succeeded`, затем выполните
   validation; несовпавшие или отсутствующие bytes не принимаются.

```bash
sudo systemctl stop websetupmanager
sudo mv /var/lib/websetupmanager /var/lib/websetupmanager.failed
sudo mv /srv/websetupmanager/library /srv/websetupmanager/library.failed
sudo install -d -o cncoperator -g cncoperator -m 0750 /var/lib/websetupmanager /srv/websetupmanager/library
sudo rsync -aHAX --numeric-ids /backup/wsm-2026-08-20/state/ /var/lib/websetupmanager/
sudo rsync -aHAX --numeric-ids /backup/wsm-2026-08-20/library/ /srv/websetupmanager/library/
sudo chown -R cncoperator:cncoperator /var/lib/websetupmanager /srv/websetupmanager/library
sudo systemctl start websetupmanager
```

Если `quick_check`, migration checksum, library fingerprint или process lock не
проходит, сервис намеренно остаётся unready/не запускается. Не удаляйте БД, lock
или marker и не создавайте пустую БД поверх старой; верните последний проверенный
backup либо передайте сохранённые roots разработчику.

## 7. Recovery, reconciliation и GC

Startup до listener удаляет безопасные остатки staging, согласует import/journal
records и выполняет identity-проверку managed ссылок. После listener первый
background-проход полностью сверяет SHA-256, очищает expired
idempotency/import/delete-confirmation records и запускает GC. Далее identity,
cleanup и GC выполняются каждые `WEB_SETUP_MANAGER_RECONCILE_INTERVAL`, а
полный SHA-256 scrub — раз в сутки. Поэтому большая библиотека не перечитывается
целиком каждую минуту и не задерживает liveness endpoint.

GC удаляет только immutable object без ссылок и без активной journal/reservation.
Дубликаты могут разделять object, поэтому физический объём после удаления может
не уменьшиться. Не удаляйте `objects/*`, `.object-staging`, `staging`, `indexes`
или rows SQLite вручную. Публичного endpoint принудительного GC нет; безопасный
ручной запуск — controlled restart, успешный `/readyz` и завершение первого
background maintenance event в JSON-log.

При внезапном shutdown незавершённая логическая revision не считается
завершённой. Для import/upload/validation/duplicate/restore доменный commit и
terminal persistent job фиксируются вместе: до commit новая revision/копия/
status невидимы и действительно прерванный job получает
`PROCESS_INTERRUPTED`; после commit job уже содержит стабильный terminal result.
Recovery может пометить неоднозначную незавершённую journal/reservation запись
как `conflict`, но не угадывает success по одному физическому object. Оператор
должен сверить setup, job и audit, затем повторить безопасную операцию либо
восстановить backup. Current setup не снимается автоматически, но блокирующее
состояние отображается, если он перестал быть ready.

## 8. Логи и аудит

Backend пишет newline-delimited JSON в stderr. Под systemd:

```bash
journalctl -u websetupmanager --since today --output=cat
journalctl -u websetupmanager -p warning..alert --output=cat
journalctl -u websetupmanager -f --output=cat
```

HTTP записи содержат безопасные request/entity/job IDs, route, status,
duration, bytes и stable error code. Доменные create/import/validate/current/
replace/duplicate/archive/restore/permanent-delete имеют audit events в SQLite.
Логи не должны содержать password, raw session cookie, Bearer token, G-code,
SQL, storage key или absolute path.
Если это обнаружено, ограничьте доступ к журналу, сохраните forensic copy и
ротируйте secret. Не вставляйте пользовательское содержимое в командную строку.

## 9. Upgrade и rollback

1. Прочитайте release notes и убедитесь, что target architecture поддержан.
2. Сделайте проверенный cold backup обоих roots.
3. Остановите сервис; сохраните старый binary.
4. Атомарно установите новый binary и запустите сервис.
5. Проверьте health/readiness, logs, library/current, preview и тестовую mutation.

```bash
sudo systemctl stop websetupmanager
sudo cp -a /usr/local/bin/websetupmanager /usr/local/bin/websetupmanager.previous
sudo install -o root -g root -m 0755 websetupmanager.new /usr/local/bin/websetupmanager
sudo systemctl start websetupmanager
curl --fail --silent http://127.0.0.1:8080/readyz
```

Migrations embedded, checksummed и выполняются до ready. Автоматического
downgrade нет. Если новая версия успела применить несовместимую migration,
нельзя просто вернуть старый binary: остановите сервис и восстановите оба roots
из pre-upgrade backup. Старый binary без restore допустим только когда release
notes явно подтверждают schema compatibility.

## 10. Incident runbook

| Симптом | Безопасное действие |
|---|---|
| `/healthz` недоступен | `systemctl status`, logs, unit/env, порт; не удалять данные |
| health 200, ready не 200 | проверить mount/права/free space/SQLite logs; остановить при повторе |
| remote startup сообщает `AUTHENTICATION_UNAVAILABLE` | проверить PAM production build/libpam, `/etc/pam.d/websetupmanager` и account status; не включать обход login |
| startup сообщает account mismatch | сделать `User=`/`Group=` unit равными `ALLOWED_USER`; не запускать от root или другого account |
| login отклонён/ограничен | проверить точное имя `ALLOWED_USER`, Linux password/account policy, HTTPS Origin и дождаться throttle window; не логировать password |
| startup сообщает второй процесс | найти владельца unit/lock; не удалять lock при живом процессе |
| import/download завис | проверить network/disk; отменить job; sliding I/O idle timeout завершит stall/slow client |
| `INSUFFICIENT_STORAGE` | освободить место вне managed roots или расширить FS; повторить operation |
| setup стал `attention` | прекратить использование; reconcile, проверить/заменить artifact, validate снова |
| HTML upload вернул `INVALID_CONTENT` | проверить standalone UTF-8 HTML; разбить text/attribute token >1 MiB на bounded elements; не отключать sanitizer |
| revision conflict | обновить карточку, проверить новый состав, повторить сохранённый ввод |
| corrupt/migration checksum | остановить, сохранить roots, восстановить matching backup; не пересоздавать DB |
| после crash job имеет `PROCESS_INTERRUPTED`/`conflict` | сверить setup revision, terminal job и audit; повторить с новым key только незавершённую operation; при сомнении остановить сервис и restore backup |
| подозрение на symlink/FIFO/socket | остановить, сохранить forensic copy; не открывать объект shell-инструментом от root |
| optional Bearer раскрыт | сменить secret в root-only env, рестарт, очистить proxy/journal exposure, проверить audit |
| browser session скомпрометирована | выполнить logout в доступной session; при недоступности временно остановить remote endpoint и провести credential/session incident procedure до возобновления |
| TLS certificate истекает | установить новую regular-file пару, проверить ownership, controlled restart |

После любого восстановления запишите время, версию binary, backup generation,
результат readiness и проверенные setup IDs. Не подтверждайте `ready` или current,
пока оператор не просмотрел артефакты и не выполнил validation заново.

## 11. Границы development qualification

В development environment автотесты проверяют domain/API/storage/UI
инварианты, sparse 10 GiB backend Range и SHA-verified cold-copy rebind.
Headless Firefox проверил загрузку same-origin API/assets и loading-state, но это
не controlled browser acceptance и не заменяет qualification на целевом станке.
Development suite также проверил durable pre-commit recovery, post-commit
terminal job/replay для replace/duplicate/validation/restore и настоящий
subprocess SIGKILL rollback для archive/delete/current. Это не заменяет
повторяемый power-loss drill import/replace/duplicate на target filesystem.

После PAM integration обычные и PAM-tagged Go suites/race/vet прошли; отдельный
non-PAM remote binary подтвердил fail-closed
`AUTHENTICATION_UNAVAILABLE`. Frontend lint/typecheck, 12 files/83 tests и Vite
production build прошли; `scripts/build.sh` прошёл целиком. Production binary
собран с tags `production,pam`. На текущем amd64 host (2026-08-20)
`websetupmanager.service` enabled/active от Linux-пользователя `user` и слушает
`https://microb.int:443`. Live direct-TLS проверка зафиксировала health/ready
200, guest session и закрытые capabilities, обычный PAM login/logout, затем
remembered PAM login, только SHA-256 cookie-token hash в SQLite, graceful
restart, восстановленную authenticated session и logout с удалением remembered
row. SHA-256 установленного binary:
`5df67ec084ec30e0f253f6fd38f565adbe9e4eb8656edc180f3fa2454be8469d`.
Текущий certificate self-signed с SAN `microb.int` и `10.0.1.136`; его доверие
на управляемом browser/client остаётся отдельным deployment действием.

До признания target qualification завершённой отдельно выполните:

- arm64 runtime, если целевая архитектура arm64;
- cold start/RSS/CPU/stream memory/10k-library/first-viewport measurements;
- controlled-browser visual, keyboard, 10 GiB preview/search and malicious
  PDF/HTML network/console walkthrough;
- trusted reverse-proxy variant, если он будет использоваться, и provisioning
  доверия к выбранному production certificate на browser clients;
- полный controlled-browser PAM walkthrough, включая expiry/throttling и
  optional Bearer deployment только если automation credential будет включён;
- cold backup/restore/upgrade drill на target filesystem;
- SIGKILL/power-loss fault drill с последующей recovery-проверкой.
