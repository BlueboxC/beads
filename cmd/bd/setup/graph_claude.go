package setup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/config"
)

// GraphClaudeStop configures only Steph's existing Stop handler in project
// settings and links Claude to existing graph guidance with an @-import.
// It does not replace instructions or install prime hooks, plugins or globals.
func GraphClaudeStop(projectDir string, check, remove bool) error {
	env, err := claudeEnvProvider()
	if err != nil {
		return err
	}
	env.projectDir = projectDir
	return graphClaudeStop(env, check, remove)
}

// graphClaudeSkipHooksHint completes every init-time preflight refusal: the
// refusal names what to change, and this names the way to initialize anyway.
const graphClaudeSkipHooksHint = "To create the workspace without the Stop hook, run `bd init --graph-mode link --skip-hooks` with your other init flags; once this is fixed, `bd setup claude` can add the hook"

// PreflightGraphClaudeStop checks an init-time installation without writing
// files, so known Claude conflicts refuse before graph storage is created.
// Each refusal also says how to initialize without the Stop hook.
func PreflightGraphClaudeStop(projectDir string) error {
	env, err := claudeEnvProvider()
	if err == nil {
		env.projectDir = projectDir
		env.stdout = io.Discard
		err = graphClaudeStopMode(env, false, false, true)
	}
	if err != nil {
		return fmt.Errorf("%w. %s", err, graphClaudeSkipHooksHint)
	}
	return nil
}

// InstallGraphClaudeStopOnInit uses the same guarded installer as explicit
// setup, but leaves init's structured output to its caller.
func InstallGraphClaudeStopOnInit(projectDir string) error {
	env, err := claudeEnvProvider()
	if err != nil {
		return err
	}
	env.projectDir = projectDir
	env.stdout = io.Discard
	return graphClaudeStop(env, false, false)
}

func graphClaudeStop(env claudeEnv, check, remove bool) error {
	return graphClaudeStopMode(env, check, remove, false)
}

func graphClaudeStopMode(env claudeEnv, check, remove, preflight bool) error {
	if check && remove {
		return fmt.Errorf("choose --check or --remove, not both")
	}
	path := projectSettingsPath(env.projectDir)
	// Never follow a project settings symlink into another workspace or home.
	for _, candidate := range []string{filepath.Dir(path), path} {
		info, err := os.Lstat(candidate)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("cannot inspect graph Claude settings path %s: %w; make it accessible before graph setup", candidate, err)
		}
		if err == nil && (info.Mode()&os.ModeSymlink != 0 || (candidate == path && !info.Mode().IsRegular()) || (candidate != path && !info.IsDir())) {
			want := "a real directory"
			if candidate == path {
				want = "a regular file"
			}
			return fmt.Errorf("preserve non-regular graph Claude settings path %s: graph setup writes only project-local settings, so it must be %s, not a symlink or other file type; replace it before graph setup (settings were preserved)", candidate, want)
		}
	}
	settings, exists, err := graphClaudeSettings(env, path)
	if err != nil {
		return err
	}
	hooks := map[string]interface{}{}
	if raw, present := settings["hooks"]; present {
		var ok bool
		hooks, ok = raw.(map[string]interface{})
		if !ok {
			return fmt.Errorf("preserve malformed hooks in %s: \"hooks\" must be a JSON object; fix it before graph setup (settings were preserved)", path)
		}
	}
	installed, err := graphClaudeStopPresent(path, hooks)
	if err != nil {
		return err
	}
	if !remove {
		// Global and legacy files are read only. An existing plugin or prime
		// recipe needs a deliberate operator decision, not a silent migration.
		for _, other := range []string{globalSettingsPath(env.homeDir), legacyProjectSettingsPath(env.projectDir)} {
			if _, _, err := graphClaudeSettings(env, other); err != nil {
				return err
			}
		}
		if hasBeadsPlugin(env) {
			return graphClaudePluginRefusal(env)
		}
		for _, existing := range []string{path, globalSettingsPath(env.homeDir), legacyProjectSettingsPath(env.projectDir)} {
			if hasBeadsHooks(existing) {
				return fmt.Errorf("existing Beads prime hooks in %s are unsupported in graph mode: remove that file's bd prime SessionStart/PreCompact hook entries before installing the graph Stop hook (settings were preserved)", existing)
			}
		}
	}
	var importPath string
	var importBefore, importAfter []byte
	if !remove {
		importPath, importBefore, importAfter, err = graphClaudeImport(env)
		if err != nil {
			return err
		}
	}
	if preflight {
		return nil
	}
	if check {
		if !bytes.Equal(importBefore, importAfter) {
			return fmt.Errorf("Claude does not import graph instructions; run bd setup claude")
		}
		if !installed {
			return fmt.Errorf("graph Claude Stop hook is not installed; run bd setup claude")
		}
		_, _ = fmt.Fprintln(env.stdout, "Graph Claude Stop hook is installed.")
		return nil
	}
	// All settings/plugin/import refusals have completed before publication.
	if !remove && !bytes.Equal(importBefore, importAfter) {
		if err := graphClaudeWritePreservingMode(importPath, importAfter); err != nil {
			return fmt.Errorf("write graph guidance import: %w", err)
		}
	}
	if remove {
		if !exists || !installed {
			_, _ = fmt.Fprintf(env.stdout, "Graph Claude Stop hook is absent from %s; instructions preserved.\n", path)
			return nil
		}
		removeHookCommand(hooks, "Stop", claudeStopHookCommand)
	} else {
		if installed {
			_, _ = fmt.Fprintf(env.stdout, "Graph Claude Stop hook configured in %s; guidance loaded by %s.\n", path, importPath)
			return nil
		}
		addHookCommand(hooks, "Stop", claudeStopHookCommand)
	}
	settings["hooks"] = hooks
	data, err := marshalSettings(settings)
	if err != nil {
		return err
	}
	if err := env.ensureDir(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("write graph Claude settings %s: %w", path, err)
	}
	if existing, readErr := env.readFile(path); readErr != nil || !bytes.Equal(existing, data) {
		if err := graphClaudeWritePreservingMode(path, data); err != nil {
			return fmt.Errorf("write graph Claude settings %s: %w", path, err)
		}
	}
	if remove {
		_, _ = fmt.Fprintf(env.stdout, "Graph Claude Stop hook removed from %s; instructions preserved.\n", path)
	} else {
		_, _ = fmt.Fprintf(env.stdout, "Graph Claude Stop hook configured in %s; guidance loaded by %s.\n", path, importPath)
	}
	return nil
}

