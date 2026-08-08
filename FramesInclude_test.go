package wlgows

import "testing"

func TestFramesIsIncludedMaskedFrame(t *testing.T) {
	tests := []struct {
		name string
		msg  Frames
		want bool
	}{
		{"empty message", Frames{}, false},
		{"all unmasked", Frames{{Mask: false}, {Mask: false}}, false},
		{"all masked", Frames{{Mask: true}, {Mask: true}}, true},
		{"mixed, masked last", Frames{{Mask: false}, {Mask: true}}, true},
		{"mixed, masked first", Frames{{Mask: true}, {Mask: false}}, true},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.msg.IsIncludedMaskedFrame(); got != testCase.want {
				t.Errorf("IsIncludedMaskedFrame() = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestFramesIsIncludedUnMaskedFrame(t *testing.T) {
	tests := []struct {
		name string
		msg  Frames
		want bool
	}{
		{"empty message", Frames{}, false},
		{"all unmasked", Frames{{Mask: false}, {Mask: false}}, true},
		{"all masked", Frames{{Mask: true}, {Mask: true}}, false},
		{"mixed, unmasked last", Frames{{Mask: true}, {Mask: false}}, true},
		{"mixed, unmasked first", Frames{{Mask: false}, {Mask: true}}, true},
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
func TestFramesIncludePredicatesAreIndependent(t *testing.T) {
	msg := Frames{{Mask: true}, {Mask: false}}
	if !msg.IsIncludedMaskedFrame() || !msg.IsIncludedUnMaskedFrame() {
		t.Error("a mixed message should report true for both predicates")
	}
	empty := Frames{}
	if empty.IsIncludedMaskedFrame() || empty.IsIncludedUnMaskedFrame() {
		t.Error("an empty message should report false for both predicates")
	}
}
