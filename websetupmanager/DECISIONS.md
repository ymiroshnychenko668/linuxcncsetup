# Web Setup Manager — decisions

Неоднозначности требований фиксируются здесь до реализации. Решения `D-001`–
`D-020` сохраняют историю первой managed-library версии. Прямое продуктовое
решение владельца станка от 2026-08-21 имеет больший приоритет и зафиксировано в
`D-021`–`D-032` и
[PRODUCT_REQUIREMENTS.ru.md](PRODUCT_REQUIREMENTS.ru.md). Старое решение нельзя
использовать для восстановления multi-program/validation/dashboard UX вопреки
новой модели.

## D-021: catalog tool заменяет managed setup library

Текущий продукт — узкий каталог и upload-инструмент для одного LinuxCNC
`PROGRAM_PREFIX`, а не workflow проверки технологической готовности. Setup
остаётся основной сущностью, но имеет `0..1` G-code-программу и `0..1` Setup
Sheet и может быть неполным. Validation, `draft/ready/attention/archived` и
`current setup` не являются текущими операторскими состояниями или gate.

Файловые каталоги теперь являются разрешённым способом группировки Setup, но
это не означает универсальный файловый менеджер: Backend имеет ровно один
настроенный root, public API работает только с catalog folder/setup IDs и
нормализованными относительными путями. Он не принимает произвольный absolute
path и не открывает остальную filesystem.

Основной UI — компактный split в духе Visual Studio Code: G-code/Setup Sheet
viewer слева, дерево catalog folders/setups справа. Большие library cards,
pinned current area и validation banners относятся к старому UX и не должны
занимать основной viewport.

Этим решением полностью заменены `D-003`, `D-004` и `D-011`; части `D-009`,
`D-012`, `D-013`, `D-016` и `D-020` применимы только если не возвращают
устаревшую композицию/workflow.

## D-022: именованные программы публикуются в реальный PROGRAM_PREFIX

Фактически LinuxCNC 2.9.10 запущен от `user` с INI
`/home/user/linuxcnc/configs/corvuscnc/g540.ini`. Его `[DISPLAY]
PROGRAM_PREFIX` равен `/home/user/linuxcnc/nc_files`; QtDragon запоминает тот же
каталог и показывает вложенные directories. Поэтому новые G-code payload
публикуются как обычные именованные файлы непосредственно под этим root.

Configuration contract:

```text
WEB_SETUP_MANAGER_PROGRAM_ROOT=/home/user/linuxcnc/nc_files
WEB_SETUP_MANAGER_LINUXCNC_INI=/home/user/linuxcnc/configs/corvuscnc/g540.ini
WEB_SETUP_MANAGER_PROGRAM_ROOT_DISPLAY=~/linuxcnc/nc_files
```

Startup canonicalizes обе настройки и требует совпадения root с INI. UI/API
показывают безопасный `rootDisplay` и relative path, достаточные для навигации
оператора, но не раскрывают другой host path. Зарезервированный INI-путь
`ngcgui_lib` не доступен catalog mutations. Разрешены `.ngc`, `.nc`, `.tap` и
PDF/HTML sheet; `.py` и image filters не являются upload-типами приложения.

Web Setup Manager никогда не вызывает `ACTION.OPEN_PROGRAM`, LinuxCNC NML или
run command. Atomic publish делает файл видимым в QtDragon, а его загрузка и
исполнение остаются отдельным явным действием оператора на станке.

## D-023: filesystem payload и SQLite identity разделены

Именованные program/sheet files — operator-visible payload source of truth;
SQLite — источник устойчивых catalog folder/setup IDs, связей, revision,
expected file identity/version, idempotency и audit. Content-addressed objects
старой версии остаются migration source и rollback data, но не destination
новых upload.

