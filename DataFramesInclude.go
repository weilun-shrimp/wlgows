package wlgows

func (f DataFrames) IsIncludedMaskedFrame() bool {
	for _, frame := range f {
		if frame.Mask {
			return true
		}
	}
	return false
}

func (f DataFrames) IsIncludedUnMaskedFrame() bool {
	for _, frame := range f {
		if !frame.Mask {
			return true
		}
	}
	return false
}
