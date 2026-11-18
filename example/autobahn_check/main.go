// Fails when an Autobahn report has a failed or missing case.
// .github/workflows/autobahn_server.yml and autobahn_client.yml run it on
// each report.
//
//	go run ../autobahn_check reports/index.json 517
//
// The number is how many cases the report must have. A case that never ran is
// missing from the report, so any other count fails. It prints how many cases
// got each result, then every failed case.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// The name of the tested side in the report.
const agent = "wlgows"

// The results that are not failures. Unimplemented means an optional feature,
// such as a compression setting, was not agreed. See ../AUTOBAHN.md.
var passingResults = map[string]bool{
	"OK":            true,
	"NON-STRICT":    true,
	"INFORMATIONAL": true,
	"UNIMPLEMENTED": true,
}

// One case in index.json.
// Behavior is the result of the case.
// BehaviorClose is the result of its closing handshake.
type caseResult struct {
	Behavior      string `json:"behavior"`
	BehaviorClose string `json:"behaviorClose"`
}

func main() {
	if len(os.Args) != 3 {
		exit("usage: autobahn_check <index.json> <case count>")
	}
	reportPath := os.Args[1]
	caseCount, err := strconv.Atoi(os.Args[2])
	if err != nil {
		exit("case count: " + err.Error())
	}

	data, err := os.ReadFile(reportPath)
	if err != nil {
		exit(err.Error())
	}
	var report map[string]map[string]caseResult
	if err := json.Unmarshal(data, &report); err != nil {
		exit("reading " + reportPath + ": " + err.Error())
	}
	cases, ok := report[agent]
	if !ok {
		exit("the report has no agent " + agent)
	}

	counts := map[string]int{}
	var failures []string
	for caseNumber, result := range cases {
		fields := []struct{ name, value string }{
			{"behavior", result.Behavior},
			{"behaviorClose", result.BehaviorClose},
		}
		for _, field := range fields {
			counts[field.name+" "+field.value]++
			if !passingResults[field.value] {
				failures = append(failures, caseNumber+": "+field.name+" "+field.value)
			}
		}
	}

	for _, key := range sortedKeys(counts) {
		fmt.Printf("%s: %d\n", key, counts[key])
	}

	var problems []string
	if len(failures) > 0 {
		sort.Strings(failures)
		fmt.Println(strings.Join(failures, "\n"))
		problems = append(problems, fmt.Sprintf("%d failed", len(failures)))
	}
	if len(cases) != caseCount {
		problems = append(problems, fmt.Sprintf("the report has %d cases, not %d", len(cases), caseCount))
	}
	if len(problems) > 0 {
		exit(strings.Join(problems, "; "))
	}
}

func sortedKeys(counts map[string]int) []string {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// exit prints message to stderr and ends with exit code 1.
func exit(message string) {
	fmt.Fprintln(os.Stderr, "error: "+message)
	// go run exits with this code too. GitHub fails a step whose exit code
	// is not 0, and the job with it. When main returns, the code is 0.
	os.Exit(1)
}
