# Web Setup Manager — руководство оператора и администратора

Документ описывает актуальную catalog-версию из
[PRODUCT_REQUIREMENTS.ru.md](PRODUCT_REQUIREMENTS.ru.md). Web Setup Manager
работает только внутри одного настроенного LinuxCNC `PROGRAM_PREFIX` и не даёт
произвольный доступ к остальной filesystem. Исторические managed `objects` и
SQLite нельзя изменять вручную во время migration/rollback window.

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

1. Справа раскройте compact tree folders/setups или создайте нужный folder.
   Физические folders находятся под `PROGRAM_PREFIX`; `ngcgui_lib` зарезервирован
   LinuxCNC и не используется для пользовательских сетапов.
2. Создайте Setup в выбранном folder. Пустой Setup допустим. Добавьте одну
   `.ngc`, `.nc` или `.tap` программу и при необходимости одну PDF/HTML Setup
   Sheet. Вторая программа/sheet означает replace, а не добавление composition.
3. До подтверждения upload проверьте operator-facing destination, например
   `Программы → Заказы → Клиент → detail.ngc`. Backend не принимает absolute
   path от browser и не может выйти выше настроенного root.
4. После успеха программа физически опубликована под
   `/home/user/linuxcnc/nc_files/<относительный каталог>/...`. Откройте штатный
   QtDragon file manager, перейдите из `User` по тому же дереву и выберите файл
   вручную.
5. Слева просматривайте выбранную программу с номерами строк, переходом и
   literal-поиском. Большие файлы читаются Range-блоками. Setup Sheet открывается
   как canvas PDF либо очищенный HTML в sandbox.
6. При version/revision conflict сначала обновите tree/viewer и сравните
   актуальный файл. Приложение не перезаписывает внешнее изменение молча.
7. Удаление/перемещение catalog entity меняет именованный файл/каталог только
   после явного подтверждения. Восстановление удалённых данных возможно из
   согласованной cold backup.

Upload, selection и preview **не открывают и не запускают программу в
LinuxCNC**. Перед обработкой оператор отдельно проверяет станок, оснастку,
tool table, ноль, траекторию и сам G-code. Security-проверки имени, типа,
размера и safe path не являются проверкой корректности обработки.

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
`/readyz` — SQLite/state, program root и совпадение active INI. Не направляйте
production процесс на каталог тестов, другой `PROGRAM_PREFIX` или другой legacy
library marker. Во время migration readiness не заменяет проверку migration
manifest; выполните отдельный сценарий из [MIGRATION_PLAN.md](MIGRATION_PLAN.md).

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
session response, login, capabilities, тестовое создание пустого catalog Setup
и logout; не
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
State backup содержит hash-only remembered-session rows. Защищайте его как
authentication state: password/raw cookie там нет, но matching browser token и
тот же deployment scope могут восстановить session после cold restore.

## 6. Восстановление

1. Остановите Backend и сохраните повреждённые roots отдельно для анализа.
2. Восстанавливайте matching `state`, `library` и `program-root` из одной backup
   generation. Не смешивайте DB одной generation с program files/objects другой.
3. Восстановите владельца/права, убедитесь, что roots и TLS files не symlink.
4. Проверьте, что restored root совпадает с `PROGRAM_PREFIX` active INI, затем
   запустите сервис. Startup выполняет SQLite `quick_check`, checksummed
   migrations, cleanup собственных temp и catalog identity reconciliation.
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
остатки, согласует незавершённые catalog mutations и проверяет удерживаемый root.
Он не удаляет неизвестные files/folders под `PROGRAM_ROOT`. Periodic reconcile
сравнивает ожидаемую relative path и file identity; дорогой digest нужен при
неоднозначной смене identity, а не для validation G-code.

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

## 10. Incident runbook

| Симптом | Безопасное действие |
|---|---|
| `/healthz` недоступен | `systemctl status`, logs, unit/env, порт; не удалять данные |
| health 200, ready не 200 | проверить mount/права/free space/SQLite logs; остановить при повторе |
| remote startup сообщает `AUTHENTICATION_UNAVAILABLE` | проверить PAM production build/libpam, `/etc/pam.d/websetupmanager` и account status; не включать обход login |
| startup сообщает account mismatch | сделать `User=`/`Group=` unit равными `ALLOWED_USER`; не запускать от root или другого account |
| login отклонён/ограничен | проверить точное имя `ALLOWED_USER`, Linux password/account policy, HTTPS Origin и дождаться throttle window; не логировать password |
| startup сообщает второй процесс | найти владельца unit/lock; не удалять lock при живом процессе |
| upload/download завис | проверить network/disk; отменить request/job; sliding I/O idle timeout завершит stall/slow client |
| `INSUFFICIENT_STORAGE` | освободить место вне `PROGRAM_ROOT`/state или расширить FS; повторить operation |
| program/sheet показывает conflict | обновить tree/viewer, сравнить relative path/version и только затем явно replace |
| HTML upload вернул `INVALID_CONTENT` | проверить standalone UTF-8 HTML; разбить text/attribute token >1 MiB на bounded elements; не отключать sanitizer |
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

## 11. Границы development qualification

Историческая managed-library версия имела автотесты domain/API/storage/UI,
sparse 10 GiB backend Range и SHA-verified cold-copy rebind.
Headless Firefox проверил загрузку same-origin API/assets и loading-state, но это
не controlled browser acceptance и не заменяет qualification на целевом станке.
Исторический suite также проверил durable pre-commit recovery, post-commit
terminal job/replay и несколько SIGKILL сценариев. Это не является доказательством
direct named-file publish, folder operations или migration новой catalog-модели.

До смены product direction PAM integration, обычные/PAM-tagged Go suites,
race/vet прошли; отдельный
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

Это evidence можно переиспользовать как auth foundation, но новую версию нельзя
назвать qualified, пока не пройдут catalog schema/API/storage/frontend tests,
live upload в `/home/user/linuxcnc/nc_files`, QtDragon visibility без изменения
loaded program и no-data-loss migration/rollback drill.

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
