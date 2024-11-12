/*
Copyright 2021 Flant JSC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package rules

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/deckhouse/d8-lint/pkg/errors"
)

func skipModuleImageNameIfNeeded(filePath string) bool {
	for _, img := range Cfg.SkipModuleImageName {
		if strings.HasSuffix(filePath, img) {
			return true
		}
	}
	return false
}

var regexPatterns = map[string]string{
	`$BASE_ALPINE`:           imageRegexp(`alpine:[\d.]+`),
	`$BASE_GOLANG_ALPINE`:    imageRegexp(`golang:1.15.[\d.]+-alpine3.12`),
	`$BASE_GOLANG_16_ALPINE`: imageRegexp(`golang:1.16.[\d.]+-alpine3.12`),
	`$BASE_GOLANG_BUSTER`:    imageRegexp(`golang:1.15.[\d.]+-buster`),
	`$BASE_GOLANG_16_BUSTER`: imageRegexp(`golang:1.16.[\d.]+-buster`),
	`$BASE_NGINX_ALPINE`:     imageRegexp(`nginx:[\d.]+-alpine`),
	`$BASE_PYTHON_ALPINE`:    imageRegexp(`python:[\d.]+-alpine`),
	`$BASE_UBUNTU`:           imageRegexp(`ubuntu:[\d.]+`),
	`$BASE_JEKYLL`:           imageRegexp(`jekyll/jekyll:[\d.]+`),
	`$BASE_SCRATCH`:          imageRegexp(`scratch:[\d.]+`),
}

var distrolessImagesPrefix = map[string][]string{
	"werf": {
		"{{ .Images.BASE_DISTROLESS",
		"{{ $.Images.BASE_ALT",
	},
	"docker": {
		"$BASE_DISTROLESS",
		"$BASE_ALT",
	},
}

func skipDistrolessImageCheckIfNeeded(image string) bool {
	for _, img := range Cfg.SkipDistrolessImageCheck {
		if strings.HasSuffix(image, img) {
			return true
		}
	}

	return false
}

func imageRegexp(s string) string {
	return fmt.Sprintf("^(from:|FROM)(\\s+)(%s)", s)
}

func isImageNameUnacceptable(imageName string) (b bool, s string) {
	for ciVariable, pattern := range regexPatterns {
		matched, _ := regexp.MatchString(pattern, imageName)
		if matched {
			return true, ciVariable
		}
	}
	return false, ""
}

func CheckImageNamesInDockerAndWerfFiles(
	name, path string,
) (lintRuleErrorsList errors.LintRuleErrorsList) {
	var filePaths []string
	imagesPath := filepath.Join(path, ImagesDir)

	if !IsExistsOnFilesystem(imagesPath) {
		return lintRuleErrorsList
	}

	err := filepath.Walk(imagesPath, func(fullPath string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		switch filepath.Base(fullPath) {
		case "werf.inc.yaml", "Dockerfile":
			filePaths = append(filePaths, fullPath)
		}
		return nil
	})
	if err != nil {
		lintRuleErrorsList.Add(errors.NewLintRuleError(
			ID,
			ModuleLabel(name),
			imagesPath,
			nil,
			"Cannot read directory structure: %s",
			err.Error(),
		))
		return lintRuleErrorsList
	}
	for _, filePath := range filePaths {
		if skipModuleImageNameIfNeeded(filePath) {
			continue
		}
		lintRuleErrorsList.Add(lintOneDockerfileOrWerfYAML(name, filePath, imagesPath))
	}

	return lintRuleErrorsList
}

func lintOneDockerfileOrWerfYAML(name, filePath, imagesPath string) *errors.LintRuleError {
	file, err := os.Open(filePath)
	if err != nil {
		return errors.NewLintRuleError(
			ID,
			filePath,
			ModuleLabel(name),
			filePath,
			"Error opening file:%s",
			err,
		)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	linePos := 0
	relativeFilePath, err := filepath.Rel(imagesPath, filePath)
	if err != nil {
		return errors.NewLintRuleError(
			ID,
			ModuleLabel(name),
			filePath,
			nil,
			"Error calculating relative file path: %s",
			err.Error(),
		)
	}

	var (
		dockerfileFromInstructions []string
		lastWerfImagePos           int
	)
	isWerfYAML := filepath.Base(filePath) == "werf.inc.yaml"

	for scanner.Scan() {
		line := scanner.Text()
		linePos++
		result, ciVariable := isImageNameUnacceptable(line)
		if result {
			return errors.NewLintRuleError(
				ID,
				fmt.Sprintf("module = %s, image = %s, line = %d", name, relativeFilePath, linePos),
				line,
				nil,
				"Please use %s as an image name", ciVariable,
			)
		}

		if isWerfYAML {
			if strings.HasPrefix(line, "image: ") {
				lastWerfImagePos = linePos
			} else if strings.HasPrefix(line, "from: ") {
				fromTrimmed := strings.TrimPrefix(line, "from: ")
				// "from:" right after "image:"
				if linePos-lastWerfImagePos == 1 {
					if skipDistrolessImageCheckIfNeeded(relativeFilePath) {
						log.Printf("WARNING!!! SKIP DISTROLESS CHECK!!!\nmodule = %s, image = %s\nvalue - %s\n\n", name, relativeFilePath, fromTrimmed)
						continue
					}

					result, message := isWerfInstructionUnacceptable(fromTrimmed)
					if result {
						return errors.NewLintRuleError(
							ID,
							name,
							fmt.Sprintf("module = %s, image = %s", name, relativeFilePath),
							nil,
							fromTrimmed,
							message,
						)
					}
				}
			}
			continue
		}
		if strings.HasPrefix(line, "FROM ") {
			fromTrimmed := strings.TrimPrefix(line, "FROM ")
			dockerfileFromInstructions = append(dockerfileFromInstructions, fromTrimmed)
		}
	}

	for i, fromInstruction := range dockerfileFromInstructions {
		lastInstruction := i == len(dockerfileFromInstructions)-1
		if skipDistrolessImageCheckIfNeeded(relativeFilePath) {
			log.Printf("WARNING!!! SKIP DISTROLESS CHECK!!!\nmodule = %s, image = %s\nvalue - %s\n\n", name, relativeFilePath, fromInstruction)
			continue
		}

		result, message := isDockerfileInstructionUnacceptable(fromInstruction, lastInstruction)
		if result {
			return errors.NewLintRuleError(
				ID,
				name,
				name,
				fmt.Sprintf("module = %s, image = %s", name, relativeFilePath),
				fromInstruction,
				message,
			)
		}
	}

	return nil
}

func isWerfInstructionUnacceptable(from string) (b bool, s string) {
	if !checkDistrolessPrefix(from, distrolessImagesPrefix["werf"]) {
		return true, "`from:` parameter for `image:` should be one of our BASE_DISTROLESS images"
	}
	return false, ""
}

func isDockerfileInstructionUnacceptable(from string, final bool) (b bool, s string) {
	if from == "scratch" {
		return false, ""
	}

	if final {
		if !checkDistrolessPrefix(from, distrolessImagesPrefix["docker"]) {
			return true, "Last `FROM` instruction should use one of our $BASE_DISTROLESS images"
		}
	} else {
		matched, _ := regexp.MatchString("@sha256:[A-Fa-f0-9]{64}", from)
		if !strings.HasPrefix(from, "$BASE_") && !matched {
			return true, "Intermediate `FROM` instructions should use one of our $BASE_ images or have `@sha526:` checksum specified"
		}
	}
	return false, ""
}

func checkDistrolessPrefix(str string, in []string) bool {
	result := false
	for _, pattern := range in {
		if strings.HasPrefix(str, pattern) {
			result = true
			break
		}
	}
	return result
}