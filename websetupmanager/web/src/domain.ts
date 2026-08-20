export type SetupStatus = 'draft' | 'ready' | 'attention' | 'archived'
export type ArtifactRole = 'program' | 'setup_sheet'

export interface Artifact {
  artifactId: string
  setupId: string
  role: ArtifactRole
  displayName: string
  mediaType: string
  byteSize: number
  version: string
  position: number
  primary: boolean
  state: 'available' | 'missing' | 'changed' | 'corrupt' | 'unavailable'
  createdAt: string
  updatedAt: string
}

export interface Setup {
  setupId: string
  libraryId: string
  name: string
  description?: string
  status: SetupStatus
  archivedFromStatus?: SetupStatus
  revision: number
  source: 'created' | 'imported' | 'duplicated'
  sourceSetupId?: string
  importSessionId?: string
  artifacts: Artifact[]
  notReadyReasons?: string[]
  createdAt: string
  updatedAt: string
}

export interface SetupSummary {
  setupId: string
  name: string
  description?: string
  status: SetupStatus
  revision: number
  programCount: number
  hasSetupSheet: boolean
  isCurrent: boolean
  notReadyReasons?: string[]
  createdAt: string
  updatedAt: string
  lastOpenedAt?: string
}

export interface CurrentSetup {
  libraryId: string
  setupId: string
  revisionSelected: number
  selectedAt: string
}

export type JobState = 'queued' | 'running' | 'cancelling' | 'succeeded' | 'failed' | 'cancelled' | 'conflict'

export interface Job {
  jobId: string
  kind: string
  setupId?: string
  state: JobState
  progress: {
    completedBytes: number
    totalBytes?: number
    completedItems: number
    totalItems?: number
  }
  errorCode?: string
  result?: unknown
  createdAt: string
  startedAt?: string
  completedAt?: string
}

export interface ValidationIssue {
  code: string
  severity: 'error' | 'warning'
  message: string
  artifactId?: string
  action?: string
}

export interface ValidationResult {
  validationRunId: string
  setupId: string
  revision: number
  state: 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled' | 'conflict'
  issues: ValidationIssue[]
}

export interface RecentSetup {
  libraryId: string
  setupId: string
  setupName: string
  setupStatus: SetupStatus
  lastArtifactId?: string
  lastLine?: number
  lastOpenedAt: string
}

export interface UIState {
  clientId: string
  screen: 'library' | 'detail'
  selectedSetupId?: string
  selectedArtifactId?: string
  filters: Record<string, unknown>
  view: Record<string, unknown>
  updatedAt?: string
}

export interface ImportArtifact {
  importArtifactId: string
  artifactId?: string
  role: ArtifactRole
  displayName: string
  byteSize: number
  bytes: number
  state: 'pending' | 'uploading' | 'staged' | 'excluded' | 'published' | 'failed'
  errorCode?: string
}

export interface ImportSession {
  importSessionId: string
  jobId?: string
  name: string
  description?: string
  state: 'staging' | 'committing' | 'succeeded' | 'draft_saved' | 'failed' | 'cancelled' | 'conflict'
  artifacts: ImportArtifact[]
  bytes: number
  setupId?: string
  errorCode?: string
  expiresAt: string
  createdAt: string
  updatedAt: string
}
