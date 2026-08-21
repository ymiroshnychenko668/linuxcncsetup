package domain

import "time"

// CatalogFolder is an operator-created grouping below LinuxCNC's configured
// PROGRAM_PREFIX. RelativePath is logical and safe to display; host paths are absent.
type CatalogFolder struct {
	ID             string    `json:"folderId"`
	ParentFolderID string    `json:"parentFolderId,omitempty"`
	Name           string    `json:"name"`
	RelativePath   string    `json:"relativePath"`
	Revision       Revision  `json:"revision"`
	CreatedAt      time.Time `json:"createdAt,omitempty"`
	UpdatedAt      time.Time `json:"updatedAt,omitempty"`
}

// CatalogFile is one of the two bounded setup components.
type CatalogFile struct {
	ID             string       `json:"artifactId"`
	Role           ArtifactRole `json:"role"`
	DisplayName    string       `json:"displayName"`
	MediaType      string       `json:"mediaType"`
	ByteSize       int64        `json:"byteSize"`
	Version        string       `json:"version"`
	RelativePath   string       `json:"relativePath"`
	SHA256         string       `json:"-"`
	IdentityDevice uint64       `json:"-"`
	IdentityInode  uint64       `json:"-"`
	CreatedAt      time.Time    `json:"createdAt,omitempty"`
	UpdatedAt      time.Time    `json:"updatedAt,omitempty"`
}

// CatalogSetup is a catalog entry, not a readiness state machine. Its two
// components are independently nullable so incomplete uploads remain useful.
type CatalogSetup struct {
	ID                     string       `json:"setupId"`
	FolderID               string       `json:"folderId,omitempty"`
	Name                   string       `json:"name"`
	Description            string       `json:"description,omitempty"`
	Revision               Revision     `json:"revision"`
	Program                *CatalogFile `json:"program"`
	SetupSheet             *CatalogFile `json:"setupSheet"`
	ProgramRelativePath    string       `json:"programRelativePath,omitempty"`
	SetupSheetRelativePath string       `json:"setupSheetRelativePath,omitempty"`
	CreatedAt              time.Time    `json:"createdAt,omitempty"`
	UpdatedAt              time.Time    `json:"updatedAt"`
}

type CatalogDestination struct {
	RootLabel   string `json:"rootLabel"`
	RootDisplay string `json:"rootDisplay"`
}

type CatalogTree struct {
	Destination CatalogDestination `json:"destination"`
	Generation  string             `json:"generation"`
	Folders     []CatalogFolder    `json:"folders"`
	Setups      []CatalogSetup     `json:"setups"`
}
