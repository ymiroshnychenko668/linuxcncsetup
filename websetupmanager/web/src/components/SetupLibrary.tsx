import { useEffect, useState, type FormEvent } from 'react'
import Alert from '@mui/material/Alert'
import Button from '@mui/material/Button'
import Card from '@mui/material/Card'
import Chip from '@mui/material/Chip'
import CircularProgress from '@mui/material/CircularProgress'
import NativeSelect from '@mui/material/NativeSelect'
import TextField from '@mui/material/TextField'
import type { SetupStatus, SetupSummary } from '../domain'
import { formatDate, statusLabels } from '../ui'

export type StatusFilter = 'active' | SetupStatus | 'all'
export type BooleanFilter = 'any' | 'yes' | 'no'

export interface LibraryFilters {
  query: string
  status: StatusFilter
  sheet: BooleanFilter
  current: BooleanFilter
  sort: 'updated_desc' | 'updated_asc' | 'name_asc' | 'name_desc' | 'recent_desc'
}

interface Props {
  alias: string
  items: SetupSummary[]
  filters: LibraryFilters
  loading: boolean
  loadingMore: boolean
  error?: string
  nextCursor?: string
  onFiltersChange: (filters: LibraryFilters) => void
  onResetFilters: () => void
  onRetry: () => void
  onLoadMore: () => void
  onOpen: (setupId: string) => void
  onCreate: () => void
  onImport: () => void
}

function SetupCard({ item, onOpen }: { item: SetupSummary; onOpen: (setupId: string) => void }) {
  return (
    <Card component="article" elevation={0} className={`setup-card setup-card--${item.status}`} aria-labelledby={`setup-${item.setupId}`}>
      <div className="setup-card__topline">
        <Chip component="span" className={`status-badge status-badge--${item.status}`} label={statusLabels[item.status]} size="small" />
        {item.isCurrent ? <Chip component="span" className="current-chip" label="Текущий" size="small" /> : null}
      </div>
      <h2 id={`setup-${item.setupId}`}>{item.name}</h2>
      <p className="setup-card__description">{item.description || 'Описание не добавлено.'}</p>
      <dl className="setup-card__facts">
        <div><dt>Программы</dt><dd>{item.programCount}</dd></div>
        <div><dt>Setup Sheet</dt><dd>{item.hasSetupSheet ? 'Есть' : 'Нет'}</dd></div>
        <div><dt>Revision</dt><dd>{item.revision}</dd></div>
      </dl>
      {item.notReadyReasons && item.notReadyReasons.length > 0 ? (
        <p className="setup-card__reason">{item.notReadyReasons[0]}</p>
      ) : null}
      <footer className="setup-card__footer">
        <span>Изменён {formatDate(item.updatedAt)}</span>
        <Button className="button button--quiet" variant="text" type="button" onClick={() => onOpen(item.setupId)}>
          Открыть сетап <span className="visually-hidden">{item.name}</span>
        </Button>
      </footer>
    </Card>
  )
}

