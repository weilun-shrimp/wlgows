package wlgows

func (f Frames) IsIncludedMaskedFrame() bool {
	for _, frame := range f {
		if frame.Mask {
			return true
		}
	}
	return false
}

func (f Frames) IsIncludedUnMaskedFrame() bool {
	for _, frame := range f {
		if !frame.Mask {
			return true
		}
	}
	return false
}