Catalog storage использует held root FD, beneath/no-symlink resolution,
regular-file checks, streaming exclusive temp, target free-space checks,
`fsync` и atomic rename. Create всегда no-replace; replace/delete требуют
expected revision/version. Security/name/extension checks не называются
validation сетапа и не делают утверждений о корректности G-code.

Physical folders соответствуют catalog hierarchy. Public contract использует
`/api/v1/catalog`, relative paths и opaque IDs, а не `/fs`. Setup/payload
cardinality закрепляется unique `(setup_id, role)`, где role равен `program` или
`setup_sheet`.

## D-024: migration сохраняет legacy data и требует manifest

Переход выполняется forward-only additive schema migration и atomic copy из
legacy objects в именованные файлы. Legacy Setup с несколькими программами
разделяется на несколько catalog setups; общая sheet копируется для каждой
полученной однозначной пары. Пустой или sheet-only legacy Setup остаётся
неполным Setup. Collision не перезаписывается, а получает deterministic rename
или manual-review outcome.

До проверки count/size/SHA-256 manifest, restart stability, path-security suite,
QtDragon visibility и backup/restore старые objects и tables не удаляются.
Cold backup во время transition включает `state_dir`, legacy `library_dir` и
весь `PROGRAM_ROOT`. Полная процедура находится в
[MIGRATION_PLAN.md](MIGRATION_PLAN.md).

## D-025: file mutation precondition задаётся ровно одним HTTP header

Setup revision защищает composition/metadata, но не заменяет identity конкретного
именованного файла. Поэтому `PUT` новой program/sheet требует ровно
`If-None-Match: *`; replace и `DELETE` существующей component требуют ровно один
`If-Match: "<version>"`, где `version` — lowercase 64-hex opaque ETag из catalog
DTO/content response. Weak, unquoted, wildcard для replace, несколько значений
или одновременные `If-Match`/`If-None-Match` отклоняются как
`PRECONDITION_REQUIRED` до filesystem effect.

Mutation одновременно несёт `expectedRevision`, `Idempotency-Key` и session
CSRF. Header связывает действие с file identity, revision — с Setup, а
idempotency — с повтором одного network intent; ни один из трёх механизмов не
заменяет остальные. Content GET/HEAD допускает standard optional `If-Match`
candidate/`*` и отдельный exact `version` query, а Range viewer всегда отправляет
один exact ETag, чтобы блоки разных версий не объединялись.

## D-026: completed migration остаётся проверяемой provenance, а не флагом доверия

Общий `legacy_migration_state=completed` — terminal marker: последующий startup
является no-op и не превращает migration в постоянный filesystem scanner. Пока
общий run ещё `pending`/`running`, возобновление не доверяет уже completed
per-source mapping только по флагу: оно повторно сверяет ожидаемую cardinality
manifest, legacy artifact/object IDs, role, target relative path, byte
size/SHA-256, catalog setup/file linkage, version и physical identity. Только
после обработки всех source mappings общий state становится `completed`.
Folder, созданный migrator, получает unique `legacy_source_key`; существующий
same-name folder без этого точного source key не усваивается как migration-owned.

Legacy tables/objects остаются неизменяемой provenance и rollback source.
Collision, неполный/mismatched manifest, изменившийся target либо неясное
владение folder переводят migration в `manual_review` и возвращают startup
error до открытия listener. Автоматическое «починить» provenance по совпавшему
имени или молча отметить migration completed запрещено.

## D-027: HTML viewer отделяет credential fetch от originless rendering

Trusted SPA получает version-bound sanitized HTML same-origin запросом с
session credential и exact `If-Match`; Backend уже удалил active content и
выдал restrictive response/document CSP. Полученные bytes превращаются в Blob
URL и показываются только в iframe с пустым `sandbox` — без
`allow-scripts`, `allow-same-origin`, forms, navigation или popups. Поэтому даже
если строка URL выглядит как `blob:https://microb.int/...`, sandboxed document
имеет opaque origin и не получает доступ к parent DOM, cookies, local storage,
CSRF/session credential или catalog API.

