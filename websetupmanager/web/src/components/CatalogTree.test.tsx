import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { CatalogSnapshot } from '../api'
import { CatalogTree, flattenCatalogTree } from './CatalogTree'

const catalog: CatalogSnapshot = {
  destination: { rootLabel: 'LinuxCNC PROGRAM_PREFIX', rootDisplay: '~/linuxcnc/nc_files' },
  generation: '7',
  folders: [
    { folderId: 'orders', name: 'Заказы', relativePath: 'Заказы', revision: 1 },
    { folderId: 'year', parentFolderId: 'orders', name: '2026', relativePath: 'Заказы/2026', revision: 2 },
  ],
  setups: [
    {
      setupId: 'root-setup', name: 'Образец', revision: 1, program: null, setupSheet: null,
      updatedAt: '2026-08-21T08:00:00Z',
    },
    {
      setupId: 'bracket', folderId: 'year', name: 'Кронштейн', description: 'Операция 20', revision: 3,
      program: {
        artifactId: 'program', displayName: 'bracket.ngc', mediaType: 'text/x-gcode', byteSize: 12,
        version: 'v1', relativePath: 'Заказы/2026/bracket.ngc',
      },
      setupSheet: null, programRelativePath: 'Заказы/2026/bracket.ngc', updatedAt: '2026-08-21T08:00:00Z',
    },
  ],
}

function callbacks() {
  return {
    onExpandedChange: vi.fn(), onActivateRoot: vi.fn(), onActivateFolder: vi.fn(), onActivateSetup: vi.fn(),
    onRenameFolder: vi.fn(), onRenameSetup: vi.fn(), onDeleteFolder: vi.fn(), onDeleteSetup: vi.fn(),
  }
}

describe('catalog tree', () => {
  it('flattens only expanded branches and reveals matching setup ancestors during search', () => {
    expect(flattenCatalogTree(catalog, new Set(), '').map((node) => node.key)).toEqual([
      'root', 'folder:orders', 'setup:root-setup',
    ])
    expect(flattenCatalogTree(catalog, new Set(['orders', 'year']), '').map((node) => node.key)).toEqual([
      'root', 'folder:orders', 'folder:year', 'setup:bracket', 'setup:root-setup',
    ])
    expect(flattenCatalogTree(catalog, new Set(), 'bracket.ngc').map((node) => node.key)).toEqual([
      'root', 'folder:orders', 'folder:year', 'setup:bracket',
    ])
  })

  it('keeps cyclic malformed folders visible without recursing forever', () => {
    const malformed: CatalogSnapshot = {
      ...catalog,
      setups: [],
      folders: [
        { folderId: 'a', parentFolderId: 'b', name: 'A', relativePath: 'A', revision: 1 },
        { folderId: 'b', parentFolderId: 'a', name: 'B', relativePath: 'B', revision: 1 },
      ],
    }
    expect(flattenCatalogTree(malformed, new Set(['a', 'b']), '').map((node) => node.key)).toEqual([
      'root', 'folder:a', 'folder:b',
    ])
  })

  it('implements roving focus and standard tree keyboard actions', async () => {
    const user = userEvent.setup()
    const actions = callbacks()
    render(<CatalogTree
      catalog={catalog}
      query=""
      expandedFolderIds={new Set(['orders', 'year'])}
      activeFolderId="orders"
      onExpandedChange={actions.onExpandedChange}
      onActivateRoot={actions.onActivateRoot}
      onActivateFolder={actions.onActivateFolder}
      onActivateSetup={actions.onActivateSetup}
      onRenameFolder={actions.onRenameFolder}
      onRenameSetup={actions.onRenameSetup}
      onDeleteFolder={actions.onDeleteFolder}
      onDeleteSetup={actions.onDeleteSetup}
    />)

    const orders = screen.getByRole('treeitem', { name: 'Заказы' })
    orders.focus()
    await user.keyboard('{ArrowDown}{ArrowDown}')
    const bracket = screen.getByRole('treeitem', { name: /Кронштейн/ })
    expect(bracket).toHaveFocus()
    await user.keyboard('{Enter}')
    expect(actions.onActivateSetup).toHaveBeenCalledWith(expect.objectContaining({ setupId: 'bracket' }))
    await user.keyboard('{F2}{Delete}')
    expect(actions.onRenameSetup).toHaveBeenCalledWith(expect.objectContaining({ setupId: 'bracket' }))
    expect(actions.onDeleteSetup).toHaveBeenCalledWith(expect.objectContaining({ setupId: 'bracket' }))
    await user.keyboard('{ArrowLeft}')
    expect(screen.getByRole('treeitem', { name: '2026' })).toHaveFocus()
  })
})