// Graph setup retains existing private file modes; ordinary setup is unchanged.
func graphClaudeWritePreservingMode(path string, data []byte) error {
	mode := os.FileMode(0644)
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("preserve non-regular graph Claude file %s", path)
		}
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return err
	}
	return atomicWriteFile(path, data, mode)
}

func graphClaudeSettings(env claudeEnv, path string) (map[string]interface{}, bool, error) {
	raw, err := env.readFile(path)
	if os.IsNotExist(err) {
		return map[string]interface{}{}, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("cannot read Claude settings %s: %w; make it readable before graph setup (settings were preserved)", path, err)
	}
	// Shared plugin/prime detection still decodes numbers as float64. Retain
	// that admission range so an unrelated overflow cannot hide those hooks;
	// UseNumber below preserves the exact values of admitted settings.
	var legacySettings interface{}
	if err := json.Unmarshal(raw, &legacySettings); err != nil {
		return nil, true, fmt.Errorf("preserve Claude settings in %s: unsupported JSON or numeric range: %w; fix or remove the invalid content so graph setup can check that file for the Beads plugin and bd prime hooks (settings were preserved)", path, err)
	}
	var settings map[string]interface{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&settings); err != nil || settings == nil {
		return nil, true, fmt.Errorf("preserve invalid Claude settings object in %s: the file must contain one JSON object; fix it before graph setup (settings were preserved)", path)
	}
	if err := decoder.Decode(new(interface{})); err != io.EOF {
		return nil, true, fmt.Errorf("preserve trailing Claude settings content in %s: remove everything after the first JSON object before graph setup (settings were preserved)", path)
	}
	return settings, true, nil
}

// graphClaudePluginRefusal names every settings file that enables the Beads
// plugin. hasBeadsPlugin reports the plugin when ANY of these files enables
// it, so a false entry in one file does not override another file's true
// entry: advice to disable it "for this project" alone would be a dead end.
func graphClaudePluginRefusal(env claudeEnv) error {
	var enabling []string
	for _, candidate := range []string{projectSettingsPath(env.projectDir), globalSettingsPath(env.homeDir), legacyProjectSettingsPath(env.projectDir)} {
		if checkBeadsPluginInFile(env.readFile, candidate) {
			enabling = append(enabling, candidate)
		}
	}
	where, scope := strings.Join(enabling, " and "), "each of those files"
	if len(enabling) == 1 {
		scope = "that file"
	} else if len(enabling) == 0 { // Unreachable while both read the same files.
		where, scope = "Claude settings", "each settings file that enables it"
	}
	return fmt.Errorf("the Beads Claude plugin is enabled in %s, and its bd prime hooks are unsupported in graph mode: disable the plugin in %s (set its \"beads@<marketplace>\" entry under enabledPlugins to false, or remove the entry); graph setup checks each settings file separately, so a false entry in another file does not count (settings were preserved)", where, scope)
}

