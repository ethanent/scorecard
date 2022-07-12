// Copyright 2021 Security Scorecard Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package raw

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/h2non/filetype"
	"github.com/h2non/filetype/types"
	"github.com/rhysd/actionlint"

	"github.com/ossf/scorecard/v4/checker"
	"github.com/ossf/scorecard/v4/checks/fileparser"
	"github.com/ossf/scorecard/v4/clients"
	sce "github.com/ossf/scorecard/v4/errors"
)

var gradleWrapperValidationActionRegex = regexp.MustCompile(`^gradle\/wrapper-validation-action(?:@.+)?$`)

// BinaryArtifacts retrieves the raw data for the Binary-Artifacts check.
func BinaryArtifacts(c clients.RepoClient) (checker.BinaryArtifactData, error) {
	files := []checker.File{}
	err := fileparser.OnMatchingFileContentDo(c, fileparser.PathMatcher{
		Pattern:       "*",
		CaseSensitive: false,
	}, checkBinaryFileContent, &files)
	if err != nil {
		return checker.BinaryArtifactData{}, fmt.Errorf("%w", err)
	}

	// Indices of any gradle-wrapper.jar files
	var gradleWrappers []int
	if len(files) > 0 {
		for i, f := range files {
			if filepath.Base(f.Path) == "gradle-wrapper.jar" {
				gradleWrappers = append(gradleWrappers, i)
			}
		}
	}
	if len(gradleWrappers) > 0 {
		// Gradle wrapper JARs present, so check that they are validated
		if ok, err := gradleWrapperValidationOK(c); ok && err == nil {
			// It has been confirmed that latest commit has validated JARs!
			// Remove Gradle wrapper JARs from files.
			offset := 0
			for _, ji := range gradleWrappers {
				files = append(files[:ji-offset], files[ji+1-offset:]...)
				offset++
			}
		}
	}
	// No error, return the files.
	return checker.BinaryArtifactData{Files: files}, nil
}

var checkBinaryFileContent fileparser.DoWhileTrueOnFileContent = func(path string, content []byte,
	args ...interface{},
) (bool, error) {
	if len(args) != 1 {
		return false, fmt.Errorf(
			"checkBinaryFileContent requires exactly one argument: %w", errInvalidArgLength)
	}
	pfiles, ok := args[0].(*[]checker.File)
	if !ok {
		return false, fmt.Errorf(
			"checkBinaryFileContent requires argument of type *[]checker.File: %w", errInvalidArgType)
	}

	binaryFileTypes := map[string]bool{
		"crx":    true,
		"deb":    true,
		"dex":    true,
		"dey":    true,
		"elf":    true,
		"o":      true,
		"so":     true,
		"macho":  true,
		"iso":    true,
		"class":  true,
		"jar":    true,
		"bundle": true,
		"dylib":  true,
		"lib":    true,
		"msi":    true,
		"dll":    true,
		"drv":    true,
		"efi":    true,
		"exe":    true,
		"ocx":    true,
		"pyc":    true,
		"pyo":    true,
		"par":    true,
		"rpm":    true,
		"whl":    true,
	}
	var t types.Type
	var err error
	if len(content) == 0 {
		return true, nil
	}
	if t, err = filetype.Get(content); err != nil {
		return false, sce.WithMessage(sce.ErrScorecardInternal, fmt.Sprintf("filetype.Get:%v", err))
	}

	exists1 := binaryFileTypes[t.Extension]
	if exists1 {
		*pfiles = append(*pfiles, checker.File{
			Path:   path,
			Type:   checker.FileTypeBinary,
			Offset: checker.OffsetDefault,
		})
		return true, nil
	}

	exists2 := binaryFileTypes[strings.ReplaceAll(filepath.Ext(path), ".", "")]
	if !isText(content) && exists2 {
		*pfiles = append(*pfiles, checker.File{
			Path:   path,
			Type:   checker.FileTypeBinary,
			Offset: checker.OffsetDefault,
		})
	}

	return true, nil
}

// TODO: refine this function.
func isText(content []byte) bool {
	for _, c := range string(content) {
		if c == '\t' || c == '\n' || c == '\r' {
			continue
		}
		if !unicode.IsPrint(c) {
			return false
		}
	}
	return true
}

// gradleWrapperValidationOK checks for the gradle-wrapper-verify action being
// used in a non-failing workflow on the latest commit.
func gradleWrapperValidationOK(c clients.RepoClient) (bool, error) {
	gradleWrapperValidatingWorkflowFile := ""
	err := fileparser.OnMatchingFileContentDo(c, fileparser.PathMatcher{
		Pattern:       ".github/workflows/*",
		CaseSensitive: false,
	}, checkWorkflowValidatesGradleWrapper, &gradleWrapperValidatingWorkflowFile)
	if err != nil {
		return false, fmt.Errorf("%w", err)
	}
	if gradleWrapperValidatingWorkflowFile != "" {
		// If validated, check that latest commit has a relevant successful run
		runs, err := c.ListSuccessfulWorkflowRuns(gradleWrapperValidatingWorkflowFile)
		if err != nil {
			return false, fmt.Errorf("failure listing workflow runs: %w", err)
		}
		commits, err := c.ListCommits()
		if err != nil {
			return false, fmt.Errorf("failure listing commits: %w", err)
		}
		if len(commits) < 1 || len(runs) < 1 {
			return false, nil
		}
		for _, r := range runs {
			if *r.HeadSHA == commits[0].SHA {
				// Commit has corresponding successful run!
				return true, nil
			}
		}
	}
	return false, nil
}

// checkWorkflowValidatesGradleWrapper checks that the current workflow file
// is indeed using the gradle/wrapper-validation-action action, else continues.
func checkWorkflowValidatesGradleWrapper(path string, content []byte, args ...interface{}) (bool, error) {
	vwID, ok := args[0].(*string)
	if !ok {
		return false, fmt.Errorf("checkWorkflowValidatesGradleWrapper expects arg[0] of type *string: %w", errInvalidArgType)
	}

	action, errs := actionlint.Parse(content)
	if len(errs) > 0 {
		return true, errs[0]
	}

	for _, j := range action.Jobs {
		for _, s := range j.Steps {
			if ea, ok := s.Exec.(*actionlint.ExecAction); ok {
				if ea.Uses != nil && gradleWrapperValidationActionRegex.MatchString(ea.Uses.Value) {
					*vwID = filepath.Base(path)
					return true, nil
				}
			}
		}
	}
	return true, nil
}
