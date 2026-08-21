# Web Setup Manager — актуальные продуктовые требования

Статус: **нормативный документ текущей версии** с 2026-08-21.

Этот документ фиксирует прямое продуктовое решение владельца станка и заменяет
противоречащие ему положения
[functional-requirements.ru.md](functional-requirements.ru.md). Исходный документ
сохранён как исторический baseline первой реализации. Его требования по
аутентификации, безопасной потоковой работе с файлами и изолированным viewer
переиспользуются только там, где они не противоречат этой спецификации.

## 1. Назначение и границы

Web Setup Manager — компактный каталог и инструмент загрузки программ для
конкретного LinuxCNC-станка. Оператор организует сетапы по реальным каталогам,
загружает G-code и необязательную Setup Sheet, просматривает их в браузере и
затем самостоятельно выбирает программу в штатном интерфейсе LinuxCNC.

Приоритеты текущего P0:

1. понятное физическое место программы на станке;
2. быстрый upload и просмотр;
3. компактное дерево каталогов и сетапов;
4. безопасная работа строго внутри LinuxCNC `PROGRAM_PREFIX`;
5. отсутствие действий, управляющих LinuxCNC.

Не входят в текущий P0: validation/readiness workflow, проверка корректности
G-code или траектории, `current setup`, toolpath rendering, редактирование
tool table, запуск/загрузка программы в LinuxCNC, произвольный доступ ко всей
файловой системе и универсальный файловый API.

## 2. Фактическая интеграция со станком

На целевом хосте LinuxCNC 2.9.10 запущен от `user` с конфигурацией:

```text
/home/user/linuxcnc/configs/corvuscnc/g540.ini
```

Её `[DISPLAY] PROGRAM_PREFIX`:

```text
/home/user/linuxcnc/nc_files
```

QtDragon использует этот каталог как `User` и поддерживает вложенные каталоги.
Допустимые расширения G-code в активной конфигурации: `.ngc`, `.nc`, `.tap`.
Подкаталог `ngcgui_lib` зарезервирован конфигурацией LinuxCNC для подпрограмм и
не является пользовательской группой сетапов.

Backend получает корень и активный INI явно:

```text
WEB_SETUP_MANAGER_PROGRAM_ROOT=/home/user/linuxcnc/nc_files
WEB_SETUP_MANAGER_LINUXCNC_INI=/home/user/linuxcnc/configs/corvuscnc/g540.ini
WEB_SETUP_MANAGER_PROGRAM_ROOT_DISPLAY=~/linuxcnc/nc_files
```

На старте canonical `PROGRAM_ROOT` должен совпасть с `PROGRAM_PREFIX` из INI.
Несовпадение делает сервис неготовым/останавливает безопасный startup: нельзя
успешно сообщить оператору, что файл доступен LinuxCNC, если это не так.

## 3. Доменная модель

- Основная сущность остаётся **Setup**.
- Один Setup содержит **не более одной** G-code-программы и **не более одной**
  PDF/HTML Setup Sheet.
- Setup может быть неполным: без программы, без Setup Sheet или временно без
  обоих файлов. Неполнота отображается информационно и не блокирует upload,
  просмотр, переименование или перемещение.
- Setup имеет устойчивый opaque `setup_id`, отображаемое имя, revision и ссылку
  на один каталог каталога.
- Catalog Folder соответствует реальному подкаталогу под `PROGRAM_ROOT` и может
  содержать дочерние каталоги и сетапы. Корень каталога также может содержать
  сетапы.
- Программа и Setup Sheet публикуются как обычные именованные файлы рядом в
  каталоге сетапа. SQLite хранит устойчивые ID, относительные имена/связи,
  revision, версии файлов и служебный audit, но не заменяет файловый каталог
  content-addressed объектами.
- Состояния `draft`, `ready`, `attention`, `archived`, validation runs и выбор
  `current setup` относятся к исторической модели и не являются текущим
  операторским workflow.

## 4. P0-требования

