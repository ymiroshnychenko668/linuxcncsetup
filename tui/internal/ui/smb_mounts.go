package ui

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"github.com/ymiroshnychenko668/linuxcncsetup/tui/internal/playbooks"
)

const (
	smbDefaultServer     = "10.0.1.246"
	smbDefaultShare      = "share"
	smbDefaultMountPoint = "/mnt/smb_share"
	smbLegacyMountID     = "legacy"
	smbFstabPath         = "/etc/fstab"
	smbMountInfoPath     = "/proc/self/mountinfo"
)

const (
	smbServerField = iota
	smbShareField
	smbMountPointField
	smbFieldCount
)

var (
	smbSharePattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._$-]{0,79}$`)
	smbMountPointPattern = regexp.MustCompile(`^/(?:mnt|media)(?:/[A-Za-z0-9][A-Za-z0-9._-]{0,79})+$`)
	smbMountIDPattern    = regexp.MustCompile(`^[a-f0-9]{16}$`)
	smbBeginPattern      = regexp.MustCompile(`^# BEGIN LINUXCNCSETUP MANAGED SMB MOUNT ([a-f0-9]{16})$`)
	smbEndPattern        = regexp.MustCompile(`^# END LINUXCNCSETUP MANAGED SMB MOUNT ([a-f0-9]{16})$`)
)

// smbMount describes one fstab entry owned by LinuxCNC Setup. Passwords are
// intentionally not part of this model: this workflow manages guest shares.
type smbMount struct {
	ID         string
	Server     string
	Share      string
	MountPoint string
	Mounted    bool
	Automount  bool
}

func (mount smbMount) source() string {
	return fmt.Sprintf("//%s/%s", mount.Server, mount.Share)
}

func (mount smbMount) state() string {
	switch {
	case mount.Mounted:
		return "mounted"
	case mount.Automount:
		return "automount ready"
	default:
		return "configured, inactive"
	}
}

type smbMountDraft struct {
	server             string
	share              string
	mountPoint         string
	focusedField       int
	previousID         string
	previousServer     string
	previousShare      string
	previousMountPoint string
}

func newSMBMountDraft() smbMountDraft {
	return smbMountDraft{
		server:     smbDefaultServer,
		share:      smbDefaultShare,
		mountPoint: smbDefaultMountPoint,
	}
}

func editSMBMountDraft(mount smbMount) smbMountDraft {
	return smbMountDraft{
		server:             mount.Server,
		share:              mount.Share,
		mountPoint:         mount.MountPoint,
		previousID:         mount.ID,
		previousServer:     mount.Server,
		previousShare:      mount.Share,
		previousMountPoint: mount.MountPoint,
	}
}

func (draft smbMountDraft) normalized() smbMountDraft {
	draft.server = strings.TrimSpace(draft.server)
	draft.share = strings.TrimSpace(strings.Trim(draft.share, "/"))
	draft.mountPoint = strings.TrimSpace(draft.mountPoint)
	draft.previousID = strings.TrimSpace(draft.previousID)
	draft.previousServer = strings.TrimSpace(draft.previousServer)
	draft.previousShare = strings.TrimSpace(strings.Trim(draft.previousShare, "/"))
	draft.previousMountPoint = strings.TrimSpace(draft.previousMountPoint)
	return draft
}

func (draft smbMountDraft) editing() bool {
	return draft.normalized().previousID != ""
}

func (draft smbMountDraft) mount() smbMount {
	draft = draft.normalized()
	mount := smbMount{
		Server:     draft.server,
		Share:      draft.share,
		MountPoint: draft.mountPoint,
	}
	mount.ID = smbMountID(mount)
	return mount
}

func (draft smbMountDraft) previousMount() (smbMount, bool) {
	draft = draft.normalized()
	if draft.previousID == "" {
		return smbMount{}, false
	}
	return smbMount{
		ID:         draft.previousID,
		Server:     draft.previousServer,
		Share:      draft.previousShare,
		MountPoint: draft.previousMountPoint,
	}, true
}

func (draft smbMountDraft) fieldValue(field int) string {
	switch field {
	case smbServerField:
		return draft.server
	case smbShareField:
		return draft.share
	case smbMountPointField:
		return draft.mountPoint
	default:
		return ""
	}
}

