package wlgows

import "testing"

func TestMsgIsIncludedMaskedFrame(t *testing.T) {
	tests := []struct {
		name string
		msg  Msg
		want bool
	}{
		{"empty message", Msg{}, false},
		{"all unmasked", Msg{Frames: []*Frame{{Mask: false}, {Mask: false}}}, false},
		{"all masked", Msg{Frames: []*Frame{{Mask: true}, {Mask: true}}}, true},
		{"mixed, masked last", Msg{Frames: []*Frame{{Mask: false}, {Mask: true}}}, true},
		{"mixed, masked first", Msg{Frames: []*Frame{{Mask: true}, {Mask: false}}}, true},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.msg.IsIncludedMaskedFrame(); got != testCase.want {
				t.Errorf("IsIncludedMaskedFrame() = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestMsgIsIncludedUnMaskedFrame(t *testing.T) {
	tests := []struct {
		name string
		msg  Msg
		want bool
	}{
		{"empty message", Msg{}, false},
		{"all unmasked", Msg{Frames: []*Frame{{Mask: false}, {Mask: false}}}, true},
		{"all masked", Msg{Frames: []*Frame{{Mask: true}, {Mask: true}}}, false},
		{"mixed, unmasked last", Msg{Frames: []*Frame{{Mask: true}, {Mask: false}}}, true},
		{"mixed, unmasked first", Msg{Frames: []*Frame{{Mask: false}, {Mask: true}}}, true},
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
func TestMsgIncludePredicatesAreIndependent(t *testing.T) {
	msg := Msg{Frames: []*Frame{{Mask: true}, {Mask: false}}}
	if !msg.IsIncludedMaskedFrame() || !msg.IsIncludedUnMaskedFrame() {
		t.Error("a mixed message should report true for both predicates")
	}
	empty := Msg{}
	if empty.IsIncludedMaskedFrame() || empty.IsIncludedUnMaskedFrame() {
		t.Error("an empty message should report false for both predicates")
	}
}
