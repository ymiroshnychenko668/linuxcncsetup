import { useEffect, useRef, useState, type FormEvent } from 'react'
import Alert from '@mui/material/Alert'
import Button from '@mui/material/Button'
import Checkbox from '@mui/material/Checkbox'
import FormControlLabel from '@mui/material/FormControlLabel'
import TextField from '@mui/material/TextField'
import { checkSetupName, createSetup, newIdempotencyKey } from '../api'
import type { Setup } from '../domain'
import { errorMessage, isNetworkError } from '../ui'
import { Modal } from './Modal'

interface Props {
  onClose: () => void
  onCreated: (setup: Setup) => void
}

export function CreateSetupDialog({ onClose, onCreated }: Props) {
  const nameRef = useRef<HTMLInputElement>(null)
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string>()
  const [key, setKey] = useState(newIdempotencyKey)
  const [matchingName, setMatchingName] = useState<string>()
  const [checkedName, setCheckedName] = useState<string>()
  const [duplicateAcknowledged, setDuplicateAcknowledged] = useState(false)

  const checkMatchingName = async (value: string, signal?: AbortSignal): Promise<string | undefined> => {
    if (value.trim() === '') return undefined
    return (await checkSetupName(value, signal))?.name
  }

  useEffect(() => {
    setMatchingName(undefined)
    setCheckedName(undefined)
    setDuplicateAcknowledged(false)
    if (name.trim() === '') return
    const controller = new AbortController()
    const timeout = window.setTimeout(() => {
      void checkMatchingName(name, controller.signal).then((match) => {
        setMatchingName(match)
        setCheckedName(name)
      }, (reason: unknown) => {
        if (!(reason instanceof DOMException && reason.name === 'AbortError')) setCheckedName(undefined)
      })
    }, 250)
    return () => { window.clearTimeout(timeout); controller.abort() }
  }, [name])

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (name.trim() === '') {
      setError('Введите название сетапа.')
      nameRef.current?.focus()
      return
    }
    setPending(true)
    setError(undefined)
    try {
      const match = checkedName === name
        ? matchingName
        : await checkMatchingName(name)
      setMatchingName(match)
      setCheckedName(name)
      if (match && !duplicateAcknowledged) {
        setError('Подтвердите создание отдельного сетапа с совпадающим названием.')
        return
      }
      const setup = await createSetup(name, description, key)
      onCreated(setup)
    } catch (reason) {
      setError(errorMessage(reason))
      if (!isNetworkError(reason)) setKey(newIdempotencyKey())
    } finally {
      setPending(false)
    }
  }

  return (
    <Modal
      title="Новый сетап"
      description="Создаётся пустой черновик. Программы и общую Setup Sheet можно добавить из карточки."
      onClose={onClose}
      closeDisabled={pending}
      initialFocusRef={nameRef}
      footer={(
        <>
          <Button className="button button--quiet" variant="outlined" type="button" onClick={onClose} disabled={pending}>Отмена</Button>
          <Button className="button button--primary" variant="contained" type="submit" form="create-setup-form" disabled={pending}>
            {pending ? 'Создаём…' : 'Создать черновик'}
          </Button>
        </>
      )}
    >
      <form id="create-setup-form" className="stack-form" onSubmit={(event) => void submit(event)}>
        <label>
          <span>Название <strong aria-hidden="true">*</strong></span>
          <TextField inputRef={nameRef} value={name} fullWidth size="small" slotProps={{ htmlInput: { maxLength: 200 } }} onChange={(event) => setName(event.target.value)} required />
        </label>
        <label>
          <span>Описание</span>
          <TextField multiline rows={5} value={description} fullWidth size="small" onChange={(event) => setDescription(event.target.value)} />
        </label>
        {error ? <Alert className="form-error" severity="error" role="alert">{error}</Alert> : null}
        {matchingName ? (
          <Alert className="inline-warning" severity="warning" role="alert">
            <p>Уже существует сетап «{matchingName}» с таким отображаемым названием. Сетапы останутся разными благодаря устойчивым ID.</p>
            <FormControlLabel
              className="toggle"
              sx={{ margin: 0 }}
              control={<Checkbox
                size="small"
                sx={{ padding: '2px' }}
                checked={duplicateAcknowledged}
                onChange={(event) => { setDuplicateAcknowledged(event.target.checked); setError(undefined) }}
              />}
              label="Создать отдельный сетап с совпадающим названием"
            />
          </Alert>
        ) : <p className="form-hint">Совпадающие отображаемые названия разрешены, но перед созданием будет показано предупреждение.</p>}
      </form>
    </Modal>
  )
}
