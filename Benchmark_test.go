package wlgows

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"os"
	"testing"
	"text/tabwriter"
)

/*
Speed and memory of sending and reading frames, on a server and a client, with
a 4096 byte reader and writer.

A table with a header, one run of each case:

	go test -run TestBenchmarkReport -report

Raw benchmark lines, for benchstat:

	go test -run '^$' -bench . -count 10 > bench.txt

Each size is the whole frame on the wire, header and payload. A size no frame
can hit, like a 128 byte server frame (127 or 130), takes the largest frame
under it. "one" is a single frame per op. "many" is 64 frames per op.

MB/s counts wire bytes, B/op and allocs/op are memory, and writes/op is how many
Writes reach the connection.
*/

const (
	benchmarkBufferSize = 4096
	benchmarkManyFrames = 64
)

var benchmarkReport = flag.Bool("report", false, "print the benchmark report table")

var benchmarkSizes = []struct {
	name  string
	total int
}{
	{"empty", 0},
	{"16B", 16},
	{"64B", 64},
	{"128B", 128},
	{"1KB", 1 << 10},
	{"4KB", 4 << 10},
	{"16KB", 16 << 10},
	{"128KB", 128 << 10},
	{"1MB", 1 << 20},
}

var benchmarkModes = []struct {
	name  string
	count int
}{
	{"one", 1},
	{"many", benchmarkManyFrames},
}

var benchmarkFrameSink *Frame

// benchmarkConn drops what is written and counts the Writes.
type benchmarkConn struct {
	fakeConn
	writes int
}

func (conn *benchmarkConn) Write(data []byte) (int, error) {
	conn.writes++
	return len(data), nil
}

// benchmarkPayload is the longest payload whose frame fits in total bytes.
func benchmarkPayload(total int, mask bool) []byte {
	payload := bytes.Repeat([]byte{'x'}, total)
	f := &Frame{Mask: mask}
	for length := total; length > 0; length-- {
		f.PayloadData = payload[:length]
		f.fillPayloadLength()
		if length+f.getHeaderSize() <= total {
			return f.PayloadData
		}
	}
	return []byte{}
}

func BenchmarkServerSend(b *testing.B) { benchmarkRun(b, benchmarkSend, false) }
func BenchmarkServerRead(b *testing.B) { benchmarkRun(b, benchmarkRead, true) }
func BenchmarkClientSend(b *testing.B) { benchmarkRun(b, benchmarkSend, true) }
func BenchmarkClientRead(b *testing.B) { benchmarkRun(b, benchmarkRead, false) }

func benchmarkRun(b *testing.B, run func(mask bool, total, count int) func(*testing.B), mask bool) {
	for _, mode := range benchmarkModes {
		for _, size := range benchmarkSizes {
			b.Run(mode.name+"/"+size.name, run(mask, size.total, mode.count))
		}
	}
}

// TestBenchmarkReport runs every case once and prints them as one table. It
// runs only with -report.
func TestBenchmarkReport(t *testing.T) {
	if !*benchmarkReport {
		t.Skip("run with -report")
	}
	sides := []struct {
		side, op string
		run      func(mask bool, total, count int) func(*testing.B)
		mask     bool
	}{
		{"server", "send", benchmarkSend, false},
		{"server", "read", benchmarkRead, true},
		{"client", "send", benchmarkSend, true},
		{"client", "read", benchmarkRead, false},
	}

	table := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(table, "side\top\tframes\tsize\tns/op\tMB/s\tB/op\tallocs/op\twrites/op\t")
	fmt.Fprintln(table, "\t\t\tbetter is\tlower\thigher\tlower\tlower\tlower\t")
	for _, side := range sides {
		for _, mode := range benchmarkModes {
			for _, size := range benchmarkSizes {
				result := testing.Benchmark(side.run(side.mask, size.total, mode.count))
				megabytesPerSecond := float64(result.Bytes) * float64(result.N) / 1e6 / result.T.Seconds()
				writes := "-"
				if side.op == "send" {
					writes = fmt.Sprintf("%.0f", result.Extra["writes/op"])
				}
				fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%d\t%.0f\t%d\t%d\t%s\t\n",
					side.side, side.op, mode.name, size.name,
					result.NsPerOp(), megabytesPerSecond,
					result.AllocedBytesPerOp(), result.AllocsPerOp(), writes)
			}
		}
		fmt.Fprintln(table, "\t\t\t\t\t\t\t\t\t")
	}
	table.Flush()
}

// benchmarkSend sends from a client when mask is true, else from a server.
// "many" buffers its frames and flushes once, the way SendData does.
func benchmarkSend(mask bool, total, count int) func(*testing.B) {
	return func(b *testing.B) {
		conn := &benchmarkConn{}
		c, err := NewConn(conn, nil, bufio.NewWriterSize(conn, benchmarkBufferSize), mask)
		if err != nil {
			b.Fatal(err)
		}
		f := &Frame{FIN: true, Opcode: OpcodeBinary, Mask: mask, PayloadData: benchmarkPayload(total, mask)}
		f.fillPayloadLength()
		b.SetBytes(int64((len(f.PayloadData) + f.getHeaderSize()) * count))
		b.ReportAllocs()
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			if count == 1 {
				err = c.SendFrame(f)
			} else {
				for j := 0; j < count && err == nil; j++ {
					err = c.bufferedWriteFrame(f)
				}
				if err == nil {
					err = c.writer.Flush()
				}
			}
			if err != nil {
				b.Fatal(err)
			}
		}

		b.ReportMetric(float64(conn.writes)/float64(b.N), "writes/op")
	}
}

// benchmarkRead reads on a server when mask is true, so the frames come masked
// from a client, else on a client. It calls GetFrameFromReader, the lowest
// level read, as the other libraries are measured at theirs.
func benchmarkRead(mask bool, total, count int) func(*testing.B) {
	return func(b *testing.B) {
		// The other side sends the frames once, so the wire is real.
		sender := newFakeConn(nil)
		senderConn, err := NewConn(sender, nil, bufio.NewWriterSize(sender, benchmarkBufferSize), mask)
		if err != nil {
			b.Fatal(err)
		}
		payload := benchmarkPayload(total, mask)
		for j := 0; j < count; j++ {
			if err := senderConn.SendFrame(&Frame{FIN: true, Opcode: OpcodeBinary, PayloadData: payload}); err != nil {
				b.Fatal(err)
			}
		}
		wire := sender.written()

		source := bytes.NewReader(wire)
		reader := bufio.NewReaderSize(source, benchmarkBufferSize)

		// Check one round before timing it.
		for j := 0; j < count; j++ {
			f, err := GetFrameFromReader(reader, 0)
			if err != nil {
				b.Fatal(err)
			}
			if !bytes.Equal(f.PayloadData, payload) {
				b.Fatal("read payload differs from the one sent")
			}
		}

		b.SetBytes(int64(len(wire)))
		b.ReportAllocs()
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			source.Reset(wire)
			reader.Reset(source)
			for j := 0; j < count; j++ {
				benchmarkFrameSink, err = GetFrameFromReader(reader, 0)
				if err != nil {
					b.Fatal(err)
				}
			}
		}
	}
}
