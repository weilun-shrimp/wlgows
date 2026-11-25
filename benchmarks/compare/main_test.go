package main

import (
	"errors"
	"reflect"
	"testing"
)

func TestCompareLibraries(t *testing.T) {
	writesPerOp := 1.0
	baseLibraries := func() []libraryResult {
		return []libraryResult{
			{
				Library: "wlgows",
				Version: "v7.0.0 => ../",
				Go:      "go1.26.6",
				OS:      "darwin",
				Arch:    "arm64",
				CPU:     "Apple M5",
				Rounds:  2,
				Cases: []caseResult{
					{
						ID:   "Server-Send:Frame-One-16B",
						Call: "SendFrame",
						Runs: []caseRun{
							{
								NsPerOp:     20,
								WritesPerOp: &writesPerOp,
							},
							{
								NsPerOp:     18,
								WritesPerOp: &writesPerOp,
							},
						},
					},
					{
						ID:   "Server-Read:Frame-One-16B",
						Call: "GetFrameHeaderFromReader + FillFramePayloadFromReader",
						Runs: []caseRun{
							{
								NsPerOp:     36,
								BytesPerOp:  64,
								AllocsPerOp: 2,
							},
						},
					},
				},
			},
			{
				Library: "gorilla",
				Version: "v1.5.3",
				Go:      "go1.26.6",
				OS:      "darwin",
				Arch:    "arm64",
				CPU:     "Apple M5",
				Rounds:  2,
				Cases: []caseResult{
					{
						ID:   "Server-Send:Frame-One-16B",
						Call: "WriteMessage",
						Runs: []caseRun{
							{
								NsPerOp:     23,
								WritesPerOp: &writesPerOp,
							},
						},
					},
					{
						ID:   "Server-Read:Frame-One-16B",
						Call: "ReadMessage",
						Runs: []caseRun{
							{
								NsPerOp:     93,
								BytesPerOp:  520,
								AllocsPerOp: 2,
							},
						},
					},
				},
			},
		}
	}

	t.Run("puts the run pickRun picks from each library side by side, in case order", func(t *testing.T) {
		pickedRuns := [][]caseRun{}
		pickLastRun := func(runs []caseRun) caseRun {
			pickedRuns = append(pickedRuns, runs)
			return runs[len(runs)-1]
		}

		got, err := compareLibraries(baseLibraries(), pickLastRun)
		if err != nil {
			t.Fatalf("compareLibraries returned %v", err)
		}
		want := comparison{
			Environment: map[string]environment{
				"wlgows": {
					Version: "v7.0.0 => ../",
					Go:      "go1.26.6",
					OS:      "darwin",
					Arch:    "arm64",
					CPU:     "Apple M5",
					Rounds:  2,
				},
				"gorilla": {
					Version: "v1.5.3",
					Go:      "go1.26.6",
					OS:      "darwin",
					Arch:    "arm64",
					CPU:     "Apple M5",
					Rounds:  2,
				},
			},
			Cases: []caseComparison{
				{
					ID: "Server-Send:Frame-One-16B",
					Libraries: map[string]libraryRun{
						"wlgows": {
							Call: "SendFrame",
							caseRun: caseRun{
								NsPerOp:     18,
								WritesPerOp: &writesPerOp,
							},
						},
						"gorilla": {
							Call: "WriteMessage",
							caseRun: caseRun{
								NsPerOp:     23,
								WritesPerOp: &writesPerOp,
							},
						},
					},
				},
				{
					ID: "Server-Read:Frame-One-16B",
					Libraries: map[string]libraryRun{
						"wlgows": {
							Call: "GetFrameHeaderFromReader + FillFramePayloadFromReader",
							caseRun: caseRun{
								NsPerOp:     36,
								BytesPerOp:  64,
								AllocsPerOp: 2,
							},
						},
						"gorilla": {
							Call: "ReadMessage",
							caseRun: caseRun{
								NsPerOp:     93,
								BytesPerOp:  520,
								AllocsPerOp: 2,
							},
						},
					},
				},
			},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("compareLibraries returned %+v, want %+v", got, want)
		}
		libraries := baseLibraries()
		wantPickedRuns := [][]caseRun{
			libraries[0].Cases[0].Runs,
			libraries[0].Cases[1].Runs,
			libraries[1].Cases[0].Runs,
			libraries[1].Cases[1].Runs,
		}
		if !reflect.DeepEqual(pickedRuns, wantPickedRuns) {
			t.Errorf("pickRun got the runs %+v, want each case's runs: %+v", pickedRuns, wantPickedRuns)
		}
	})

	t.Run("no libraries returns ErrNoLibraryResults", func(t *testing.T) {
		_, err := compareLibraries([]libraryResult{}, fastestRun)
		if !errors.Is(err, ErrNoLibraryResults) {
			t.Errorf("compareLibraries returned %v, want ErrNoLibraryResults", err)
		}
	})

	t.Run("a library with another case ID returns ErrCaseIDsDiffer", func(t *testing.T) {
		libraries := baseLibraries()
		libraries[1].Cases[1].ID = "Client-Read:Frame-One-16B"
		_, err := compareLibraries(libraries, fastestRun)
		if !errors.Is(err, ErrCaseIDsDiffer) {
			t.Errorf("compareLibraries returned %v, want ErrCaseIDsDiffer", err)
		}
	})

	t.Run("a library with the same case IDs in another order returns ErrCaseIDsDiffer", func(t *testing.T) {
		libraries := baseLibraries()
		libraries[1].Cases[0], libraries[1].Cases[1] = libraries[1].Cases[1], libraries[1].Cases[0]
		_, err := compareLibraries(libraries, fastestRun)
		if !errors.Is(err, ErrCaseIDsDiffer) {
			t.Errorf("compareLibraries returned %v, want ErrCaseIDsDiffer", err)
		}
	})

	t.Run("a library with a missing case returns ErrCaseIDsDiffer", func(t *testing.T) {
		libraries := baseLibraries()
		libraries[1].Cases = libraries[1].Cases[:1]
		_, err := compareLibraries(libraries, fastestRun)
		if !errors.Is(err, ErrCaseIDsDiffer) {
			t.Errorf("compareLibraries returned %v, want ErrCaseIDsDiffer", err)
		}
	})

	t.Run("a library that comes twice returns ErrLibraryRepeated", func(t *testing.T) {
		libraries := baseLibraries()
		libraries[1].Library = "wlgows"
		_, err := compareLibraries(libraries, fastestRun)
		if !errors.Is(err, ErrLibraryRepeated) {
			t.Errorf("compareLibraries returned %v, want ErrLibraryRepeated", err)
		}
	})

	t.Run("a case with no runs returns ErrCaseHasNoRuns", func(t *testing.T) {
		libraries := baseLibraries()
		libraries[1].Cases[1].Runs = []caseRun{}
		_, err := compareLibraries(libraries, fastestRun)
		if !errors.Is(err, ErrCaseHasNoRuns) {
			t.Errorf("compareLibraries returned %v, want ErrCaseHasNoRuns", err)
		}
	})
}