func (draft *smbMountDraft) setFieldValue(field int, value string) {
	switch field {
	case smbServerField:
		draft.server = value
	case smbShareField:
		draft.share = value
	case smbMountPointField:
		draft.mountPoint = value
	}
}

func smbMountID(mount smbMount) string {
	digest := sha256.Sum256([]byte(mount.source() + "\x00" + mount.MountPoint))
	return fmt.Sprintf("%x", digest[:8])
}

func validateSMBConnectionDraft(draft smbMountDraft) error {
	draft = draft.normalized()
	address := net.ParseIP(draft.server)
	if address == nil || address.To4() == nil {
		return fmt.Errorf("server must be a valid IPv4 address")
	}
	ipv4 := address.To4()
	if ipv4[0] == 0 || ipv4[0] >= 224 || address.IsLoopback() {
		return fmt.Errorf("server must be a usable non-loopback IPv4 address")
	}
	if !smbSharePattern.MatchString(draft.share) {
		return fmt.Errorf("share/folder must use 1-80 letters, digits, dots, underscores, dashes, or $")
	}
	return nil
}

func validateSMBMountDraft(draft smbMountDraft, existing []smbMount) error {
	draft = draft.normalized()
	if err := validateSMBConnectionDraft(draft); err != nil {
		return err
	}
	if draft.mountPoint != path.Clean(draft.mountPoint) ||
		!smbMountPointPattern.MatchString(draft.mountPoint) {
		return fmt.Errorf("mount directory must be a clean absolute path below /mnt or /media")
	}

	for _, mount := range existing {
		if mount.ID == draft.previousID {
			continue
		}
		if mount.MountPoint == draft.mountPoint {
			return fmt.Errorf("mount directory %s is already managed", draft.mountPoint)
		}
	}
	return nil
}

func parseSMBSource(source string) (server, share string, err error) {
	if !strings.HasPrefix(source, "//") {
		return "", "", fmt.Errorf("SMB source %q does not begin with //", source)
	}
	parts := strings.Split(strings.TrimPrefix(source, "//"), "/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("SMB source %q must contain one share/folder", source)
	}
	draft := smbMountDraft{server: parts[0], share: parts[1]}
	if err := validateSMBConnectionDraft(draft); err != nil {
		return "", "", fmt.Errorf("invalid managed SMB source %q: %w", source, err)
	}
	return parts[0], parts[1], nil
}

type smbFstabBlock struct {
	id      string
	legacy  bool
	content []string
}

func parseManagedSMBMounts(contents string) ([]smbMount, error) {
	var (
		mounts []smbMount
		block  *smbFstabBlock
	)

	scanner := bufio.NewScanner(strings.NewReader(contents))
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())

		beginID, begin, legacyBegin := parseSMBBeginMarker(line)
		if begin {
			if block != nil {
				return nil, fmt.Errorf("nested managed SMB marker at /etc/fstab line %d", lineNumber)
			}
			block = &smbFstabBlock{id: beginID, legacy: legacyBegin}
			continue
		}

		endID, end, legacyEnd := parseSMBEndMarker(line)
		if end {
			if block == nil {
				return nil, fmt.Errorf("unmatched managed SMB end marker at /etc/fstab line %d", lineNumber)
			}
			if block.id != endID || block.legacy != legacyEnd {
				return nil, fmt.Errorf("mismatched managed SMB markers ending at /etc/fstab line %d", lineNumber)
			}
			mount, err := parseSMBFstabBlock(*block)
			if err != nil {
				return nil, fmt.Errorf("managed SMB block ending at /etc/fstab line %d: %w", lineNumber, err)
			}
			mounts = append(mounts, mount)
			block = nil
			continue
		}

		if block != nil {
			block.content = append(block.content, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read /etc/fstab: %w", err)
	}
	if block != nil {
		return nil, fmt.Errorf("managed SMB block %q has no end marker", block.id)
	}

	seenIDs := make(map[string]struct{}, len(mounts))
	seenMountPoints := make(map[string]struct{}, len(mounts))
	for _, mount := range mounts {
		if _, found := seenIDs[mount.ID]; found {
			return nil, fmt.Errorf("managed SMB mount ID %q is duplicated", mount.ID)
		}
		if _, found := seenMountPoints[mount.MountPoint]; found {
			return nil, fmt.Errorf("managed SMB mount directory %q is duplicated", mount.MountPoint)
		}
		seenIDs[mount.ID] = struct{}{}
		seenMountPoints[mount.MountPoint] = struct{}{}
	}

	sort.Slice(mounts, func(left, right int) bool {
		return mounts[left].MountPoint < mounts[right].MountPoint
	})
	return mounts, nil
}