Document CSP сохраняет `default-src 'none'`/`connect-src 'none'` и разрешает
только очищенные inline styles и `data:` images. Blob URL отзывается при
переключении файла, unmount или version change. Credential разрешён только
trusted fetch слою; он не передаётся документу viewer.

## D-028: production endpoint — direct HTTPS 443 без listener/redirect на 80

Фактический host использует direct TLS на `10.0.1.136:443` и URL
`https://microb.int/`. Web Setup Manager не слушает TCP/80 и не реализует
HTTP→HTTPS redirect. `http://microb.int/` поэтому должен получить connection
refused, если отдельный внешний proxy не был явно установлен; browser может
визуально «перенаправить» адрес локально из-за HTTPS-first/HSTS, но это не ответ
приложения.

Login, session cookie и mutations обслуживаются только HTTPS endpoint. Если в
будущем понадобится port 80 redirect, это отдельная reverse-proxy/firewall
конфигурация вне процесса и без принятия credentials по HTTP.

## D-029: v3→v4 migration backfill принимает только точную provenance

Migration 004 добавляет `catalog_setups.legacy_source_key` отдельно от уже
применяемой migration 003, не изменяя checksum старой schema generation. Для
незавершённого v3 run source key восстанавливается только из
`catalog_legacy_migrations`, если `catalog_setup_id`, `library_id` и
`legacy_setup_id` образуют ровно одну согласованную связь. Missing target,
cross-library/mismatched legacy setup, orphan marker или несколько mappings на
один catalog Setup заставляют всю migration 004 откатиться транзакционно.

Совпавшие folder/name и idempotency-key сами по себе не считаются provenance и
не дают права усвоить существующий объект. Новые folder/setup/file migration
effects записывают source provenance, mapping/manifest linkage и logical DB
effect в одной SQLite transaction; filesystem effect остаётся покрыт durable
catalog operation journal. Это устраняет зависимость restart resume от срока
жизни idempotency response.

## D-030: editor viewport использует flex containment и проверяется визуально

Catalog Workbench сохраняет горизонтальный split viewer/tree, но внутренний
editor stack строится как column flex с явными `min-height: 0` и растущим
content region. В production первый headless Firefox visual run обнаружил, что
предыдущий nested grid сжал G-code viewport до 0 px, хотя component tests были
зелёными. Commit `18411e613b380c4b73837003b96c949a21661041` заменил этот участок
grid→flex; повторный desktop/mobile run показал подсвеченный G-code и дерево.

Production visual smoke через WebDriver BiDi считается evidence layout/auth/UI,
но не подменяет keyboard-only acceptance или performance qualification
логического 10 GiB preview. Эти проверки получают отдельные статусы и не
объявляются пройденными по screenshot.

## D-031: focus return фиксируется до portal autoFocus, line jump не remount-ит input

Сквозной keyboard-only integration test обнаружил две реальные потери focus,
которые раздельные component tests не показывали. Portal child с `autoFocus`
успевал перехватить `document.activeElement` до эффекта `Modal`, поэтому при
закрытии dialog исходная кнопка не восстанавливалась. `Modal` теперь запоминает
инициатор при первом render до portal commit; если mutation удалила инициатор,
focus возвращается сначала в устойчивый `#catalog-editor`, затем в общий
`#main-content`.

Поле перехода к строке больше не имеет изменяющийся React `key`: оно controlled
и синхронизирует значение без remount, поэтому Enter сохраняет focus. Один App
integration scenario теперь проходит только keyboard events через login, tree,
upload/file choice, preview search, line jump и logout; отдельные tree/modal/
viewer tests проверяют детали trap/return/roving focus. Native file picker в
automation представлен стандартным `userEvent.upload`; production PAM/session
и реальный layout проверены отдельным Firefox smoke.

## D-032: G-code является родителем inline Setup Sheet в левом file tree