| ID | Требование |
|---|---|
| `CAT-P0-001` | Основной экран состоит из viewer слева и дерева каталогов/сетапов справа. |
| `CAT-P0-002` | Layout компактный, плотный и визуально близок к UX-паттернам Visual Studio Code: activity/toolbar, tree rows, tabs, resizable split, без больших dashboard-карточек. |
| `CAT-P0-003` | Дерево отражает иерархию каталогов под настроенным `PROGRAM_ROOT`; каталоги раскрываются/сворачиваются, выбранный Setup подсвечивается. |
| `CAT-P0-004` | Setup имеет cardinality `0..1 program + 0..1 setup_sheet`; вторая программа или вторая sheet не создаётся молча. |
| `CAT-P0-005` | Неполный Setup допустим и не требует validation/readiness transition. UI показывает только фактическое наличие программы и sheet. |
| `CAT-P0-006` | До upload UI показывает каталог, имя и понятный operator-facing destination; после успеха сообщает путь вида `Программы → Заказы → Деталь → part.ngc`. |
| `CAT-P0-007` | Загруженный G-code атомарно появляется под реальным LinuxCNC `PROGRAM_PREFIX` и доступен в QtDragon после ручной навигации оператора. |
| `CAT-P0-008` | Выбор Setup, preview и upload не вызывают LinuxCNC API/NML, не открывают и не исполняют программу на станке. |
| `CAT-P0-009` | Поддержаны создание, переименование, перемещение и удаление catalog folders и setups с revision/version conflict handling. |
| `CAT-P0-010` | Поддержаны streaming add/replace/delete единственной программы и единственной Setup Sheet без чтения целого файла в память. |
| `CAT-P0-011` | G-code viewer поддерживает Range/ETag consistency, номера строк, виртуализацию, переход к строке и literal search. |
| `CAT-P0-012` | PDF показывается локальным безопасным viewer; HTML очищается, получает отдельный CSP и показывается в sandbox без credential. |
| `CAT-P0-013` | Public API относится только к catalog entities и content по ID; в нём нет произвольного `/fs`, абсолютных host paths, storage keys или доступа выше root. |
| `CAT-P0-014` | API может возвращать только нормализованный относительный `relativePath` и безопасный `rootDisplay`, предназначенный для оператора. |
| `CAT-P0-015` | Все операции разрешают путь от удерживаемого root FD, запрещают traversal, symlink traversal, hardlink/special-file substitution и зарезервированный `ngcgui_lib`. |
| `CAT-P0-016` | Upload использует exclusive staging, лимиты/проверку свободного места, regular-file identity, `fsync` и atomic rename; ошибка, отмена или restart не оставляют опубликованный частичный файл. |
| `CAT-P0-017` | Create не перезаписывает существующий файл; replace требует ожидаемую revision и версию файла. Внешнее изменение приводит к стабильному conflict, а не к тихой потере данных. |
| `CAT-P0-018` | Разрешены только G-code-типы активной конфигурации (`.ngc`, `.nc`, `.tap`) и PDF/HTML sheet. Произвольные `.py`, device, FIFO, socket и symlink не принимаются. |
| `CAT-P0-019` | Поиск/фильтр дерева работает по относительному каталогу и имени Setup; loading, empty, offline, error и conflict состояния локальны и не уничтожают выбранный контекст. |
| `CAT-P0-020` | Keyboard baseline включает навигацию по tree, открытие Setup, viewer search/line jump, upload dialogs, focus trap/return и видимый focus. |
| `CAT-P0-021` | Remote access сохраняет PAM login того же Linux account, secure cookie session, exact Host/Origin, per-session CSRF, logout и throttle; password не сохраняется приложением. |
| `CAT-P0-022` | `/healthz` проверяет процесс; `/readyz` — SQLite, state/staging и удерживаемый `PROGRAM_ROOT`, включая совпадение с активным INI. |
| `CAT-P0-023` | Backup/restore включает SQLite state, legacy library до завершения миграции и весь `PROGRAM_ROOT` как одну согласованную generation. |
| `CAT-P0-024` | Существующие пользовательские данные и legacy managed objects не удаляются автоматически; миграция формирует проверяемый manifest и использует no-replace публикацию. |

Проверка имени/расширения, размера, regular-file identity и безопасного пути —
это security/integrity checks, а не проверка готовности сетапа или корректности
обработки. Backend не интерпретирует G-code как доказательство его безопасности.

## 5. API-границы

Текущий API использует namespace `/api/v1/catalog` и сущности folder/setup.
Нормативная семантика:

- получить дерево каталога;
- CRUD folder с относительным родителем/именем;
- CRUD Setup и перемещение между folders;
- `PUT/DELETE` единственной программы и единственной Setup Sheet;
- `HEAD/GET` version-bound content с Range/ETag;
- optimistic concurrency и idempotency для опасных повторов.

Физический root существует только в server config. Browser получает
`rootDisplay` и относительный путь выбранного Setup, достаточные для ответа на
вопрос «где найти программу на станке», но не может выбрать другой host root.

## 6. Acceptance criteria текущей версии

| AC | Проверяемый результат |
|---|---|
| `CAT-AC-01` | Startup на целевом host подтверждает canonical root `/home/user/linuxcnc/nc_files` и соответствующий `PROGRAM_PREFIX` активного `g540.ini`. |
| `CAT-AC-02` | Созданный через UI вложенный folder физически существует под root и отображается в правом tree после reload. |
| `CAT-AC-03` | Можно создать пустой Setup, Setup только с программой и Setup только с sheet; validation не требуется и не предлагается. |
| `CAT-AC-04` | Upload `jobs/acme/part.ngc` заканчивается atomic publish; файл с теми же bytes виден в QtDragon `User → jobs → acme` и не загружен в LinuxCNC автоматически. |
| `CAT-AC-05` | Попытка добавить вторую программу/sheet получает понятный replace/conflict flow и не создаёт multi-program composition. |
| `CAT-AC-06` | Выбор узла открывает слева G-code или sheet; большой G-code использует bounded Range/virtualized preview и сохраняет ETag consistency. |
| `CAT-AC-07` | Основной viewport — компактный left-viewer/right-tree split; destination виден до подтверждения upload без поиска по отдельной карточке. |
| `CAT-AC-08` | Traversal, абсолютный путь, symlink, hardlink, FIFO, socket, device и race substitution не читают/не меняют внешний sentinel. |
| `CAT-AC-09` | Disconnect/cancel/crash до commit оставляет прежний файл либо отсутствие файла; частичное конечное имя не наблюдается, temp восстанавливается/очищается. |
| `CAT-AC-10` | Внешнее изменение между preview/replace и commit вызывает version conflict; существующие bytes не перезаписываются. |
| `CAT-AC-11` | Полный keyboard-only flow login → tree → upload → preview/search → logout имеет корректный порядок focus и возврат focus. |
| `CAT-AC-12` | Production build, Go unit/integration/race/vet, frontend lint/typecheck/tests, path-security suite, local health/ready и реальный PAM smoke проходят для новой catalog-модели. |

## 7. Открытые последующие направления

Toolpath renderer, tool table и другие станочные функции могут появиться позже
как отдельные модули вокруг выбранной программы. Они не должны менять P0-факт:
каталог только публикует и показывает файлы; выполнение остаётся явным действием
оператора в LinuxCNC.
