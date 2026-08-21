# Web Setup Manager — план миграции к LinuxCNC catalog

Статус: план перехода с исторической managed-object модели на требования
[PRODUCT_REQUIREMENTS.ru.md](PRODUCT_REQUIREMENTS.ru.md). Миграция не имеет права
удалять или молча перезаписывать пользовательские данные.

## 1. Исходное и целевое состояние

Историческая версия хранит G-code и Setup Sheet как SHA-256 objects в
`library_dir`; LinuxCNC их по именам не видит. Она допускает несколько программ,
validation/status/current/archive workflow.

Целевая версия хранит именованные payload-файлы непосредственно под canonical
LinuxCNC root `/home/user/linuxcnc/nc_files`, группирует Setup по физическим
каталогам и допускает ровно `0..1 program + 0..1 setup_sheet`. SQLite хранит
устойчивые catalog IDs, связи, revision/version, idempotency и audit.

## 2. До миграции

1. Остановить сервис и убедиться, что процесс действительно завершён.
2. Сделать согласованную cold copy:
   - `state_dir` со SQLite/WAL;
   - всего legacy `library_dir`;
   - всего `PROGRAM_ROOT`, включая пустые каталоги;
   - service unit/env и active LinuxCNC INI.
3. Записать checksum/size manifest всех regular files; отдельно перечислить
   symlink, hardlink и special-file entries, не открывая их содержимое.
4. Проверить, что `PROGRAM_ROOT` canonical, не symlink и совпадает с
   `[DISPLAY] PROGRAM_PREFIX` активного INI.
5. Не менять и не удалять legacy schema/objects до завершения проверки и
   отдельного решения об окончании rollback window.

## 3. Schema transition

Добавить checksummed forward-only migration с таблицами:

- `catalog_folders`: stable ID, parent ID, display/normalized name, revision;
- `catalog_setups`: stable ID, nullable folder ID, name, revision, timestamps;
- `catalog_files`: setup ID, role `program|setup_sheet`, relative filename,
  byte size, digest и file identity/version; unique `(setup_id, role)`;
- migration manifest/audit rows, позволяющие доказать source legacy ID/object,
  target relative path, digest и outcome.

Legacy tables остаются read-only migration source. Новая UI-модель не выводит
их validation/status/current semantics. Migration transaction никогда не
считает payload опубликованным до успешного atomic filesystem commit и
зафиксированного manifest result.

## 4. Преобразование legacy setups

Для каждого legacy Setup сформировать deterministic migration plan до записи:

- 0 программ: создать один неполный catalog Setup; перенести sheet, если она
  есть;
- 1 программа: создать один catalog Setup с программой и общей sheet;
- N программ: создать N catalog setups, по одному на программу. Первый может
  сохранить legacy setup ID при отсутствии конфликта; остальные получают новые
  opaque IDs. Общая legacy sheet копируется/публикуется отдельно для каждого
  нового Setup, чтобы связь оставалась однозначной;
- archived legacy entries не теряются: помещаются в явно названную migration
  folder и отмечаются в manifest, а не удаляются;
- status/validation/current не переносятся как gate. Их прежние значения можно
  сохранить только в migration audit для диагностики.

Default destination для migration должен быть отдельным физическим деревом,
например `Миграция/<legacy setup>/`, с безопасными normalized именами. Collision
разрешается deterministic suffix и отражается в preview manifest. Ни один
существующий target file не перезаписывается.

## 5. Публикация payload

Для каждого target файла:

1. Открыть каталог от удерживаемого `PROGRAM_ROOT` FD без symlink traversal.
2. Проверить source legacy object как regular single-link file и сверить
   ожидаемый SHA-256/размер.
3. Потоково скопировать в exclusive hidden temp target directory.
4. Сверить результирующий размер/digest, выполнить `fsync` файла.
5. Опубликовать `renameat2(..., RENAME_NOREPLACE)` и `fsync` каталога.
6. Зафиксировать catalog row и manifest outcome с относительным путём.
7. При любой ошибке убрать только созданный temp; legacy source и существующий
   target оставить без изменений.

`ngcgui_lib`, symlink, hardlink, FIFO, socket и device не мигрируются как
пользовательские Setup payload. Неизвестное расширение остаётся в legacy store и
получает явный `manual_review` outcome вместо догадки.

## 6. Deployment transition

Service config получает `PROGRAM_ROOT`, active `LINUXCNC_INI` и безопасный
display hint. Systemd сохраняет `ProtectHome=read-only`, но добавляет точное:

```ini
RequiresMountsFor=/home/user/linuxcnc/nc_files
ReadWritePaths=/home/user/linuxcnc/nc_files
```

Процесс остаётся `User=user`; приложение не получает root и не вызывает
LinuxCNC. Legacy `library_dir` остаётся доступен только на время миграции/
rollback и не является destination новых upload.

Discovery зафиксировал mode `0775` у текущего program root; новый root validator
требует отсутствие group/other write. Deployment отдельно проверяет owner
`user:user` и меняет только mode root на `0750` после cold backup. Recursive
permission rewrite payload tree в migration не входит.

## 7. Verification и rollback gate

Миграция считается подтверждённой только если:

- source/target count и manifest outcomes объясняют каждый legacy setup/artifact;
- каждый опубликованный target совпадает по byte size и SHA-256;
- cardinality каждого нового Setup не превышает один файл каждой роли;
- traversal/symlink/special/race suite оставляет внешний sentinel неизменным;
- catalog reload после рестарта даёт те же stable IDs/relative paths;
- тестовый G-code виден в QtDragon в ожидаемом каталоге, но `linuxcnc.stat().file`
  не меняется от действий Web Setup Manager;
- backup/restore новой тройки `state + legacy library + program root` проверен;
- PAM login, health/ready и production build прошли после deployment.

До этого gate rollback выполняется остановкой сервиса, восстановлением всей cold
generation и возвратом предыдущего binary/unit. Частичный rollback одной SQLite
или одного каталога запрещён. После gate legacy objects удаляются только отдельной
явно согласованной операцией с новым backup; сама migration их не удаляет.
