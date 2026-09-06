package repos

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/otuschhoff/go-svn/svn"
)

func (repository *Repository) RunHook(ctx context.Context, name string, arguments []string, stdin []byte) (string, error) {
	hookPath := filepath.Join(repository.path, "hooks", name)
	info, err := os.Stat(hookPath)
	if errors.Is(err, os.ErrNotExist) || err == nil && info.Mode().Perm()&0o111 == 0 {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	command := exec.CommandContext(ctx, hookPath, arguments...)
	command.Env, err = repository.hookEnvironment(name)
	if err != nil {
		return "", err
	}
	command.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return stdout.String(), fmt.Errorf("%w: %s hook: %s", svn.ErrReposHookFailure, name, message)
	}
	return stdout.String(), nil
}

func (repository *Repository) hookEnvironment(hookName string) ([]string, error) {
	environment := os.Environ()
	file, err := os.Open(filepath.Join(repository.path, "conf", "hooks-env"))
	if errors.Is(err, os.ErrNotExist) {
		return environment, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	sections := map[string]map[string]string{"default": {}, hookName: {}}
	section := ""
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		name, value, found := strings.Cut(line, "=")
		if !found || sections[section] == nil {
			continue
		}
		sections[section][strings.TrimSpace(name)] = strings.TrimSpace(value)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	values := make(map[string]string)
	for _, entry := range environment {
		if name, value, found := strings.Cut(entry, "="); found {
			values[name] = value
		}
	}
	for _, selected := range []string{"default", hookName} {
		for name, value := range sections[selected] {
			values[name] = value
		}
	}
	environment = environment[:0]
	for name, value := range values {
		environment = append(environment, name+"="+value)
	}
	return environment, nil
}
