package setup

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrUnbalancedSection reports a file that carries a beads BEGIN or END marker
// without its partner. Refusing beats guessing a range: a stray BEGIN followed
// by a later END would otherwise make every later run and every --remove treat
// the text between them as the section and drop it.
var ErrUnbalancedSection = errors.New("beads section markers are unbalanced")

// ManagedSection wraps body in the beads integration markers so it can be
// inserted into - and later replaced inside - a file the user also owns.
func ManagedSection(body string) string {
	return agentsBeginMarker + "\n" + strings.TrimRight(body, "\n") + "\n" + agentsEndMarker + "\n"
}

// ContainsManagedSection reports whether content already carries a beads section.
func ContainsManagedSection(content string) bool {
	_, _, found, err := managedSectionBounds(content)
	return found && err == nil
}

// UpsertManagedSection returns content with its beads section set to body.
// A file that does not have a section yet gets one appended, so unrelated
// user-authored content is preserved - unless it is byte-for-byte the unmarked
// template a previous version wrote, which is beads' own content and is
// migrated to the section in place. The bool reports whether an existing
// section was replaced (as opposed to one being added).
//
// Content holding a BEGIN marker with no END (or the reverse) is refused with
// ErrUnbalancedSection: appending would supply the missing marker and the next
// run, or --remove, would then span from the stray marker to it and delete
// whatever the user wrote in between.
func UpsertManagedSection(content, body string) (string, bool, error) {
	section := ManagedSection(body)
	start, after, found, err := managedSectionBounds(content)
	if err != nil {
		return "", false, err
	}
	if !found {
		if strings.TrimSpace(content) == "" {
			return section, false, nil
		}
		if isLegacyTemplate(content, body) {
			return section, true, nil
		}
		return content + "\n\n" + section, false, nil
	}
	return content[:start] + section + content[after:], true, nil
}

// isLegacyTemplate reports whether content is exactly the body a previous
// version of setup wrote as the whole file, ignoring trailing whitespace. The
// file is then beads' own output rather than user content, so it can be
// migrated to the managed section instead of preserved alongside it.
func isLegacyTemplate(content, body string) bool {
	if strings.TrimSpace(body) == "" {
		return false
	}
	return strings.TrimRight(content, " \t\r\n") == strings.TrimRight(body, " \t\r\n")
}

// RemoveManagedSection drops the beads section from content. The bool reports
// whether anything other than whitespace is left, so callers can tell a
// beads-only file (safe to delete) from a shared one. Unbalanced markers are
// left alone (and reported as ErrUnbalancedSection) rather than guessed at, so
// a stray BEGIN cannot make removal eat the text up to some later END.
func RemoveManagedSection(content string) (string, bool, error) {
	if _, _, _, err := managedSectionBounds(content); err != nil {
		return "", false, err
	}
	remaining := removeBeadsSection(content)
	return remaining, strings.TrimSpace(remaining) != "", nil
}

// SectionAction describes what an InstallManagedSectionFile call did.
type SectionAction string

const (
	// SectionCreated means the file did not exist (or was empty) and now holds
	// only the beads section.
	SectionCreated SectionAction = "created"
	// SectionAdded means a beads section was appended to a user-authored file.
	SectionAdded SectionAction = "added"
	// SectionUpdated means an existing beads section was replaced in place.
	SectionUpdated SectionAction = "updated"
)

// InstallManagedSectionFile writes body as the beads section of path, leaving
// the rest of an existing file untouched.
func InstallManagedSectionFile(path, body string) (SectionAction, error) {
	content := ""
	mode := os.FileMode(0o644)
	if data, perm, err := readManagedSectionFile(path); err == nil {
		content, mode = data, perm
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("read %s: %w", path, err)
	}

	wasEmpty := strings.TrimSpace(content) == ""
	updated, replaced, err := UpsertManagedSection(content, body)
	if err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	if err := atomicWriteFile(path, []byte(updated), mode); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}

	switch {
	case replaced:
		return SectionUpdated, nil
	case wasEmpty:
		return SectionCreated, nil
	default:
		return SectionAdded, nil
	}
}

// WriteManagedSectionFile writes content back to path through the same atomic,
// symlink-refusing path InstallManagedSectionFile uses, so `--remove` and
// install agree on what a writable destination is.
func WriteManagedSectionFile(path, content string) error {
	_, mode, err := readManagedSectionFile(path)
	if err != nil {
		return err
	}
	return atomicWriteFile(path, []byte(content), mode)
}

// managedSectionBounds refuses ambiguous ranges before install, check or removal.
func managedSectionBounds(content string) (start, after int, found bool, err error) {
	const begin = "<!-- BEGIN BEADS INTEGRATION"
	const end = "<!-- END BEADS INTEGRATION"
	begins, ends := strings.Count(content, begin), strings.Count(content, end)
	if begins == 0 && ends == 0 {
		return 0, 0, false, nil
	}
	bad := func() (int, int, bool, error) {
		return 0, 0, false, fmt.Errorf("%w: expected one complete marker pair on separate lines; fix the markers by hand", ErrUnbalancedSection)
	}
	if begins != 1 || ends != 1 {
		return bad()
	}
	start, finish := strings.Index(content, begin), strings.Index(content, agentsEndMarker)
	if finish <= start || (start > 0 && content[start-1] != '\n') || content[finish-1] != '\n' {
		return bad()
	}
	headerEnd := strings.IndexByte(content[start:], '\n')
	if headerEnd < 0 || start+headerEnd >= finish || !strings.HasSuffix(strings.TrimSuffix(content[start:start+headerEnd], "\r"), "-->") {
		return bad()
	}
	after = finish + len(agentsEndMarker)
	if after < len(content) && content[after] != '\r' && content[after] != '\n' {
		return bad()
	}
	if after < len(content) && content[after] == '\r' {
		after++
	}
	if after < len(content) && content[after] == '\n' {
		after++
	}
	return start, after, true, nil
}

// ReadManagedSectionFile refuses links and non-regular shared destinations.
func ReadManagedSectionFile(path string) (string, error) {
	content, _, err := readManagedSectionFile(path)
	return content, err
}

func readManagedSectionFile(path string) (string, os.FileMode, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", 0, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", 0, fmt.Errorf("%w: %s", errRefuseSymlinkWrite, path)
	}
	if !info.Mode().IsRegular() {
		return "", 0, fmt.Errorf("shared instructions must be a regular file: %s", path)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- explicit setup destination
	return string(data), info.Mode().Perm(), err
}