Прямая продуктовая корректировка владельца станка от 2026-08-21 заменяет
стороны split, create-flow и способ открытия Sheet из `D-021`: дерево файлов
находится слева, а единая editor surface — справа. В дереве G-code является
строкой верхнего уровня Setup; при наличии Sheet она всегда показана дочерней
строкой и обе строки переключают обычные editor tabs. Sheet рендерится inline,
а modal остаётся только для подтверждений и свойств.

Новая кнопка «Добавить» открывает native multi-file picker без application
wizard. Ровно один G-code обязателен; одна PDF/HTML Sheet может быть выбрана
вместе с ним или прикреплена позже прямым действием. Формулировка «оба файла —
оба загружаем» означает upload обеих компонент существующим scoped catalog API,
а не ZIP/export/download из P1. Backend/schema не ужесточаются: исторические
empty/sheet-only записи остаются видимыми и восстанавливаемыми добавлением
G-code, что избегает потери данных и не требует неатомарной destructive cleanup.

Первый paint G-code имеет приоритет над полным индексом: UI запрашивает один
version-bound prefix не более 64 КиБ, сразу показывает законченные начальные
строки и только после этого запускает Worker. Дальнейшие Range-блоки остаются
bounded и связаны exact ETag/`If-Match`. Заголовок не повторяет имя и путь:
имя остаётся в tab, поиск/line/index — в одной компактной панели, а точный
destination — в единственной status bar. Primary CTA использует спокойный
тёмно-зелёный фон с светлым текстом вместо яркого lime.

Очищенный HTML загружается exact-version fetch и показывается через revocable
`blob:` URL. Поэтому application CSP разрешает `blob:` только в `frame-src`;
iframe остаётся originless с пустым `sandbox`, а встроенная CSP очищенного
документа сохраняет `default-src 'none'; connect-src 'none'`.

## Статус исторических решений

| Решение | Статус в catalog-версии |
|---|---|
| `D-001`, `D-005`–`D-008`, `D-014`, `D-015`, `D-017`, `D-019` | сохраняется в непротиворечащей части |
| `D-002` | state/legacy roots сохраняются; добавлен отдельный `PROGRAM_ROOT` |
| `D-003`, `D-004`, `D-011` | заменены `D-021`–`D-023` |
| `D-009`, `D-010`, `D-012`, `D-013`, `D-016`, `D-020` | частично применимы к новым catalog mutations; legacy workflow не нормативен |
| `D-018` | backup boundary расширен до `PROGRAM_ROOT`; legacy текст ниже исторический |
| `D-025`–`D-032` | текущие exact precondition, migration provenance/backfill, HTML credential boundary, direct HTTPS, editor containment, focus-return и left-tree/inline-sheet решения |

## Исторический журнал D-001–D-020

Текст ниже сохранён без переписывания исходной мотивации. При конфликте
применяются `D-021`–`D-032`.

## D-001: production baseline

Первая сборка поддерживает Linux amd64/arm64, Go 1.26.5+, Node используется
только при сборке. Зафиксированный development/первичный acceptance baseline:
Debian 13.5 Trixie, kernel `6.12.95+deb13-rt-amd64` PREEMPT_RT, x86_64 и ext4.
Filesystem должна поддерживать atomic rename, fsync, sparse files и 64-bit
offsets. `openat2` с требуемыми resolve flags проверен реальным syscall;
root-anchored `*at` fallback покрывается тем же suite. Linux arm64 production
поддерживается как target, но новая cgo/PAM-сборка и runtime qualification
требуют arm64 toolchain/sysroot с libpam либо нативный arm64-хост; прежний
non-PAM cross-build не является evidence для текущей production-сборки.

## D-002: storage roots

`library_dir` и `state_dir` должны существовать, быть абсолютными реальными
каталогами, быть доступны текущему непривилегированному пользователю и не могут
совпадать или быть вложены друг в друга. Backend никогда не выбирает родительский
fallback. Это минимально и однозначно выполняет изоляцию state от content API.

