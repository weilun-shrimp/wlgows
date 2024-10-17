package msg

func (msg *Msg) IsIncludedMaskedFrame() bool {
	for _, f := range msg.Frames {
		if f.Mask {
			return true
		}
	}
	return false
}

func (msg *Msg) IsIncludedUnMaskedFrame() bool {
	for _, f := range msg.Frames {
		if !f.Mask {
			return true
		}
	}
	return false
}
