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
	"github.com/deckhouse/d8-lint/pkg/flags"
	"github.com/deckhouse/d8-lint/pkg/linters/copyright"
	no_cyrillic "github.com/deckhouse/d8-lint/pkg/linters/no-cyrillic"
	"github.com/deckhouse/d8-lint/pkg/linters/openapi"
	"github.com/deckhouse/d8-lint/pkg/linters/probes"
	"github.com/deckhouse/d8-lint/pkg/logger"
	"github.com/deckhouse/d8-lint/pkg/module"
)

const (
	ChartConfigFilename = "Chart.yaml"
	ModuleYamlFilename  = "module.yaml"
	HooksDir            = "hooks"
	ImagesDir           = "images"
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

	m.Linters = make(LinterList, 0)
	for _, linter := range m.lintersMap {
		m.Linters = append(m.Linters, linter)
	}

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
		logger.DebugF("Found `%s` module", paths[i])
		m.Modules = append(m.Modules, module.NewModule(paths[i]))
	}

	logger.InfoF("Found %d modules", len(m.Modules))

	return m
}

func (m *Manager) Run() errors.LintRuleErrorsList {
	result := errors.LintRuleErrorsList{}

	for i := range m.Modules {
		logger.InfoF("Run linters for `%s` module", m.Modules[i].GetName())
		var g errgroup.Group
		g.SetLimit(flags.LintersLimit)
		sm := sync.Mutex{}
		for j := range m.Linters {
			g.Go(func() error {
				logger.DebugF("Running linter `%s` on module `%s`", m.Linters[j].Name(), m.Modules[i].GetName())
				errs, err := m.Linters[j].Run(m.Modules[i])
				if err != nil {
					logger.ErrorF("Error running linter `%s`: %s\n", m.Linters[j].Name(), err)
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

		// Check if first level subdirectory has a helm chart configuration file
		if isExistsOnFilesystem(path, ModuleYamlFilename) ||
			(isExistsOnFilesystem(path, ChartConfigFilename) &&
				(isExistsOnFilesystem(path, HooksDir) || isExistsOnFilesystem(path, ImagesDir))) {
			chartDirs = append(chartDirs, path)
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return chartDirs, nil
}
