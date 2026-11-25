package benchmarks

import (
	"bufio"
	"bytes"
	"sync"
	"testing"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// The calls a gobwas case can measure: wsutil, gobwas's highest level API.
// wsutil takes no lock, so each call takes a sync.Mutex, as a caller sharing
// the connection between goroutines must.
const (
	gobwasWriteServerMessageCall     = "sync.Mutex + wsutil.WriteServerMessage + Flush"
	gobwasWriteServerMessageLoopCall = "sync.Mutex + wsutil.WriteServerMessage loop + Flush"
	gobwasWriteClientMessageCall     = "sync.Mutex + wsutil.WriteClientMessage + Flush"
	gobwasWriteClientMessageLoopCall = "sync.Mutex + wsutil.WriteClientMessage loop + Flush"
	gobwasReadClientDataCall         = "sync.Mutex + wsutil.ReadClientData"
	gobwasReadServerDataCall         = "sync.Mutex + wsutil.ReadServerData"
)

// TestGobwas runs every case on gobwas/ws and writes results/gobwas.json.
func TestGobwas(t *testing.T) {
	cases := []libraryCase{}
	for _, benchmarkCase := range benchmarkCases {
		if benchmarkCase.operation == readOperation {
			call := gobwasReadServerDataCall
			if benchmarkCase.side == serverSide {
				call = gobwasReadClientDataCall
			}
			cases = append(cases, libraryCase{
				id:        benchmarkCase.id,
				call:      call,
				benchmark: gobwasReadData(benchmarkCase),
			})
			continue
		}
		call := gobwasWriteServerMessageCall
		switch {
		case benchmarkCase.side == clientSide && benchmarkCase.frameCount > 1:
			call = gobwasWriteClientMessageLoopCall
		case benchmarkCase.side == clientSide:
			call = gobwasWriteClientMessageCall
		case benchmarkCase.frameCount > 1:
			call = gobwasWriteServerMessageLoopCall
		}
		cases = append(cases, libraryCase{
			id:        benchmarkCase.id,
			call:      call,
			benchmark: gobwasWriteMessage(benchmarkCase),
		})
	}

	runLibrary(t, "gobwas", "github.com/gobwas/ws", cases)
}

// gobwasWriteMessage writes each frame of the case as one message with
// wsutil.WriteServerMessage, or wsutil.WriteClientMessage on a client, to a
// bufferSize bufio.Writer, each under a sync.Mutex. The last message flushes
// under its lock. A client message is copied before it is masked, so payload
// is only read.
func gobwasWriteMessage(benchmarkCase benchmarkCase) func(b *testing.B) {
	return func(b *testing.B) {
		netConn := &countingConn{}
		writer := bufio.NewWriterSize(netConn, bufferSize)
		payload := casePayload(benchmarkCase.frameSize, benchmarkCase.side)
		writeMessage := wsutil.WriteServerMessage
		if benchmarkCase.side == clientSide {
			writeMessage = wsutil.WriteClientMessage
		}
		writeLocker := &sync.Mutex{}
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			for j := 0; j < benchmarkCase.frameCount; j++ {
				writeLocker.Lock()
				err := writeMessage(writer, ws.OpBinary, payload)
				if err == nil && j == benchmarkCase.frameCount-1 {
					err = writer.Flush()
				}
				writeLocker.Unlock()
				if err != nil {
					b.Fatal(err)
				}
			}
		}

		b.ReportMetric(float64(netConn.writes)/float64(b.N), "writes/op")
	}
}

// gobwasReadData reads the case's frames from the recorded wire with
// wsutil.ReadClientData, or wsutil.ReadServerData on a client, under a
// sync.Mutex, from a bufferSize bufio.Reader, after checking them once.
func gobwasReadData(benchmarkCase benchmarkCase) func(b *testing.B) {
	return func(b *testing.B) {
		sender := benchmarkCase.side.peer()
		wire, err := recordedWire(sender, benchmarkCase.frameSize, benchmarkCase.frameCount)
		if err != nil {
			b.Fatal(err)
		}
		payload := casePayload(benchmarkCase.frameSize, sender)
		source := bytes.NewReader(wire)
		reader := bufio.NewReaderSize(source, bufferSize)
		// wsutil writes any reply to a control frame to the writer side. The
		// recorded wire has none.
		readWriter := bufio.NewReadWriter(reader, bufio.NewWriter(&countingConn{}))
		readData := wsutil.ReadServerData
		if benchmarkCase.side == serverSide {
			readData = wsutil.ReadClientData
		}

		for j := 0; j < benchmarkCase.frameCount; j++ {
			message, opcode, err := readData(readWriter)
			if err != nil {
				b.Fatal(err)
			}
			if opcode != ws.OpBinary || !bytes.Equal(message, payload) {
				b.Fatal("the message read differs from the one sent")
			}
		}
		readLocker := &sync.Mutex{}
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			source.Reset(wire)
			reader.Reset(source)
			for j := 0; j < benchmarkCase.frameCount; j++ {
				readLocker.Lock()
				_, _, err := readData(readWriter)
				readLocker.Unlock()
				if err != nil {
					b.Fatal(err)
				}
			}
		}
	}
}
