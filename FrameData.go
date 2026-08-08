package wlgows

import "fmt"

/*
NewDataFrame builds one data frame, refusing a non data opcode before the frame
exists.

It takes NewFrameConfig unchanged. A data frame is a plain frame with a data
opcode, so there is nothing NewControlFrameConfig's shape would add.

Two rules NewControlFrame keeps do not apply here:

  - FIN is yours. A data frame may be one fragment of a larger message (RFC 6455
    5.4), so forcing it would make fragmenting impossible. NewFrameConfig
    defaults it to false — set it on the frame that ends the message, which for
    an unfragmented one is the only frame.
  - There is no payload cap. The 125 bytes is a control frame rule (5.5), and a
    data frame's length is bounded only by what NewFrame can encode.

The fragmentation sequence 5.4 describes — the first frame carrying the opcode,
every continuation carrying OpcodeContinuation, only the last setting FIN — spans
several frames, so no single frame can be checked against it. That stays with
the caller.
*/
func NewDataFrame(config NewFrameConfig) (*Frame, error) {
	if !IsDataOpcode(config.Opcode) {
		return nil, fmt.Errorf("opcode %#x: %w", config.Opcode, ErrNotDataFrameOpcode)
	}
	return NewFrame(config)
}