func parseSMBBeginMarker(line string) (string, bool, bool) {
	if line == "# BEGIN LINUXCNCSETUP MANAGED SMB SHARE" {
		return smbLegacyMountID, true, true
	}
	matches := smbBeginPattern.FindStringSubmatch(line)
	if len(matches) == 2 {
		return matches[1], true, false
	}
	return "", false, false
}

func parseSMBEndMarker(line string) (string, bool, bool) {
	if line == "# END LINUXCNCSETUP MANAGED SMB SHARE" {
		return smbLegacyMountID, true, true
	}
	matches := smbEndPattern.FindStringSubmatch(line)
	if len(matches) == 2 {
		return matches[1], true, false
	}
	return "", false, false
}

func parseSMBFstabBlock(block smbFstabBlock) (smbMount, error) {
	var entries []string
	for _, line := range block.content {
		if line != "" && !strings.HasPrefix(line, "#") {
			entries = append(entries, line)
		}
	}
	if len(entries) != 1 {
		return smbMount{}, fmt.Errorf("expected exactly one fstab entry, found %d", len(entries))
	}

	fields := strings.Fields(entries[0])
	if len(fields) != 6 || fields[2] != "cifs" || fields[4] != "0" || fields[5] != "0" {
		return smbMount{}, fmt.Errorf("entry is not one complete CIFS fstab record")
	}
	server, share, err := parseSMBSource(fields[0])
	if err != nil {
		return smbMount{}, err
	}
	mount := smbMount{
		ID:         block.id,
		Server:     server,
		Share:      share,
		MountPoint: fields[1],
	}
	if !block.legacy && !smbMountIDPattern.MatchString(mount.ID) {
		return smbMount{}, fmt.Errorf("invalid managed mount ID %q", mount.ID)
	}
	if mount.MountPoint != path.Clean(mount.MountPoint) ||
		!smbMountPointPattern.MatchString(mount.MountPoint) {
		return smbMount{}, fmt.Errorf("unsafe managed mount directory %q", mount.MountPoint)
	}
	return mount, nil
}

func applySMBMountInfo(mounts []smbMount, contents string) {
	byMountPoint := make(map[string]*smbMount, len(mounts))
	for index := range mounts {
		byMountPoint[mounts[index].MountPoint] = &mounts[index]
	}

	scanner := bufio.NewScanner(strings.NewReader(contents))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		separator := -1
		for index, field := range fields {
			if field == "-" {
				separator = index
				break
			}
		}
		if separator < 0 || len(fields) <= separator+1 || len(fields) <= 4 {
			continue
		}
		mount := byMountPoint[unescapeMountInfoField(fields[4])]
		if mount == nil {
			continue
		}
		switch fields[separator+1] {
		case "cifs":
			mount.Mounted = true
		case "autofs":
			mount.Automount = true
		}
	}
}

