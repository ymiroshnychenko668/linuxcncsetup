import Alert from '@mui/material/Alert'
import Button from '@mui/material/Button'
import Card from '@mui/material/Card'
import CircularProgress from '@mui/material/CircularProgress'
import IconButton from '@mui/material/IconButton'
import type { RecentSetup } from '../domain'
import { formatDate, statusLabels } from '../ui'

interface Props {
  items: RecentSetup[]
  loading: boolean
  error?: string
  onOpen: (item: RecentSetup) => void
  onDelete: (setupId: string) => void
  onClear: () => void
  onRetry: () => void
}

export function RecentSetupsPanel({ items, loading, error, onOpen, onDelete, onClear, onRetry }: Props) {
  if (loading) return <Card component="section" elevation={0} className="recent-panel" aria-busy="true"><p role="status"><CircularProgress aria-hidden="true" size={18} /> Загружаем недавние сетапы…</p></Card>
  if (error) return <Alert component="section" className="recent-panel recent-panel--error" severity="error" role="alert" action={<Button className="button button--quiet" variant="text" type="button" onClick={onRetry}>Повторить</Button>}>Недавние сетапы недоступны: {error}</Alert>
  if (items.length === 0) return null
  return (
    <Card component="section" elevation={0} className="recent-panel" aria-labelledby="recent-title">
      <div className="section-heading"><div><p className="eyebrow">История работы</p><h2 id="recent-title">Недавние сетапы</h2></div><Button className="button button--quiet" variant="text" type="button" onClick={onClear}>Очистить список</Button></div>
      <ul className="recent-list">
        {items.map((item) => <li key={item.setupId}>
          <Button className="recent-list__open" variant="text" type="button" onClick={() => onOpen(item)}>
            <strong>{item.setupName}</strong>
            <span>{statusLabels[item.setupStatus]} · {formatDate(item.lastOpenedAt)}{item.lastLine ? ` · строка ${item.lastLine}` : ''}</span>
          </Button>
          <IconButton className="icon-button" type="button" size="small" aria-label={`Удалить ${item.setupName} из недавних`} onClick={() => onDelete(item.setupId)}>×</IconButton>
        </li>)}
      </ul>
    </Card>
  )
}
