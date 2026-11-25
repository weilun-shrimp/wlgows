/*
Command compare reads every results/<library>.json and writes two files to
results:

  - optimal_comparison.json: each library's fastest run of every case.
  - worst_comparison.json: each library's slowest run of every case.

Run it from the benchmarks directory, after the library tests:

	go run ./compare
*/
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// resultsDirectory holds the library files and the comparison files.
const resultsDirectory = "results"

// comparisonFileSuffix ends the name of each file this command writes, so
// they are not read back as library files.
const comparisonFileSuffix = "_comparison.json"

// libraryResult is the content of results/<library>.json.
type libraryResult struct {
	Library string       `json:"library"`
	Version string       `json:"version"`
	Go      string       `json:"go"`
	OS      string       `json:"os"`
	Arch    string       `json:"arch"`
	CPU     string       `json:"cpu"`
	Rounds  int          `json:"rounds"`
	Cases   []caseResult `json:"cases"`
}

// caseResult is every run of one case.
type caseResult struct {
	ID   string    `json:"id"`
	Call string    `json:"call"`
	Runs []caseRun `json:"runs"`
}

// caseRun is one run of one case. WritesPerOp is set only on Send cases.
type caseRun struct {
	NsPerOp     float64  `json:"ns_per_op"`
	BytesPerOp  int64    `json:"bytes_per_op"`
	AllocsPerOp int64    `json:"allocs_per_op"`
	WritesPerOp *float64 `json:"writes_per_op,omitempty"`
}

// comparison is the content of a comparison file. Cases keep the order of
// the library files.
type comparison struct {
	Environment map[string]environment `json:"environment"`
	Cases       []caseComparison       `json:"cases"`
}

// environment is where and with what a library's results were measured.
type environment struct {
	Version string `json:"version"`
	Go      string `json:"go"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	CPU     string `json:"cpu"`
	Rounds  int    `json:"rounds"`
}

// caseComparison is one case, with the picked run of each library.
type caseComparison struct {
	ID        string                `json:"id"`
	Libraries map[string]libraryRun `json:"libraries"`
}

// libraryRun is the run picked for one library, with the call it measured.
type libraryRun struct {
	Call string `json:"call"`
	caseRun
}

var (
	ErrNoLibraryResults = errors.New("no library result files")
	ErrCaseIDsDiffer    = errors.New("the library files list different case IDs")
	ErrCaseHasNoRuns    = errors.New("a case has no runs")
	ErrLibraryRepeated  = errors.New("two library files name the same library")
)

func main() {
	if err := writeComparisonFiles(); err != nil {
		fmt.Fprintln(os.Stderr, "compare:", err)
		os.Exit(1)
	}
}

// writeComparisonFiles reads the library files and writes both comparison
// files.
func writeComparisonFiles() error {
	libraries, err := readLibraryResults(resultsDirectory)
	if err != nil {
		return err
	}
	comparisonFiles := []struct {
		fileName string
		pickRun  func(runs []caseRun) caseRun
	}{
		{
			fileName: "optimal" + comparisonFileSuffix,
			pickRun:  fastestRun,
		},
		{
			fileName: "worst" + comparisonFileSuffix,
			pickRun:  slowestRun,
		},
	}
	for _, comparisonFile := range comparisonFiles {
		result, err := compareLibraries(libraries, comparisonFile.pickRun)
		if err != nil {
			return err
		}
		// Not HTML escaped, so a version such as v7.0.0 => ../ reads as is.
		content := &bytes.Buffer{}
		encoder := json.NewEncoder(content)
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(result); err != nil {
			return err
		}
		path := filepath.Join(resultsDirectory, comparisonFile.fileName)
		if err := os.WriteFile(path, content.Bytes(), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// readLibraryResults reads every <library>.json in directory, skipping the
// comparison files.
func readLibraryResults(directory string) ([]libraryResult, error) {
	paths, err := filepath.Glob(filepath.Join(directory, "*.json"))
	if err != nil {
		return nil, err
	}
	libraries := []libraryResult{}
	for _, path := range paths {
		if strings.HasSuffix(path, comparisonFileSuffix) {
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		library := libraryResult{}
		if err := json.Unmarshal(content, &library); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		libraries = append(libraries, library)
	}
	if len(libraries) == 0 {
		return nil, fmt.Errorf("%s: %w", directory, ErrNoLibraryResults)
	}
	return libraries, nil
}

// compareLibraries puts the run pickRun picks from each library side by side,
// case by case. Every library must list the same case IDs in the same order,
// each with at least one run, and no library may come twice.
func compareLibraries(libraries []libraryResult, pickRun func(runs []caseRun) caseRun) (comparison, error) {
	if len(libraries) == 0 {
		return comparison{}, ErrNoLibraryResults
	}
	caseIDs := []string{}
	for _, libraryCase := range libraries[0].Cases {
		caseIDs = append(caseIDs, libraryCase.ID)
	}

	result := comparison{
		Environment: map[string]environment{},
		Cases:       []caseComparison{},
	}
	for _, caseID := range caseIDs {
		result.Cases = append(result.Cases, caseComparison{
			ID:        caseID,
			Libraries: map[string]libraryRun{},
		})
	}
	for _, library := range libraries {
		libraryCaseIDs := []string{}
		for _, libraryCase := range library.Cases {
			libraryCaseIDs = append(libraryCaseIDs, libraryCase.ID)
		}
		if !slices.Equal(libraryCaseIDs, caseIDs) {
			return comparison{}, fmt.Errorf("%s and %s: %w", libraries[0].Library, library.Library, ErrCaseIDsDiffer)
		}

		if _, found := result.Environment[library.Library]; found {
			return comparison{}, fmt.Errorf("%s: %w", library.Library, ErrLibraryRepeated)
		}
		result.Environment[library.Library] = environment{
			Version: library.Version,
			Go:      library.Go,
			OS:      library.OS,
			Arch:    library.Arch,
			CPU:     library.CPU,
			Rounds:  library.Rounds,
		}
		for i, libraryCase := range library.Cases {
			if len(libraryCase.Runs) == 0 {
				return comparison{}, fmt.Errorf("%s %s: %w", library.Library, libraryCase.ID, ErrCaseHasNoRuns)
			}
			result.Cases[i].Libraries[library.Library] = libraryRun{
				Call:    libraryCase.Call,
				caseRun: pickRun(libraryCase.Runs),
			}
		}
	}
	return result, nil
}

// fastestRun is the run with the lowest ns_per_op, the first one on a tie.
// runs must not be empty.
func fastestRun(runs []caseRun) caseRun {
	fastest := runs[0]
	for _, run := range runs[1:] {
		if run.NsPerOp < fastest.NsPerOp {
			fastest = run
		}
	}
	return fastest
}

// slowestRun is the run with the highest ns_per_op, the first one on a tie.
// runs must not be empty.
func slowestRun(runs []caseRun) caseRun {
	slowest := runs[0]
	for _, run := range runs[1:] {
		if run.NsPerOp > slowest.NsPerOp {
			slowest = run
		}
	}
	return slowest
}
