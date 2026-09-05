package config

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	files map[string]*Document
}

type Document struct {
	lines  []line
	values map[string]map[string]string
}

type line struct {
	raw          string
	ending       string
	section      string
	option       string
	continuation bool
	removed      bool
}

func Parse(reader io.Reader) (*Document, error) {
	document := &Document{values: make(map[string]map[string]string)}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	section := ""
	currentOption := ""
	for index, physical := range splitPhysicalLines(string(data)) {
		raw := physical.raw
		logical := strings.ReplaceAll(raw, "\r", "")
		if index == 0 {
			logical = strings.TrimPrefix(logical, "\ufeff")
		}
		trimmed := strings.TrimSpace(logical)
		entry := line{raw: raw, ending: physical.ending, section: section}
		if trimmed == "" {
			currentOption = ""
			document.lines = append(document.lines, entry)
			continue
		}
		if logical[0] == ' ' || logical[0] == '\t' {
			if currentOption == "" {
				return nil, fmt.Errorf("option expected at continuation %q", raw)
			}
			document.values[section][currentOption] += " " + trimmed
			entry.option, entry.continuation = currentOption, true
			document.lines = append(document.lines, entry)
			continue
		}
		currentOption = ""
		if strings.HasPrefix(logical, "#") {
			document.lines = append(document.lines, entry)
			continue
		}
		if strings.HasPrefix(trimmed, ";") {
			return nil, fmt.Errorf("invalid comment line %q", raw)
		}
		if strings.HasPrefix(logical, "[") {
			close := strings.IndexByte(logical, ']')
			if close < 0 {
				return nil, fmt.Errorf("section header must end with ']'")
			}
			section = normalize(logical[1:close])
			if section == "" {
				return nil, fmt.Errorf("empty section name")
			}
			entry.section = section
			document.lines = append(document.lines, entry)
			continue
		}
		name, value, ok := cutOption(logical)
		if !ok || section == "" {
			return nil, fmt.Errorf("invalid option line %q", raw)
		}
		name = normalize(name)
		if name == "" || strings.ContainsAny(name, " \t\r\n") {
			return nil, fmt.Errorf("empty option name")
		}
		if document.values[section] == nil {
			document.values[section] = make(map[string]string)
		}
		document.values[section][name] = strings.TrimSpace(value)
		entry.section, entry.option = section, name
		document.lines = append(document.lines, entry)
		currentOption = name
	}
	return document, nil
}

func New() *Config {
	config, err := Parse(strings.NewReader(DefaultConfig))
	if err != nil {
		panic("invalid embedded config template: " + err.Error())
	}
	servers, err := Parse(strings.NewReader(DefaultServers))
	if err != nil {
		panic("invalid embedded servers template: " + err.Error())
	}
	return &Config{files: map[string]*Document{"config": config, "servers": servers}}
}

func Load(configDir string) (*Config, error) {
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		configDir = filepath.Join(home, ".subversion")
	}
	result := New()
	for _, name := range []string{"config", "servers"} {
		file, err := os.Open(filepath.Join(configDir, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		document, parseErr := Parse(file)
		closeErr := file.Close()
		if parseErr != nil {
			return nil, fmt.Errorf("parse %s: %w", name, parseErr)
		}
		if closeErr != nil {
			return nil, closeErr
		}
		result.files[name] = document
	}
	return result, nil
}

func (config *Config) File(name string) *Document { return config.files[normalize(name)] }

func (config *Config) Get(section, option, fallback string) string {
	return config.GetFile("config", section, option, fallback)
}

func (config *Config) GetFile(file, section, option, fallback string) string {
	document := config.files[normalize(file)]
	if document == nil {
		return fallback
	}
	return document.Get(section, option, fallback)
}

func (document *Document) Get(section, option, fallback string) string {
	section, option = normalize(section), normalize(option)
	value, ok := document.rawValue(section, option)
	if !ok {
		return fallback
	}
	expanded, valid := document.expand(section, value, map[string]bool{section + "\x00" + option: true})
	if !valid {
		return ""
	}
	return expanded
}

func (config *Config) GetBool(section, option string, fallback bool) (bool, error) {
	value := config.Get(section, option, "")
	if value == "" {
		return fallback, nil
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "yes", "true", "on", "1":
		return true, nil
	case "no", "false", "off", "0":
		return false, nil
	default:
		return fallback, fmt.Errorf("invalid boolean %q", value)
	}
}

func (config *Config) GetInt(section, option string, fallback int64) (int64, error) {
	value := config.Get(section, option, "")
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return fallback, fmt.Errorf("invalid integer %q: %w", value, err)
	}
	return parsed, nil
}

func (config *Config) GetList(section, option string, fallback []string) []string {
	value := config.Get(section, option, "")
	if value == "" {
		return append([]string(nil), fallback...)
	}
	return strings.Fields(strings.ReplaceAll(value, ",", " "))
}

func (document *Document) Set(section, option, value string) {
	section, option = normalize(section), normalize(option)
	if document.values[section] == nil {
		document.values[section] = make(map[string]string)
	}
	document.values[section][option] = value
	for index := len(document.lines) - 1; index >= 0; index-- {
		if document.lines[index].section == section && document.lines[index].option == option {
			if document.lines[index].continuation {
				continue
			}
			document.lines[index].raw = replaceOptionValue(document.lines[index].raw, value)
			for next := index + 1; next < len(document.lines) && document.lines[next].continuation && document.lines[next].section == section && document.lines[next].option == option; next++ {
				document.lines[next].removed = true
			}
			return
		}
	}
	sectionFound := false
	for _, item := range document.lines {
		if item.section == section {
			sectionFound = true
			break
		}
	}
	if !sectionFound {
		document.ensureTrailingNewline()
		document.lines = append(document.lines, line{raw: "[" + section + "]", ending: "\n", section: section})
	}
	document.ensureTrailingNewline()
	document.lines = append(document.lines, line{raw: option + " = " + value, ending: "\n", section: section, option: option})
}

