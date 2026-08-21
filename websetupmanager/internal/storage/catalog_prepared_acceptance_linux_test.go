//go:build linux

package storage

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestCatalogCreateFolderPreparedCallbackFailureLeavesNoVisibleOrHiddenEntry(t *testing.T) {
	catalog, _, root := testCatalog(t)
	operationID := strings.Repeat("c", 32)
	callbackFailure := errors.New("persist journal identity")
	prepared, err := catalog.CreateFolderPrepared("orders", operationID, func(object *Object) error {
		if object == nil || object.Identity.Device == 0 || object.Identity.Inode == 0 {
			t.Fatalf("callback received unbound object: %#v", object)
		}
		return callbackFailure
	})
	if !errors.Is(err, callbackFailure) || prepared != nil {
		t.Fatalf("CreateFolderPrepared = %#v, %v", prepared, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("callback rollback leaked public/create/gc entry: %v", names)
	}
}
