package wlgows

import "testing"

func TestDataFramesIsIncludedMaskedFrame(t *testing.T) {
	tests := []struct {
		name string
		msg  DataFrames
		want bool
	}{
		{"empty message", DataFrames{}, false},
		{"all unmasked", DataFrames{{Mask: false}, {Mask: false}}, false},
		{"all masked", DataFrames{{Mask: true}, {Mask: true}}, true},
		{"mixed, masked last", DataFrames{{Mask: false}, {Mask: true}}, true},
		{"mixed, masked first", DataFrames{{Mask: true}, {Mask: false}}, true},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.msg.IsIncludedMaskedFrame(); got != testCase.want {
				t.Errorf("IsIncludedMaskedFrame() = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestDataFramesIsIncludedUnMaskedFrame(t *testing.T) {
	tests := []struct {
		name string
		msg  DataFrames
		want bool
	}{
		{"empty message", DataFrames{}, false},
		{"all unmasked", DataFrames{{Mask: false}, {Mask: false}}, true},
		{"all masked", DataFrames{{Mask: true}, {Mask: true}}, false},
		{"mixed, unmasked last", DataFrames{{Mask: true}, {Mask: false}}, true},
		{"mixed, unmasked first", DataFrames{{Mask: false}, {Mask: true}}, true},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.msg.IsIncludedUnMaskedFrame(); got != testCase.want {
				t.Errorf("IsIncludedUnMaskedFrame() = %v, want %v", got, testCase.want)
			}
		})
	}
}

// A mixed message is reported by both predicates; they are not complements.
func TestDataFramesIncludePredicatesAreIndependent(t *testing.T) {
	msg := DataFrames{{Mask: true}, {Mask: false}}
	if !msg.IsIncludedMaskedFrame() || !msg.IsIncludedUnMaskedFrame() {
		t.Error("a mixed message should report true for both predicates")
	}
	empty := DataFrames{}
	if empty.IsIncludedMaskedFrame() || empty.IsIncludedUnMaskedFrame() {
		t.Error("an empty message should report false for both predicates")
	}
}