func TestFastestRun(t *testing.T) {
	tests := []struct {
		name string
		runs []caseRun
		want caseRun
	}{
		{
			name: "one run",
			runs: []caseRun{
				{
					NsPerOp:    20,
					BytesPerOp: 16,
				},
			},
			want: caseRun{
				NsPerOp:    20,
				BytesPerOp: 16,
			},
		},
		{
			name: "the lowest ns_per_op, with its own memory numbers",
			runs: []caseRun{
				{
					NsPerOp:    20,
					BytesPerOp: 16,
				},
				{
					NsPerOp:     17,
					BytesPerOp:  32,
					AllocsPerOp: 2,
				},
				{
					NsPerOp:    25,
					BytesPerOp: 16,
				},
			},
			want: caseRun{
				NsPerOp:     17,
				BytesPerOp:  32,
				AllocsPerOp: 2,
			},
		},
		{
			name: "the first one on a tie",
			runs: []caseRun{
				{
					NsPerOp:    17,
					BytesPerOp: 16,
				},
				{
					NsPerOp:    17,
					BytesPerOp: 32,
				},
			},
			want: caseRun{
				NsPerOp:    17,
				BytesPerOp: 16,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := fastestRun(test.runs); !reflect.DeepEqual(got, test.want) {
				t.Errorf("fastestRun returned %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestSlowestRun(t *testing.T) {
	tests := []struct {
		name string
		runs []caseRun
		want caseRun
	}{
		{
			name: "one run",
			runs: []caseRun{
				{
					NsPerOp:    20,
					BytesPerOp: 16,
				},
			},
			want: caseRun{
				NsPerOp:    20,
				BytesPerOp: 16,
			},
		},
		{
			name: "the highest ns_per_op, with its own memory numbers",
			runs: []caseRun{
				{
					NsPerOp:    20,
					BytesPerOp: 16,
				},
				{
					NsPerOp:     25,
					BytesPerOp:  32,
					AllocsPerOp: 2,
				},
				{
					NsPerOp:    17,
					BytesPerOp: 16,
				},
			},
			want: caseRun{
				NsPerOp:     25,
				BytesPerOp:  32,
				AllocsPerOp: 2,
			},
		},
		{
			name: "the first one on a tie",
			runs: []caseRun{
				{
					NsPerOp:    25,
					BytesPerOp: 16,
				},
				{
					NsPerOp:    25,
					BytesPerOp: 32,
				},
			},
			want: caseRun{
				NsPerOp:    25,
				BytesPerOp: 16,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := slowestRun(test.runs); !reflect.DeepEqual(got, test.want) {
				t.Errorf("slowestRun returned %+v, want %+v", got, test.want)
			}
		})
	}
}