func unescapeMountInfoField(value string) string {
	replacer := strings.NewReplacer(
		`\040`, " ",
		`\011`, "\t",
		`\012`, "\n",
		`\134`, `\`,
	)
	return replacer.Replace(value)
}

func discoverSMBMounts(fstabPath, mountInfoPath string) ([]smbMount, error) {
	fstab, err := os.ReadFile(fstabPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", fstabPath, err)
	}
	mounts, err := parseManagedSMBMounts(string(fstab))
	if err != nil {
		return nil, err
	}
	mountInfo, err := os.ReadFile(mountInfoPath)
	if err == nil {
		applySMBMountInfo(mounts, string(mountInfo))
	}
	return mounts, nil
}

func smbMountListSections(mounts []smbMount) []section {
	sections := make([]section, 0, len(mounts)+3)
	for _, mount := range mounts {
		sections = append(sections, section{
			title:       fmt.Sprintf("%s on %s", mount.Share, mount.Server),
			description: fmt.Sprintf("%s — %s", mount.MountPoint, mount.state()),
			action:      actionSMBSelect,
			value:       mount.ID,
		})
	}
	sections = append(sections,
		section{
			title:       "Add SMB mount",
			description: "Enter an IPv4 address, remote share/folder, and local mount directory.",
			action:      actionSMBAdd,
		},
		section{
			title:       "Refresh",
			description: "Reload managed entries and their current mount state.",
			action:      actionSMBRefresh,
		},
		section{
			title:       "← Back",
			description: "Return to Configuration.",
			action:      actionBack,
		},
	)
	return sections
}

var smbMountDetailSections = []section{
	{
		title:       "Mount / reconnect",
		description: "Activate the persistent mount now.",
		action:      actionSMBMount,
	},
	{
		title:       "Test connection",
		description: "Verify guest access to the exact server and share without mounting it.",
		action:      actionSMBTest,
	},
	{
		title:       "Edit",
		description: "Change the server, share/folder, or local mount directory.",
		action:      actionSMBEdit,
	},
	{
		title:       "Unmount",
		description: "Unmount for this boot while retaining persistent configuration.",
		action:      actionSMBUnmount,
	},
	{
		title:       "Delete",
		description: "Unmount and remove this LinuxCNC Setup-owned fstab entry.",
		action:      actionSMBRemove,
	},
	{
		title:       "← Back",
		description: "Return to the managed SMB mount list.",
		action:      actionBack,
	},
}

var smbMountFormSections = []section{{
	title:       "SMB connection",
	description: "Edit the connection fields, test them, then save and mount.",
	action:      actionSMBSave,
}}

func (m *Model) refreshSMBMounts(report bool) error {
	mounts, err := discoverSMBMounts(smbFstabPath, smbMountInfoPath)
	if err != nil {
		m.smbMounts = nil
		m.smbSelectedID = ""
		if m.page == menuSMBMounts {
			m.selected = min(m.selected, len(smbMountListSections(nil))-1)
		}
		m.status = fmt.Sprintf("Cannot read managed SMB mounts: %v", err)
		return err
	}
	m.smbMounts = mounts
	if m.page == menuSMBMounts {
		m.selected = min(m.selected, len(smbMountListSections(mounts))-1)
	}
	if m.smbSelectedID != "" {
		if _, found := m.selectedSMBMount(); !found {
			m.smbSelectedID = ""
		}
	}
	if report {
		m.status = fmt.Sprintf("Loaded %d managed SMB mount(s).", len(mounts))
	}
	return nil
}

func (m Model) selectedSMBMount() (smbMount, bool) {
	for _, mount := range m.smbMounts {
		if mount.ID == m.smbSelectedID {
			return mount, true
		}
	}
	return smbMount{}, false
}

func (m *Model) updateSMBMountForm(message tea.KeyPressMsg) bool {
	key := message.Key()
	if key.Code == tea.KeyEnter || key.Code == tea.KeyReturn ||
		key.Code == tea.KeyEscape || message.String() == "ctrl+c" ||
		message.String() == "f5" {
		return false
	}

	switch message.String() {
	case "tab", "down":
		m.smbDraft.focusedField = (m.smbDraft.focusedField + 1) % smbFieldCount
		m.status = ""
		return true
	case "shift+tab", "up":
		m.smbDraft.focusedField = (m.smbDraft.focusedField + smbFieldCount - 1) % smbFieldCount
		m.status = ""
		return true
	case "backspace":
		value := []rune(m.smbDraft.fieldValue(m.smbDraft.focusedField))
		if len(value) > 0 {
			m.smbDraft.setFieldValue(m.smbDraft.focusedField, string(value[:len(value)-1]))
		}
		m.status = ""
		return true
	case "ctrl+u":
		m.smbDraft.setFieldValue(m.smbDraft.focusedField, "")
		m.status = ""
		return true
	}

	if key.Text == "" {
		return false
	}
	for _, character := range key.Text {
		if unicode.IsControl(character) {
			m.status = "Line breaks and control characters are not allowed in SMB fields."
			return true
		}
	}
	m.smbDraft.setFieldValue(
		m.smbDraft.focusedField,
		m.smbDraft.fieldValue(m.smbDraft.focusedField)+key.Text,
	)
	m.status = ""
	return true
}

func renderSMBMountSummary(mount smbMount) string {
	return fmt.Sprintf(
		"Share:       %s\nMount point: %s\nState:       %s",
		mount.source(),
		mount.MountPoint,
		mount.state(),
	)
}

func (m Model) renderSMBListAction(current section) []string {
	if current.action == actionSMBSelect {
		mount, found := m.selectedSMBMountByID(current.value)
		if !found {
			return []string{"This managed SMB entry is no longer available. Press r to refresh."}
		}
		return []string{
			renderSMBMountSummary(mount),
			"",
			"Press Enter to mount, test, edit, unmount, or delete this entry.",
		}
	}
	switch current.action {
	case actionSMBAdd:
		return []string{
			"Press Enter to provide the SMB server IPv4 address,",
			"remote share/folder, and local mount directory.",
			"",
			"This workflow uses guest access and stores no password.",
		}
	case actionSMBRefresh:
		return []string{"Press Enter or r to reload /etc/fstab and the current kernel mount state."}
	default:
		return []string{"Select a managed SMB mount or add a new one."}
	}
}

func (m Model) selectedSMBMountByID(id string) (smbMount, bool) {
	for _, mount := range m.smbMounts {
		if mount.ID == id {
			return mount, true
		}
	}
	return smbMount{}, false
}

func (m *Model) selectSMBMount(id string) {
	for index, candidate := range smbMountListSections(m.smbMounts) {
		if candidate.action == actionSMBSelect && candidate.value == id {
			m.selected = index
			return
		}
	}
}

func (m *Model) finishSMBMountAction(action sectionAction, actionErr error) {
	if actionErr != nil {
		return
	}
	success := actionSuccessMessage(action)
	switch action {
	case actionSMBSave:
		newID := m.smbDraft.mount().ID
		m.smbSelectedID = newID
		m.openPage(menuSMBMounts)
		if err := m.refreshSMBMounts(false); err != nil {
			return
		}
		m.selectSMBMount(newID)
	case actionSMBRemove:
		m.smbSelectedID = ""
		m.openPage(menuSMBMounts)
		if err := m.refreshSMBMounts(false); err != nil {
			return
		}
	case actionSMBMount, actionSMBUnmount:
		if err := m.refreshSMBMounts(false); err != nil {
			return
		}
		if _, found := m.selectedSMBMount(); !found {
			m.openPage(menuSMBMounts)
		}
	case actionSMBTest:
		// A connection test does not change persistent or kernel mount state.
	}
	m.status = success
}

func (m Model) renderSMBMountForm(confirming bool) []string {
	draft := m.smbDraft
	action := m.smbPendingAction
	if action == actionNone {
		action = actionSMBSave
	}
	if confirming {
		mount := draft.mount()
		if action == actionSMBTest {
			return []string{
				warningStyle.Render("Test this SMB connection?"),
				"",
				fmt.Sprintf("Share: %s", mount.source()),
				"",
				"The test uses smbclient to open the exact share",
				"as guest and request a directory listing.",
				"It does not write files or change /etc/fstab.",
				"smbclient is installed first when missing.",
				"",
				"sudo will ask for your account password.",
				"Press y to test or n to cancel.",
			}
		}
		title := "Create and mount this SMB entry?"
		if draft.editing() {
			title = "Update and reconnect this SMB entry?"
		}
		return []string{
			warningStyle.Render(title),
			"",
			renderSMBMountSummary(mount),
			"",
			"Ansible writes only this entry's marked fstab block,",
			"enables systemd automounting, and mounts it now.",
			"Guest access is used; no password is stored.",
			"A changed existing mount is normally unmounted first.",
			"",
			"sudo will ask for your account password.",
			"Press y to save or n to cancel.",
		}
	}

	labels := []string{"Server IPv4", "Share / folder", "Local mount directory"}
	lines := []string{
		"Provide the exact SMB endpoint and where it should appear locally.",
		"Mount directories are limited to safe paths below /mnt or /media.",
		"",
	}
	for field, label := range labels {
		prefix := "  "
		if field == draft.focusedField {
			prefix = "› "
		}
		value := draft.fieldValue(field)
		if value == "" {
			value = "(required)"
		}
		lines = append(lines, fmt.Sprintf("%s%-23s %s", prefix, label+":", value))
	}
	return append(lines,
		"",
		"Ctrl+U clears the current field.",
		"F5 tests the entered server/share without saving.",
		"Enter reviews the persistent mount; Esc cancels.",
		"This workflow connects as guest and stores no password.",
	)
}

func (m Model) renderSMBMountAction(action sectionAction, confirming bool) []string {
	mount, found := m.selectedSMBMount()
	if !found {
		return []string{"The selected managed SMB mount no longer exists. Return to the list and refresh."}
	}
	lines := []string{renderSMBMountSummary(mount), ""}
	if !confirming {
		switch action {
		case actionSMBMount:
			return append(lines, "Press Enter to mount or reconnect this share now.")
		case actionSMBTest:
			return append(lines, "Press Enter to test exact guest access without changing the mount.")
		case actionSMBEdit:
			return append(lines, "Press Enter to edit this managed mount.")
		case actionSMBUnmount:
			return append(lines, "Press Enter to unmount it for this boot.", "", "The persistent entry is retained.")
		case actionSMBRemove:
			return append(lines, "Press Enter to delete this managed mount.")
		}
	}

	switch action {
	case actionSMBMount:
		return append(lines,
			warningStyle.Render("Mount this SMB share now?"),
			"",
			"Ansible starts its fstab-generated systemd automount",
			"and mount units, then verifies the expected CIFS source.",
			"Press y to continue or n to cancel.",
		)
	case actionSMBTest:
		return append(lines,
			warningStyle.Render("Test this SMB connection?"),
			"",
			"smbclient opens the exact share as guest and requests",
			"a read-only directory listing. /etc/fstab is unchanged.",
			"smbclient is installed first when missing.",
			"Press y to test or n to cancel.",
		)
	case actionSMBUnmount:
		return append(lines,
			warningStyle.Render("Unmount this SMB share now?"),
			"",
			"Ansible stops automounting first, then performs a normal",
			"unmount. A busy mount fails instead of being forced.",
			"The persistent entry remains for the next reboot.",
			"Press y to continue or n to cancel.",
		)
	case actionSMBRemove:
		return append(lines,
			warningStyle.Render("Delete this persistent SMB mount?"),
			"",
			"Ansible normally unmounts it and removes only this",
			"LinuxCNC Setup-owned fstab block. The local directory",
			"and every unrelated fstab entry are retained.",
			"Press y to continue or n to cancel.",
		)
	default:
		return append(lines, "This SMB mount action is not implemented.")
	}
}

func (m *Model) prepareSMBMountAction(action sectionAction) bool {
	switch action {
	case actionSMBSave:
		if err := validateSMBMountDraft(m.smbDraft, m.smbMounts); err != nil {
			m.status = fmt.Sprintf("Cannot save SMB mount: %v", err)
			return false
		}
	case actionSMBTest:
		if m.page == menuSMBMountForm {
			if err := validateSMBConnectionDraft(m.smbDraft); err != nil {
				m.status = fmt.Sprintf("Cannot test SMB connection: %v", err)
				return false
			}
		} else if _, found := m.selectedSMBMount(); !found {
			m.status = "Select a managed SMB mount first."
			return false
		}
	case actionSMBMount, actionSMBUnmount, actionSMBRemove:
		if _, found := m.selectedSMBMount(); !found {
			m.status = "Select a managed SMB mount first."
			return false
		}
	default:
		m.status = "This SMB mount action is not implemented."
		return false
	}

	if _, err := exec.LookPath("ansible-playbook"); err != nil {
		m.status = "Install Ansible first, then retry this action."
		return false
	}
	if _, err := targetUsername(); err != nil {
		m.status = fmt.Sprintf("Cannot manage SMB mounts: %v", err)
		return false
	}
	if os.Geteuid() != 0 {
		if _, err := exec.LookPath("sudo"); err != nil {
			m.status = "Cannot manage SMB mounts: sudo was not found."
			return false
		}
	}
	m.smbPendingAction = action
	return true
}

func smbMountOperation(action sectionAction) (string, bool) {
	switch action {
	case actionSMBSave:
		return "apply", true
	case actionSMBTest:
		return "test", true
	case actionSMBMount:
		return "mount", true
	case actionSMBUnmount:
		return "unmount", true
	case actionSMBRemove:
		return "remove", true
	default:
		return "", false
	}
}

func smbMountPlaybookVariables(
	targetUser string,
	action sectionAction,
	mount smbMount,
	previous *smbMount,
) (map[string]any, error) {
	operation, ok := smbMountOperation(action)
	if !ok {
		return nil, fmt.Errorf("unsupported SMB mount action: %d", action)
	}
	if err := validateSMBConnectionDraft(smbMountDraft{server: mount.Server, share: mount.Share}); err != nil {
		return nil, err
	}
	if action != actionSMBTest {
		if mount.ID != smbLegacyMountID && !smbMountIDPattern.MatchString(mount.ID) {
			return nil, fmt.Errorf("invalid SMB mount ID %q", mount.ID)
		}
		if mount.MountPoint != path.Clean(mount.MountPoint) ||
			!smbMountPointPattern.MatchString(mount.MountPoint) {
			return nil, fmt.Errorf("invalid SMB mount directory %q", mount.MountPoint)
		}
	}

	variables := map[string]any{
		"target_user":              targetUser,
		"smb_operation":            operation,
		"smb_mount_id":             mount.ID,
		"smb_server":               mount.Server,
		"smb_share":                mount.Share,
		"smb_source":               mount.source(),
		"smb_mount_point":          mount.MountPoint,
		"smb_previous_mount_id":    "",
		"smb_previous_source":      "",
		"smb_previous_mount_point": "",
	}
	if previous != nil {
		variables["smb_previous_mount_id"] = previous.ID
		variables["smb_previous_source"] = previous.source()
		variables["smb_previous_mount_point"] = previous.MountPoint
	}
	return variables, nil
}

func (m Model) smbMountRequest(action sectionAction) (smbMount, *smbMount, error) {
	if m.page == menuSMBMountForm {
		mount := m.smbDraft.mount()
		if action == actionSMBTest {
			mount.ID = ""
			mount.MountPoint = ""
			return mount, nil, nil
		}
		previous, editing := m.smbDraft.previousMount()
		if editing {
			return mount, &previous, nil
		}
		return mount, nil, nil
	}
	mount, found := m.selectedSMBMount()
	if !found {
		return smbMount{}, nil, fmt.Errorf("selected managed SMB mount no longer exists")
	}
	return mount, nil, nil
}

func (m Model) runSMBMountPlaybook(action sectionAction) tea.Cmd {
	targetUser, err := targetUsername()
	if err != nil {
		return func() tea.Msg { return actionFinishedMsg{action: action, err: err} }
	}
	mount, previous, err := m.smbMountRequest(action)
	if err != nil {
		return func() tea.Msg { return actionFinishedMsg{action: action, err: err} }
	}
	variables, err := smbMountPlaybookVariables(targetUser, action, mount, previous)
	if err != nil {
		return func() tea.Msg { return actionFinishedMsg{action: action, err: err} }
	}
	return runEmbeddedPlaybook(action, playbooks.SMBMounts, variables)
}

func smbMountActionName(action sectionAction) (string, bool) {
	switch action {
	case actionSMBSave:
		return "SMB mount save", true
	case actionSMBTest:
		return "SMB connection test", true
	case actionSMBMount:
		return "SMB share mount", true
	case actionSMBUnmount:
		return "SMB share unmount", true
	case actionSMBRemove:
		return "SMB mount deletion", true
	default:
		return "", false
	}
}

func smbMountRunningMessage(action sectionAction) (string, bool) {
	switch action {
	case actionSMBSave:
		return "Saving and mounting the SMB entry with Ansible...", true
	case actionSMBTest:
		return "Testing guest access to the SMB share...", true
	case actionSMBMount:
		return "Mounting the selected SMB share with Ansible...", true
	case actionSMBUnmount:
		return "Unmounting the selected SMB share with Ansible...", true
	case actionSMBRemove:
		return "Deleting the selected managed SMB mount with Ansible...", true
	default:
		return "", false
	}
}

func smbMountCancelledMessage(action sectionAction) (string, bool) {
	switch action {
	case actionSMBSave:
		return "SMB mount save cancelled.", true
	case actionSMBTest:
		return "SMB connection test cancelled.", true
	case actionSMBMount:
		return "SMB share mount cancelled.", true
	case actionSMBUnmount:
		return "SMB share unmount cancelled.", true
	case actionSMBRemove:
		return "SMB mount deletion cancelled.", true
	default:
		return "", false
	}
}

func smbMountSuccessMessage(action sectionAction) (string, bool) {
	switch action {
	case actionSMBSave:
		return "SMB mount saved, mounted, and configured for automatic mounting.", true
	case actionSMBTest:
		return "SMB connection test succeeded; the guest share is reachable and readable.", true
	case actionSMBMount:
		return "SMB share mounted successfully.", true
	case actionSMBUnmount:
		return "SMB share unmounted for this boot; its persistent configuration remains.", true
	case actionSMBRemove:
		return "Managed SMB mount unmounted and deleted from persistent configuration.", true
	default:
		return "", false
	}
}
