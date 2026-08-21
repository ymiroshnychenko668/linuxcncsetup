import type { SVGProps } from 'react'

type Props = SVGProps<SVGSVGElement>

const base: Props = {
  width: 16,
  height: 16,
  viewBox: '0 0 24 24',
  fill: 'none',
  stroke: 'currentColor',
  strokeWidth: 1.8,
  strokeLinecap: 'round',
  strokeLinejoin: 'round',
  'aria-hidden': true,
}

function Icon({ children, ...props }: Props) {
  return <svg {...base} {...props}>{children}</svg>
}

export function FolderIcon(props: Props) {
  return <Icon {...props}><path d="M3 7a2 2 0 0 1 2-2h5l2 2h7a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7Z" /></Icon>
}

export function ProgramIcon(props: Props) {
  return <Icon {...props}><path d="M7 3h7l4 4v14H7z" /><path d="M14 3v5h5M10 12l-2 2 2 2M15 12l2 2-2 2M13 11l-1 6" /></Icon>
}

export function SheetIcon(props: Props) {
  return <Icon {...props}><path d="M6 3h9l4 4v14H6z" /><path d="M15 3v5h5M9 12h7M9 16h7" /></Icon>
}

export function PlusIcon(props: Props) {
  return <Icon {...props}><path d="M12 5v14M5 12h14" /></Icon>
}

export function UploadIcon(props: Props) {
  return <Icon {...props}><path d="M12 16V4M7 9l5-5 5 5M5 20h14" /></Icon>
}

export function RefreshIcon(props: Props) {
  return <Icon {...props}><path d="M20 7v5h-5M4 17v-5h5" /><path d="M6.2 9a7 7 0 0 1 11.5-2.6L20 9M4 15l2.3 2.6A7 7 0 0 0 17.8 15" /></Icon>
}

export function EditIcon(props: Props) {
  return <Icon {...props}><path d="M12 20h9M16.5 3.5a2.1 2.1 0 0 1 3 3L8 18l-4 1 1-4Z" /></Icon>
}

export function TrashIcon(props: Props) {
  return <Icon {...props}><path d="M4 7h16M9 7V4h6v3M18 7l-1 13H7L6 7M10 11v5M14 11v5" /></Icon>
}

export function SearchIcon(props: Props) {
  return <Icon {...props}><circle cx="11" cy="11" r="6" /><path d="m16 16 4 4" /></Icon>
}

export function ChevronIcon({ direction = 'right', ...props }: Props & { direction?: 'right' | 'down' }) {
  return <Icon {...props} style={{ ...props.style, transform: direction === 'down' ? 'rotate(90deg)' : undefined }}><path d="m9 18 6-6-6-6" /></Icon>
}

export function MenuIcon(props: Props) {
  return <Icon {...props}><path d="M4 7h16M4 12h16M4 17h16" /></Icon>
}

export function CloseIcon(props: Props) {
  return <Icon {...props}><path d="m6 6 12 12M18 6 6 18" /></Icon>
}

export function LogOutIcon(props: Props) {
  return <Icon {...props}><path d="M10 5H5a2 2 0 0 0-2 2v10a2 2 0 0 0 2 2h5M14 8l4 4-4 4M18 12H8" /></Icon>
}