export function SetupLibrary({
  alias,
  items,
  filters,
  loading,
  loadingMore,
  error,
  nextCursor,
  onFiltersChange,
  onResetFilters,
  onRetry,
  onLoadMore,
  onOpen,
  onCreate,
  onImport,
}: Props) {
  const [queryDraft, setQueryDraft] = useState(filters.query)
  useEffect(() => setQueryDraft(filters.query), [filters.query])
  const filtered = filters.query !== '' || filters.status !== 'active'
    || filters.sheet !== 'any' || filters.current !== 'any'

  const submitSearch = (event: FormEvent) => {
    event.preventDefault()
    onFiltersChange({ ...filters, query: queryDraft.trim() })
  }

  return (
    <section className="library" aria-labelledby="library-title" aria-busy={loading || undefined}>
      <div className="library__heading">
        <div>
          <p className="eyebrow">Локальная библиотека</p>
          <h1 id="library-title">{alias}</h1>
        </div>
        <div className="library__actions" aria-label="Действия библиотеки">
          <Button className="button button--quiet" variant="text" type="button" onClick={onCreate}>Создать сетап</Button>
          <Button className="button button--primary" variant="contained" type="button" onClick={onImport}>Импортировать комплект</Button>
        </div>
      </div>

      <form className="library-tools" role="search" onSubmit={submitSearch}>
        <label className="search-field">
          <span>Поиск сетапов</span>
          <span className="search-field__control">
            <TextField
              type="search"
              value={queryDraft}
              placeholder="Название, описание или программа"
              variant="standard"
              fullWidth
              slotProps={{
                htmlInput: { 'aria-label': 'Поиск сетапов' },
                input: { disableUnderline: true },
              }}
              onChange={(event) => setQueryDraft(event.target.value)}
            />
            <Button className="button button--primary" variant="contained" type="submit">Найти</Button>
          </span>
        </label>
        <label>
          <span>Статус</span>
          <NativeSelect
            value={filters.status}
            disableUnderline
            inputProps={{ 'aria-label': 'Статус' }}
            onChange={(event) => onFiltersChange({ ...filters, status: event.target.value as StatusFilter })}
          >
            <option value="active">Рабочие</option>
            <option value="draft">Черновики</option>
            <option value="ready">Готовые</option>
            <option value="attention">Требуют внимания</option>
            <option value="archived">Архив</option>
            <option value="all">Все статусы</option>
          </NativeSelect>
        </label>
        <label>
          <span>Setup Sheet</span>
          <NativeSelect
            value={filters.sheet}
            disableUnderline
            inputProps={{ 'aria-label': 'Setup Sheet' }}
            onChange={(event) => onFiltersChange({ ...filters, sheet: event.target.value as BooleanFilter })}
          >
            <option value="any">Любое состояние</option>
            <option value="yes">Есть</option>
            <option value="no">Нет</option>
          </NativeSelect>
        </label>
        <label>
          <span>Текущий</span>
          <NativeSelect
            value={filters.current}
            disableUnderline
            inputProps={{ 'aria-label': 'Текущий' }}
            onChange={(event) => onFiltersChange({ ...filters, current: event.target.value as BooleanFilter })}
          >
            <option value="any">Все</option>
            <option value="yes">Только текущий</option>
            <option value="no">Кроме текущего</option>
          </NativeSelect>
        </label>
        <label>
          <span>Сортировка</span>
          <NativeSelect
            value={filters.sort}
            disableUnderline
            inputProps={{ 'aria-label': 'Сортировка' }}
            onChange={(event) => onFiltersChange({
              ...filters,
              sort: event.target.value as LibraryFilters['sort'],
            })}
          >
            <option value="updated_desc">Недавно изменённые</option>
            <option value="updated_asc">Давно изменённые</option>
            <option value="name_asc">Название А–Я</option>
            <option value="name_desc">Название Я–А</option>
            <option value="recent_desc">Недавно открытые</option>
          </NativeSelect>
        </label>
      </form>

      {error ? (
        <Alert
          className="inline-state inline-state--error"
          severity="error"
          role="alert"
          action={<Button className="button button--quiet" variant="text" type="button" onClick={onRetry}>Повторить</Button>}
        >
          <div>
            <strong>Библиотеку не удалось загрузить</strong>
            <p>{error} Фильтры и введённый запрос сохранены.</p>
          </div>
        </Alert>
      ) : null}

      {loading && items.length === 0 ? (
        <div className="library-loading" role="status">
          <CircularProgress aria-hidden="true" size={34} /> Загружаем сетапы…
        </div>
      ) : null}

      {!loading && !error && items.length === 0 ? (
        <div className="empty-library">
          <div className="empty-library__illustration" aria-hidden="true"><span /><span /><span /></div>
          <p className="eyebrow">{filtered ? 'Поиск завершён' : 'Библиотека готова'}</p>
          <h2>{filtered ? 'Сетапы не найдены' : 'Здесь появятся технологические сетапы'}</h2>
          <p>
            {filtered
              ? 'Измените запрос или фильтры — существующие сетапы не были удалены.'
              : 'Каждый сетап объединяет G-code-программы, одну Setup Sheet, метаданные, revision и состояние готовности.'}
          </p>
          {filtered ? (
            <>
              <p className="filter-summary">
                Активные условия: {[
                  filters.query ? `запрос «${filters.query}»` : undefined,
                  filters.status !== 'active' ? `статус ${filters.status}` : undefined,
                  filters.sheet !== 'any' ? `Setup Sheet: ${filters.sheet === 'yes' ? 'есть' : 'нет'}` : undefined,
                  filters.current !== 'any' ? (filters.current === 'yes' ? 'только текущий' : 'кроме текущего') : undefined,
                ].filter(Boolean).join('; ')}.
              </p>
              <Button className="button button--quiet" variant="text" type="button" onClick={onResetFilters}>Сбросить запрос и фильтры</Button>
            </>
          ) : <Button className="button button--primary" variant="contained" type="button" onClick={onCreate}>Создать первый сетап</Button>}
        </div>
      ) : null}

      {items.length > 0 ? (
        <>
          <div className="setup-grid" aria-label="Сетапы">
            {items.map((item) => <SetupCard item={item} onOpen={onOpen} key={item.setupId} />)}
          </div>
          {nextCursor ? (
            <div className="load-more">
              <Button className="button button--quiet" variant="text" type="button" onClick={onLoadMore} disabled={loadingMore}>
                {loadingMore ? 'Загружаем…' : 'Показать ещё'}
              </Button>
            </div>
          ) : null}
        </>
      ) : null}
    </section>
  )
}
