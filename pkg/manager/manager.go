package manager

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/mitchellh/go-homedir"
	"golang.org/x/sync/errgroup"

	"github.com/deckhouse/d8-lint/pkg/config"
	"github.com/deckhouse/d8-lint/pkg/errors"
	"github.com/deckhouse/d8-lint/pkg/linters/copyright"
	no_cyrillic "github.com/deckhouse/d8-lint/pkg/linters/no-cyrillic"
	"github.com/deckhouse/d8-lint/pkg/linters/openapi"
	"github.com/deckhouse/d8-lint/pkg/linters/probes"
	"github.com/deckhouse/d8-lint/pkg/logger"
	"github.com/deckhouse/d8-lint/pkg/module"
)

const (
	ChartConfigFilename = "Chart.yaml"
)

type Manager struct {
	cfg     *config.Config
	Linters LinterList
	Modules []*module.Module

	lintersMap map[string]Linter
}

func NewManager(dirs []string, cfg *config.Config) *Manager {
	m := &Manager{
		cfg: cfg,
	}

	// fill all linters
	m.Linters = []Linter{
		openapi.New(&cfg.LintersSettings.OpenAPI),
		no_cyrillic.New(&cfg.LintersSettings.NoCyrillic),
		copyright.New(&cfg.LintersSettings.Copyright),
		probes.New(&cfg.LintersSettings.Probes),
	}

	m.lintersMap = make(map[string]Linter)
	for _, linter := range m.Linters {
		m.lintersMap[strings.ToLower(linter.Name())] = linter
	}

	// filter linters from config file
	m.Linters = m.getEnabledLinters()

	var paths []string

	for i := range dirs {
		dir, err := homedir.Expand(dirs[i])
		if err != nil {
			logger.ErrorF("Failed to expand home dir: %v", err)
			continue
		}
		result, err := getModulePaths(dir)
		if err != nil {
			logger.ErrorF("Error getting module paths: %v", err)
		}
		paths = append(paths, result...)
	}

	for i := range paths {
		fmt.Println("ADD", paths[i])
		//TODO: print "Found XXX module" in debug mode
		m.Modules = append(m.Modules, module.NewModule(paths[i]))
	}

	logger.InfoF("Found %d modules", len(m.Modules))

	return m
}

const (
	modulesLimit = 10
)

func (m *Manager) Run() errors.LintRuleErrorsList {
	result := errors.LintRuleErrorsList{}

	for i := range m.Linters {
		var g errgroup.Group
		g.SetLimit(modulesLimit)
		sm := sync.Mutex{}
		for j := range m.Modules {
			g.Go(func() error {
				// TODO: print INFO "Run linters for XXX module"
				// TODO: print DEBUG "Run linter YYY" <optional>
				logger.InfoF("Running linter `%s` on module `%s`", m.Linters[i].Name(), m.Modules[j].GetName())
				errs, err := m.Linters[i].Run(m.Modules[j])
				if err != nil {
					logger.WarnF("Error running linter `%s`: %s\n", m.Linters[i].Name(), err)
					return err
				}
				if errs.ConvertToError() != nil {
					sm.Lock()
					result.Merge(errs)
					sm.Unlock()
				}
				return nil
			})
		}
		_ = g.Wait()
	}

	return result
}

func isExistsOnFilesystem(parts ...string) bool {
	_, err := os.Stat(filepath.Join(parts...))
	return err == nil
}

// getModulePaths returns all paths with Chart.yaml
// modulesDir can be a module directory or a directory that contains modules in subdirectories.
func getModulePaths(modulesDir string) ([]string, error) {
	var chartDirs = make([]string, 0)

	// Here we find all dirs and check for Chart.yaml in them.
	err := filepath.Walk(modulesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return fmt.Errorf("file access '%s': %w", path, err)
		}

		// Ignore non-dirs
		if !info.IsDir() {
			return nil
		}

		// root path can be module dir, if we run one module for local testing
		// usually, root dir contains another modules and should not be ignored
		if path == modulesDir {
			return nil
		}

		// Check if first level subdirectory has a helm chart configuration file
		if isExistsOnFilesystem(path, ChartConfigFilename) {
			chartDirs = append(chartDirs, path)
		}

		return filepath.SkipDir
	})

	if err != nil {
		return nil, err
	}

	return chartDirs, nil
}

func (m *Manager) getEnabledLinters() LinterList {
	resultLintersSet := map[string]Linter{}
	switch {
	case m.cfg.Linters.DisableAll:
		// no default linters
	case m.cfg.Linters.EnableAll:
		resultLintersSet = m.lintersMap
	default:
		resultLintersSet = m.lintersMap
	}

	for _, name := range m.cfg.Linters.Enable {
		name = strings.ToLower(name)
		if m.lintersMap[name] == nil {
			continue
		}
		resultLintersSet[name] = m.lintersMap[name]
	}

	for _, name := range m.cfg.Linters.Disable {
		if m.lintersMap[name] == nil {
			continue
		}
		delete(resultLintersSet, name)
	}
	result := make(LinterList, 0)
	for _, linter := range resultLintersSet {
		result = append(result, linter)
	}

	return result
}