func (config *Config) ApplyOverride(value string) error {
	file, rest, ok := strings.Cut(value, ":")
	if !ok {
		return fmt.Errorf("invalid config override %q", value)
	}
	section, assignment, ok := strings.Cut(rest, ":")
	if !ok {
		return fmt.Errorf("invalid config override %q", value)
	}
	option, optionValue, ok := strings.Cut(assignment, "=")
	file = normalize(file)
	if !ok || file != "config" && file != "servers" {
		return fmt.Errorf("invalid config override %q", value)
	}
	document := config.files[file]
	if document == nil {
		return fmt.Errorf("config file %q is unavailable", file)
	}
	document.Set(section, option, optionValue)
	return nil
}

func (document *Document) Write(writer io.Writer) error {
	for _, item := range document.lines {
		if item.removed {
			continue
		}
		if _, err := io.WriteString(writer, item.raw+item.ending); err != nil {
			return err
		}
	}
	return nil
}

func (document *Document) ensureTrailingNewline() {
	if len(document.lines) > 0 && document.lines[len(document.lines)-1].ending == "" {
		document.lines[len(document.lines)-1].ending = "\n"
	}
}

type physicalLine struct{ raw, ending string }

func splitPhysicalLines(value string) []physicalLine {
	if value == "" {
		return nil
	}
	var lines []physicalLine
	for len(value) > 0 {
		index := strings.IndexByte(value, '\n')
		if index < 0 {
			lines = append(lines, physicalLine{raw: value})
			break
		}
		raw, ending := value[:index], "\n"
		if strings.HasSuffix(raw, "\r") {
			raw, ending = strings.TrimSuffix(raw, "\r"), "\r\n"
		}
		lines = append(lines, physicalLine{raw: raw, ending: ending})
		value = value[index+1:]
	}
	return lines
}

func (config *Config) Save(writer io.Writer) error { return config.files["config"].Write(writer) }

func (config *Config) ServerGroups(host string) []string {
	document := config.files["servers"]
	if document == nil {
		return nil
	}
	var groups []string
	seen := make(map[string]bool)
	for _, item := range document.lines {
		if item.section != "groups" || item.option == "" || item.continuation || seen[item.option] {
			continue
		}
		group, patterns := item.option, document.values["groups"][item.option]
		for _, pattern := range strings.Split(patterns, ",") {
			if hostMatch(strings.TrimSpace(pattern), host) {
				groups = append(groups, group)
				seen[group] = true
				break
			}
		}
	}
	return groups
}

func (config *Config) ServerOption(host, option, fallback string) string {
	document := config.files["servers"]
	if document == nil {
		return fallback
	}
	result := document.Get("global", option, fallback)
	groups := config.ServerGroups(host)
	if len(groups) > 0 {
		result = document.Get(groups[0], option, result)
	}
	return result
}

func (document *Document) expand(section, value string, seen map[string]bool) (string, bool) {
	var result strings.Builder
	for {
		start := strings.Index(value, "%(")
		if start < 0 {
			result.WriteString(value)
			break
		}
		end := strings.Index(value[start+2:], ")s")
		if end < 0 {
			result.WriteString(value)
			break
		}
		end += start + 2
		result.WriteString(value[:start])
		name := normalize(value[start+2 : end])
		replacement, ok := document.rawValue(section, name)
		key := section + "\x00" + name
		if !ok {
			result.WriteString(value[start : end+2])
		} else if seen[key] {
			return "", false
		} else {
			seen[key] = true
			expanded, valid := document.expand(section, replacement, seen)
			delete(seen, key)
			if !valid {
				return "", false
			}
			result.WriteString(expanded)
		}
		value = value[end+2:]
	}
	return result.String(), true
}

func (document *Document) rawValue(section, option string) (string, bool) {
	if value, ok := document.values[section][option]; ok {
		return value, true
	}
	if section != "default" {
		value, ok := document.values["default"][option]
		return value, ok
	}
	return "", false
}

func cutOption(value string) (string, string, bool) {
	equals := strings.IndexByte(value, '=')
	colon := strings.IndexByte(value, ':')
	index := equals
	if index < 0 || colon >= 0 && colon < index {
		index = colon
	}
	if index < 0 {
		return "", "", false
	}
	return strings.TrimSpace(value[:index]), value[index+1:], true
}

func hostMatch(pattern, host string) bool {
	pattern, host = strings.ToLower(pattern), strings.ToLower(host)
	matched, err := path.Match(pattern, host)
	return err == nil && matched
}

func replaceOptionValue(raw, value string) string {
	name, _, ok := cutOption(raw)
	if !ok {
		return normalize(name) + " = " + value
	}
	separator := strings.IndexAny(raw, "=:")
	prefix := raw[:separator+1]
	spaces := raw[separator+1:]
	spaces = spaces[:len(spaces)-len(strings.TrimLeft(spaces, " \t"))]
	return prefix + spaces + value
}

func normalize(value string) string { return strings.ToLower(strings.TrimSpace(value)) }