// Validate the changed event before using the shared merge primitives, which
// intentionally tolerate malformed legacy shapes in ordinary setup.
func graphClaudeStopPresent(path string, hooks map[string]interface{}) (bool, error) {
	raw, present := hooks["Stop"]
	if !present {
		return false, nil
	}
	entries, ok := raw.([]interface{})
	if !ok {
		return false, fmt.Errorf("preserve malformed Stop hooks in %s: hooks.Stop must be an array; fix it before graph setup (settings were preserved)", path)
	}
	found := false
	for _, entry := range entries {
		object, ok := entry.(map[string]interface{})
		if !ok {
			return false, fmt.Errorf("preserve malformed Stop entry in %s: each hooks.Stop entry must be an object; fix it before graph setup (settings were preserved)", path)
		}
		commands, ok := object["hooks"].([]interface{})
		if !ok {
			return false, fmt.Errorf("preserve malformed Stop entry hooks in %s: each hooks.Stop entry needs a \"hooks\" array; fix it before graph setup (settings were preserved)", path)
		}
		for _, command := range commands {
			value, ok := command.(map[string]interface{})
			if !ok {
				return false, fmt.Errorf("preserve malformed Stop command in %s: each Stop hook command must be an object; fix it before graph setup (settings were preserved)", path)
			}
			if value["command"] == claudeStopHookCommand {
				matcher, validMatcher := object["matcher"].(string)
				_, matcherPresent := object["matcher"]
				if value["type"] != "command" || matcher != "" || (matcherPresent && !validMatcher) {
					return false, fmt.Errorf("existing bd claude-hook stop in %s has an unsupported type or matcher: give it \"type\": \"command\" and no matcher, or remove that entry, before graph setup (settings were preserved)", path)
				}
				found = true
			}
		}
	}
	return found, nil
}

// Reuse Claude's established active-import parser rather than copying graph
// instructions into a second managed block. No existing content is removed.
func graphClaudeImport(env claudeEnv) (string, []byte, []byte, error) {
	path := filepath.Join(env.projectDir, claudeInstructionsFile)
	agentsFile := config.SafeAgentsFile()
	info, err := os.Lstat(path)
	if err != nil && !os.IsNotExist(err) {
		return "", nil, nil, fmt.Errorf("cannot inspect %s: %w; make it accessible before graph setup (instructions were preserved)", path, err)
	}
	if err == nil && !info.Mode().IsRegular() {
		replacement := "a regular file"
		if agentsFile != claudeInstructionsFile {
			replacement += " (one containing just the line @" + agentsFile + " loads the graph guidance)"
		}
		return "", nil, nil, fmt.Errorf("preserve non-regular %s: graph setup edits only a regular CLAUDE.md, not a symlink or other file type; replace it with %s before graph setup (instructions were preserved)", path, replacement)
	}
	before, err := env.readFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", nil, nil, fmt.Errorf("cannot read %s: %w; make it readable before graph setup (instructions were preserved)", path, err)
	}
	if agentsFile == claudeInstructionsFile {
		return path, before, before, nil // The validated graph profile is already directly loaded.
	}
	if containsBeadsMarker(string(before)) {
		return "", nil, nil, fmt.Errorf("%s contains a managed Beads block: remove that block (graph guidance lives in %s, which graph setup imports) before graph setup (instructions were preserved)", path, agentsFile)
	}
	if isAgentsImportStub(string(before), agentsFile) {
		return path, before, before, nil
	}
	after := string(before)
	if after != "" && !strings.HasSuffix(after, "\n") {
		after += "\n"
	}
	if after != "" {
		after += "\n"
	}
	after += "@" + agentsFile + "\n"
	if !isAgentsImportStub(after, agentsFile) {
		return "", nil, nil, fmt.Errorf("%s has an unclosed code fence, so an appended @%s import would not be active: close the fence before graph setup (instructions were preserved)", path, agentsFile)
	}
	return path, before, []byte(after), nil
}