## D-003: content-addressed immutable objects

Артефакт ссылается на неизменяемый SHA-256 storage object. Дубликаты могут
разделять object, но получают новые setup/artifact IDs; любая замена создаёт
новый object и меняет только одну логическую ссылку. Физический key не является
API field.

## D-004: import readiness

Успешный import публикует один целостный `draft`. Он не становится `ready`
автоматически: оператор запускает отдельную revision-bound validation. Это
следует общему правилу, что новый/изменённый setup требует проверки, и не
выдаёт распознавание типа файла за проверку LinuxCNC.

## D-005: local security and optional remote access

Default listener — `127.0.0.1:8080`. Local mode использует same-origin Host /
Origin checks и случайный CSRF token, читаемый только через same-origin
capabilities; login в этом режиме implicit. Remote mode разрешён только при
явной настройке, Linux PAM-capable production binary, одном настроенном non-root
`AllowedUser`, совпадающем с effective process user, и TLS 1.3 (или явно
доверенном TLS proxy); иначе startup завершается ошибкой.

SPA и узкие auth endpoints доступны до входа, чтобы показать собственную форму
вместо HTTP Basic prompt. PAM выполняет authentication и account-management
check только настроенного аккаунта и возвращает одинаковую внешнюю ошибку для
неверного имени/пароля. Успешный вход выдаёт случайную opaque
`__Host-websetupmanager_session` cookie (`Secure`, `HttpOnly`,
`SameSite=Strict`, `Path=/`); browser mutation требует точный HTTPS
Host/Origin и отдельный session CSRF. Обычные sessions живут только в памяти с
idle/absolute deadline. «Запомнить меня» имеет fixed deadline и сохраняет в
SQLite только SHA-256 token hash, CSRF, имя, времена и deployment scope — не
password и не raw token. Login ограничен по IP/имени, PAM concurrency и общей
session capacity. Случайный Bearer token длиной не менее 32 символов остаётся
необязательным credential для automation; HTTP Basic отсутствует.

## D-006: document viewers

HTML очищается allowlist sanitizer на backend и показывается только в iframe с
пустым `sandbox`; content response имеет отдельный максимально строгий CSP.
PDF отображается через локально собранный PDF.js canvas viewer без annotation /
JavaScript/action layer и без внешних переходов. Ни один viewer не получает
absolute path, cookie или API token.

## D-007: numerical NFR evidence

Автоматические benchmarks проверяют асимптотику, 10k-list query и sparse large
file behavior в development environment. Числа реального LinuxCNC компьютера
будут отдельно записаны как target acceptance evidence; код не будет объявлять
неизмеренное значение подтверждённым.

## D-008: SQLite implementation

SQLite реализован pure-Go `modernc.org/sqlite` v1.57.0 и не требует системной
SQLite library; он предоставляет online backup API для необратимых migrations.
Подключение включает WAL, foreign keys, busy timeout, synchronous FULL и process
lock. Однако production binary в целом намеренно собирается с
`CGO_ENABLED=1 -tags "production pam"` и зависит от системного libpam: это
необходимо для нормальной OS-аутентификации remote mode. Сборка без PAM может
использоваться для local development, но remote startup fail-closed.

Migration `002_auth_sessions.sql` добавляет только remembered browser-session
index. Первичный ключ — hex SHA-256 от opaque cookie token; raw token и password
никогда не попадают в SQLite. Username, CSRF, creation/expiry и deployment scope
нужны для проверки, logout и invalidation при изменении security-relevant
deployment state.

## D-009: names and collisions

Setup name ограничено 200 Unicode code points/800 UTF-8 bytes; artifact basename
— 255 code points и максимум 255 bytes. NUL/control, separators, volume paths,
`.`/`..`, trailing dot/space отвергаются. Collision key — NFC + Unicode
case-fold; display name хранится отдельно от внутреннего object key.

