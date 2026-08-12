package main

import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/ymiroshnychenko668/linuxcncsetup/tui/internal/ui"
)

func main() {
	program := tea.NewProgram(ui.New())
	if _, err := program.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "linuxcncsetup: %v\n", err)
		os.Exit(1)
	}
}
