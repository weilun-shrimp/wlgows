package wlgows

func (m *Msg) IsIncludedMaskedFrame() bool {
	for _, f := range m.Frames {
		if f.Mask {
			return true
		}
	}
	return false
}

func (m *Msg) IsIncludedUnMaskedFrame() bool {
	for _, f := range m.Frames {
		if !f.Mask {
			return true
		}
	}
	return false
}
