import { useEffect, useRef, useState, type FormEvent } from 'react'
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
          <button className="button button--quiet" type="button" onClick={onClose} disabled={pending}>Отмена</button>
          <button className="button button--primary" type="submit" form="create-setup-form" disabled={pending}>
            {pending ? 'Создаём…' : 'Создать черновик'}
          </button>
        </>
      )}
    >
      <form id="create-setup-form" className="stack-form" onSubmit={(event) => void submit(event)}>
        <label>
          <span>Название <strong aria-hidden="true">*</strong></span>
          <input ref={nameRef} value={name} maxLength={200} onChange={(event) => setName(event.target.value)} required />
        </label>
        <label>
          <span>Описание</span>
          <textarea value={description} rows={5} onChange={(event) => setDescription(event.target.value)} />
        </label>
        {error ? <p className="form-error" role="alert">{error}</p> : null}
        {matchingName ? (
          <div className="inline-warning" role="alert">
            <p>Уже существует сетап «{matchingName}» с таким отображаемым названием. Сетапы останутся разными благодаря устойчивым ID.</p>
            <label className="toggle">
              <input
                type="checkbox"
                checked={duplicateAcknowledged}
                onChange={(event) => { setDuplicateAcknowledged(event.target.checked); setError(undefined) }}
              />
              Создать отдельный сетап с совпадающим названием
            </label>
          </div>
        ) : <p className="form-hint">Совпадающие отображаемые названия разрешены, но перед созданием будет показано предупреждение.</p>}
      </form>
    </Modal>
  )
}
