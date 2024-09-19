package probes

import (
	"bufio"
	"fmt"
	"strings"
	"sync"

	"github.com/mitchellh/hashstructure/v2"
	"gopkg.in/yaml.v3"
	"helm.sh/helm/v3/pkg/chartutil"

	"github.com/deckhouse/d8-lint/pkg/helm"
	"github.com/deckhouse/d8-lint/pkg/module"
	"github.com/deckhouse/d8-lint/pkg/storage"
)

var (
	renderedTemplatesHash = sync.Map{}
)

func RunRender(m *module.Module, values chartutil.Values, objectStore *storage.UnstructuredObjectStore) (lintError error) {
	var renderer helm.Renderer
	renderer.Name = m.Name
	renderer.Namespace = m.Namespace
	renderer.LintMode = true

	files, err := renderer.RenderChartFromRawValues(m.Chart, values)
	if err != nil {
		lintError = fmt.Errorf("helm chart render: %v", err)
		return
	}

	hash, err := hashstructure.Hash(files, hashstructure.FormatV2, nil)
	if err != nil {
		lintError = fmt.Errorf("helm chart render: %v", err)
		return
	}

	if _, ok := renderedTemplatesHash.Load(hash); ok {
		return // the same files were already checked
	}

	defer renderedTemplatesHash.Store(hash, struct{}{})

	var docBytes []byte

	for path, bigFile := range files {
		scanner := bufio.NewScanner(strings.NewReader(bigFile))
		scanner.Split(SplitAt("---"))

		for scanner.Scan() {
			var node map[string]interface{}
			docBytes = scanner.Bytes()

			err := yaml.Unmarshal(docBytes, &node)
			if err != nil {
				return fmt.Errorf(manifestErrorMessage, err, numerateManifestLines(string(docBytes)))
			}

			if len(node) == 0 {
				continue
			}

			err = objectStore.Put(path, node, docBytes)
			if err != nil {
				return fmt.Errorf("helm chart object already exists: %v", err)
			}
		}
	}
	return
}

func SplitAt(substring string) func(data []byte, atEOF bool) (advance int, token []byte, err error) {
	return func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		// Return nothing if at end of file and no data passed
		if atEOF && len(data) == 0 {
			return 0, nil, nil
		}

		// Find the index of the input of the separator substring
		if i := strings.Index(string(data), substring); i >= 0 {
			return i + len(substring), data[0:i], nil
		}

		// If at end of file with data return the data
		if atEOF {
			return len(data), data, nil
		}

		return
	}
}

const (
	manifestErrorMessage = `manifest unmarshal: %v

--- Manifest:
%s
`
	testsSuccessfulMessage = `
%sModule %s - %v test cases passed!

`
	testsErrorMessage = `test #%v failed:
--- Error:
%s

--- Values:
%s

`
)

func numerateManifestLines(manifest string) string {
	manifestLines := strings.Split(manifest, "\n")
	builder := strings.Builder{}

	for index, line := range manifestLines {
		builder.WriteString(fmt.Sprintf("%d\t%s\n", index+1, line))
	}

	return builder.String()
}