## D-010: TTL and irreversible cancellation

Idempotency result хранится 24 часа. Delete confirmation token живёт 5 минут и
привязан к setup/revision/name. После atomic DB commit job является terminal;
поздняя отмена возвращает стабильный terminal result и не выполняет опасную
компенсацию.

## D-011: current setup after mutation

Пользовательская или внешняя мутация не снимает current автоматически. Ссылка
остаётся, а UI показывает блокирующий `draft`/`attention`, пока оператор явно не
снимет current, не выберет другой или не провалидирует новую revision.

## D-012: operation jobs and external reconciliation

Import, prepared add/replace/Setup-Sheet upload, validation, duplicate и restore
имеют persistent job record; короткие metadata/state mutations остаются
синхронными. Permanent delete атомарно удаляет только доменные ссылки, а
физическую очистку выполняет reference-safe GC. Identity проверяется при
content/validation/mutation, на startup и ограниченным периодическим scanner;
неизвестный внешний object никогда не наследует artifact ID только из-за
совпадения display name.

## D-013: UI client identity

Browser создаёт один случайный client ID в localStorage. Содержательное state
хранится SQLite по `library_id + client_id`; только визуальные размеры могут
оставаться локальными. Отсутствующий setup восстанавливается как empty selection,
а не заменяется объектом с тем же именем.

## D-014: large-file fixtures

Sparse 10 GiB real file проверяет 64-bit HEAD/Range/RSS, но holes содержат NUL и
не являются валидным G-code. Поэтому preview/search отдельно тестируются на
generator-backed логическом ASCII stream с match у конца; эти сценарии нельзя
ошибочно объединять.

## D-015: sliding network I/O idle timeout

`WEB_SETUP_MANAGER_READ_TIMEOUT` исторически назван read timeout, но в P0
трактуется как общий sliding timeout отсутствия network I/O progress: для чтения
request body и записи response. Перед каждым body `Read` и response `Write`
Backend устанавливает новый соответствующий connection deadline; каждый
успешный блок сдвигает его дальше. Поэтому многогигабайтная загрузка или Range
response с постоянным прогрессом может длиться дольше значения настройки, а
зависший upload или slow/non-reading client освобождается после одного интервала.
`http.Server.ReadTimeout` намеренно равен нулю: его абсолютная семантика
конфликтует с потоковыми операциями. `ReadHeaderTimeout` остаётся отдельным;
также действуют connection `IdleTimeout`, header limit, upload/import limits и
лимит тяжёлых jobs.

## D-016: durable journal checkpoints are collapsed around the DB commit

Схема допускает состояния `intent`, `storage_applied`, `database_applied` и
terminal states. Публикация immutable storage object и reservation фиксируются
до доменной транзакции (`intent → storage_applied`). Логическая мутация,
audit-событие и перевод journal в terminal state выполняются одной SQLite
transaction. Поэтому обычный успешный путь не обязан оставлять наблюдаемую
durable запись `database_applied`: crash до commit не публикует revision, crash
после commit уже видит terminal row. Startup recovery принимает также legacy и
искусственно восстановленные промежуточные состояния и переводит неоднозначный
остаток в `conflict`; GC учитывает любую незавершённую reservation. Это безопасное
схлопывание DB step, а не утверждение атомарности SQLite с filesystem.

## D-017: development evidence is not target qualification

Development evidence получено на Debian 13.5, Linux
`6.12.95+deb13-rt-amd64` PREEMPT_RT, ext4, Go 1.26.5, Node 20.19.2. После auth
integration полный Go test/race/vet прошёл с `CGO_ENABLED=1 -tags pam`, frontend
lint/typecheck и 15 files/87 tests прошли, а production amd64 artifact собран с
tags `production,pam`; untagged Go/race и полный `scripts/build.sh` также прошли.
Non-PAM remote binary отдельно подтвердил fail-closed startup. На этом же host
direct TLS 1.3 проверен с реальным PAM login/logout и hash-only remembered
session через graceful restart; systemd service enabled/active. Это закрывает
конкретный integrated development/host gate, но не переносится автоматически на
другую машину или transport termination mode.

