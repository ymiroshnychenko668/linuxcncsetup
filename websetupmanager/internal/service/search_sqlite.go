package service

import (
	"database/sql/driver"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
	"modernc.org/sqlite"
)

// SQLite's built-in NOCASE collation is ASCII-only. Register the setup
// manager's Unicode case-folding function before main opens the database so
// library search is correct for operator-visible names in every supported
// script without loading the library into Go memory.
func init() {
	sqlite.MustRegisterDeterministicScalarFunction(
		"wsm_casefold",
		1,
		func(_ *sqlite.FunctionContext, arguments []driver.Value) (driver.Value, error) {
			if len(arguments) != 1 || arguments[0] == nil {
				return nil, nil
			}
			value, ok := arguments[0].(string)
			if !ok {
				return nil, nil
			}
			return setupSearchFolder.String(value), nil
		},
	)
	sqlite.MustRegisterDeterministicScalarFunction(
		"wsm_setup_name_key",
		1,
		func(_ *sqlite.FunctionContext, arguments []driver.Value) (driver.Value, error) {
			if len(arguments) != 1 || arguments[0] == nil {
				return nil, nil
			}
			value, ok := arguments[0].(string)
			if !ok {
				return nil, nil
			}
			key, err := domain.SetupNameKey(value)
			if err != nil {
				return nil, nil
			}
			return key, nil
		},
	)
}
