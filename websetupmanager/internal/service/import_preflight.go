package service

import (
	"context"
	"sort"
	"strings"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
)

const maximumImportPreflightItems = 1000

type ImportPreflightItem struct {
	ClientID    string              `json:"clientId"`
	Role        domain.ArtifactRole `json:"role"`
	DisplayName string              `json:"displayName"`
}

type ImportPreflightInput struct {
	Items []ImportPreflightItem `json:"items"`
}

type ImportPreflightItemResult struct {
	ClientID    string           `json:"clientId"`
	DisplayName string           `json:"displayName,omitempty"`
	ErrorCode   domain.ErrorCode `json:"errorCode,omitempty"`
}

type ImportNameCollision struct {
	ClientIDs []string `json:"clientIds"`
}

type ImportPreflightResult struct {
	Items      []ImportPreflightItemResult `json:"items"`
	Collisions []ImportNameCollision       `json:"collisions"`
}

// PreflightImport applies the exact Unicode normalization and full case-fold
// used by persistence, before callers stream any content. Fold keys are kept
// internal; the public result contains only caller IDs and canonical basenames.
func (s *Service) PreflightImport(ctx context.Context, input ImportPreflightInput) (*ImportPreflightResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(input.Items) == 0 || len(input.Items) > maximumImportPreflightItems {
		return nil, domain.NewError(domain.CodeInvalidContent, "import preflight item count is invalid")
	}
	result := &ImportPreflightResult{Items: make([]ImportPreflightItemResult, len(input.Items)), Collisions: []ImportNameCollision{}}
	seenIDs := make(map[string]struct{}, len(input.Items))
	groups := make(map[string][]string)
	for index, item := range input.Items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		clientID := strings.TrimSpace(item.ClientID)
		if clientID == "" || len(clientID) > 128 {
			return nil, domain.NewError(domain.CodeInvalidContent, "import preflight client ID is invalid")
		}
		if _, duplicate := seenIDs[clientID]; duplicate {
			return nil, domain.NewError(domain.CodeInvalidContent, "import preflight client IDs must be unique")
		}
		seenIDs[clientID] = struct{}{}
		entry := ImportPreflightItemResult{ClientID: clientID}
		if !item.Role.Valid() {
			entry.ErrorCode = domain.CodeInvalidContent
			result.Items[index] = entry
			continue
		}
		name, err := domain.NormalizeArtifactName(item.DisplayName)
		if err != nil {
			entry.ErrorCode = safeErrorCode(err)
			result.Items[index] = entry
			continue
		}
		entry.DisplayName = name
		if item.Role == domain.ArtifactRoleProgram {
			err = s.gcode.ValidateExtension(name)
		} else {
			_, err = setupSheetMediaType(name)
		}
		if err != nil {
			entry.ErrorCode = safeErrorCode(err)
			result.Items[index] = entry
			continue
		}
		key, err := domain.ArtifactNameKey(name)
		if err != nil {
			entry.ErrorCode = safeErrorCode(err)
		} else {
			groups[key] = append(groups[key], clientID)
		}
		result.Items[index] = entry
	}
	for _, ids := range groups {
		if len(ids) < 2 {
			continue
		}
		sort.Strings(ids)
		result.Collisions = append(result.Collisions, ImportNameCollision{ClientIDs: ids})
	}
	sort.Slice(result.Collisions, func(left, right int) bool {
		return strings.Join(result.Collisions[left].ClientIDs, "\x00") < strings.Join(result.Collisions[right].ClientIDs, "\x00")
	})
	return result, nil
}
