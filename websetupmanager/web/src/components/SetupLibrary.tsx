import { useEffect, useState, type FormEvent } from 'react'
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
    <article className={`setup-card setup-card--${item.status}`} aria-labelledby={`setup-${item.setupId}`}>
      <div className="setup-card__topline">
        <span className={`status-badge status-badge--${item.status}`}>{statusLabels[item.status]}</span>
        {item.isCurrent ? <span className="current-chip">Текущий</span> : null}
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
        <button className="button button--quiet" type="button" onClick={() => onOpen(item.setupId)}>
          Открыть сетап <span className="visually-hidden">{item.name}</span>
        </button>
      </footer>
    </article>
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
          <button className="button button--quiet" type="button" onClick={onCreate}>Создать сетап</button>
          <button className="button button--primary" type="button" onClick={onImport}>Импортировать комплект</button>
        </div>
      </div>

      <form className="library-tools" role="search" onSubmit={submitSearch}>
        <label className="search-field">
          <span>Поиск сетапов</span>
          <span className="search-field__control">
            <input
              type="search"
              value={queryDraft}
              placeholder="Название, описание или программа"
              onChange={(event) => setQueryDraft(event.target.value)}
            />
            <button className="button button--primary" type="submit">Найти</button>
          </span>
        </label>
        <label>
          <span>Статус</span>
          <select
            value={filters.status}
            onChange={(event) => onFiltersChange({ ...filters, status: event.target.value as StatusFilter })}
          >
            <option value="active">Рабочие</option>
            <option value="draft">Черновики</option>
            <option value="ready">Готовые</option>
            <option value="attention">Требуют внимания</option>
            <option value="archived">Архив</option>
            <option value="all">Все статусы</option>
          </select>
        </label>
        <label>
          <span>Setup Sheet</span>
          <select
            value={filters.sheet}
            onChange={(event) => onFiltersChange({ ...filters, sheet: event.target.value as BooleanFilter })}
          >
            <option value="any">Любое состояние</option>
            <option value="yes">Есть</option>
            <option value="no">Нет</option>
          </select>
        </label>
        <label>
          <span>Текущий</span>
          <select
            value={filters.current}
            onChange={(event) => onFiltersChange({ ...filters, current: event.target.value as BooleanFilter })}
          >
            <option value="any">Все</option>
            <option value="yes">Только текущий</option>
            <option value="no">Кроме текущего</option>
          </select>
        </label>
        <label>
          <span>Сортировка</span>
          <select
            value={filters.sort}
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
          </select>
        </label>
      </form>

      {error ? (
        <div className="inline-state inline-state--error" role="alert">
          <div>
            <strong>Библиотеку не удалось загрузить</strong>
            <p>{error} Фильтры и введённый запрос сохранены.</p>
          </div>
          <button className="button button--quiet" type="button" onClick={onRetry}>Повторить</button>
        </div>
      ) : null}

      {loading && items.length === 0 ? (
        <div className="library-loading" role="status">
          <span className="spinner" aria-hidden="true" /> Загружаем сетапы…
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
              <button className="button button--quiet" type="button" onClick={onResetFilters}>Сбросить запрос и фильтры</button>
            </>
          ) : <button className="button button--primary" type="button" onClick={onCreate}>Создать первый сетап</button>}
        </div>
      ) : null}

      {items.length > 0 ? (
        <>
          <div className="setup-grid" aria-label="Сетапы">
            {items.map((item) => <SetupCard item={item} onOpen={onOpen} key={item.setupId} />)}
          </div>
          {nextCursor ? (
            <div className="load-more">
              <button className="button button--quiet" type="button" onClick={onLoadMore} disabled={loadingMore}>
                {loadingMore ? 'Загружаем…' : 'Показать ещё'}
              </button>
            </div>
          ) : null}
        </>
      ) : null}
    </section>
  )
}