Headless Firefox ранее выполнил только same-origin asset/API smoke и screenshot
loading-state; подходящей controlled browser automation и реального
LinuxCNC-станка нет. Поэтому численные NFR, полный keyboard/visual walkthrough,
измерение browser memory/DOM на 10 GiB, активное содержимое PDF/HTML под
наблюдением сети, client trust для production certificate, trusted-proxy
variant, arm64 PAM build/runtime и повторяемый import/replace/duplicate
power-loss остаются отдельными target scenarios. Actual SIGKILL для
archive/delete/current уже проверен в development environment. Документация не
называет эти внешние проверки пройденными.

## D-018: operator backup, recovery and GC boundaries

Публичный API и UI не предоставляют произвольные backup/restore/GC операции.
Согласованная операторская резервная копия P0 — cold copy одновременно
`state_dir` и `library_dir` при остановленном процессе. Восстановление выполняется
только при остановленном Backend в подготовленные disjoint roots с тем же
владельцем; ручная замена отдельных SQLite/object-файлов запрещена. Reconcile,
очистка expired import/idempotency/delete-confirmation records и reference-safe
GC запускаются в background сразу после открытия listener и затем периодически;
до listener выполняются только
обязательная recovery и bounded identity-проверка ссылок. Частая проверка
identity не хэширует гигабайтные объекты; полная SHA-256 сверка выполняется при
первом background-проходе и раз в сутки. Оператор может безопасно инициировать
полный цикл рестартом и дождаться successful reconcile log;
`/readyz` не ожидает этот background-проход. Внутренние объекты нельзя
удалять вручную.
Cold copy может изменить inode/ctime. Такой объект rebind-ится к новой физической
версии только после полного совпадения ожидаемого SHA-256. Первый rebind-проход
оставляет Setup `attention`; в любом случае Setup не возвращается в `ready`
без явной повторной validation.

## D-019: HTML streaming and structural complexity

Общий размер самостоятельного HTML не ограничивается константой приложения:
upload/staging и sanitized response потоковые. Чтобы один незавершённый text или
attribute token не нарушил memory budget, один HTML token ограничен 1 MiB.
Тот же tokenizer и limit выполняются до публикации и при показе; неподдерживаемая
структура получает `INVALID_CONTENT`. Sanitized response читает accepted
неизменную версию 512 KiB Range-блоками и не буферизует целый input/output.
Документы произвольного общего размера поддерживаются при bounded tokens и в
пределах configured/storage limits.

## D-020: asynchronous domain result and terminal job are one commit

Успешная доменная часть prepared upload, validation, duplicate и restore не
фиксируется отдельно от terminal job. Setup/revision или validation result,
journal/audit, terminal job state/result/progress и применимая внутренняя
idempotency запись выполняются одной SQLite transaction. Для streaming upload
та же transaction завершает внешний `runUploadJob` claim и сохраняет точные
content digests; error/cancel terminal job и внешний claim также фиксируются
вместе. Import изначально применяет тот же принцип к setup, session, job,
journal, audit и commit-idempotency.

Поэтому startup не пытается эвристически повышать прерванный job до success:
до commit доменная мутация невидима и активный job получает
`PROCESS_INTERRUPTED`, после commit job уже имеет неизменяемый terminal result.
Поздняя отмена не маскирует commit, который успел победить; если terminal job
невозможно сериализовать или записать, вся доменная transaction откатывается.
Это проверяют `TestCommittedJobResultsAndUploadClaimsSurviveRestart`,
`TestAtomicJobMarshalFailureRollsBackDomainTransaction` и queued-validation
cancellation regression.
