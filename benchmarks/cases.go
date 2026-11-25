/*
Package benchmarks measures wlgows, gorilla/websocket and gobwas/ws on the same
cases. Each library's test writes results/<library>.json.

Every case has an ID such as Server-Send:Frame-Many64-16B:
<Side>-<Operation>:<Topic>-<Mode>-<Size>. The size is the whole frame on the
wire, header and payload. One is a single frame per b.N iteration. Many64
and Many8 are 64 or 8 frames per iteration.
*/
package benchmarks

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/weilun-shrimp/wlgows/v7"
)

// bufferSize is the size of every library's reader and writer.
const bufferSize = 4 * 1024 // 4 KB

// side is the end of the connection a case runs on.
type side string

const (
	serverSide side = "Server"
	clientSide side = "Client"
)

// masks tells whether frames sent from this side are masked.
// RFC 6455 5.1: a client masks every frame, a server masks none.
func (s side) masks() bool {
	return s == clientSide
}

// peer is the other end of the connection, the one that sends what this side
// reads.
func (s side) peer() side {
	if s == serverSide {
		return clientSide
	}
	return serverSide
}

// operation is what a case measures.
type operation string

const (
	sendOperation operation = "Send"
	readOperation operation = "Read"
)

// caseSize is a whole frame on the wire, and how many frames a Many case
// sends or reads at that size.
type caseSize struct {
	name           string
	frameSize      int
	manyFrameCount int
}

var caseSizes = []caseSize{
	{
		name:           "16B",
		frameSize:      16,
		manyFrameCount: 64,
	},
	{
		name:           "1KB",
		frameSize:      1024, // 1 KB
		manyFrameCount: 64,
	},
	{
		name:           "16KB",
		frameSize:      16 * 1024, // 16 KB
		manyFrameCount: 8,
	},
	{
		name:           "1MB",
		frameSize:      1024 * 1024, // 1 MB
		manyFrameCount: 8,
	},
}

// benchmarkCase is one case that every library runs.
type benchmarkCase struct {
	id         string
	side       side
	operation  operation
	frameSize  int // the whole frame on the wire, header and payload
	frameCount int // frames per b.N iteration
}

// benchmarkCases is every case, in the order the result files list them.
var benchmarkCases = newBenchmarkCases()

func newBenchmarkCases() []benchmarkCase {
	cases := []benchmarkCase{}
	for _, caseSide := range []side{serverSide, clientSide} {
		for _, caseOperation := range []operation{sendOperation, readOperation} {
			for _, size := range caseSizes {
				for _, frameCount := range []int{1, size.manyFrameCount} {
					cases = append(cases, benchmarkCase{
						id:         caseID(caseSide, caseOperation, frameCount, size.name),
						side:       caseSide,
						operation:  caseOperation,
						frameSize:  size.frameSize,
						frameCount: frameCount,
					})
				}
			}
		}
	}
	return cases
}

// caseID builds an ID such as Server-Send:Frame-Many64-16B.
func caseID(caseSide side, caseOperation operation, frameCount int, sizeName string) string {
	mode := "One"
	if frameCount > 1 {
		mode = "Many" + strconv.Itoa(frameCount)
	}
	return fmt.Sprintf("%s-%s:Frame-%s-%s", caseSide, caseOperation, mode, sizeName)
}

// frameHeaderSize is the header size of a frame with this payload length.
// RFC 6455 5.2: 2 bytes, then 2 more for a 16 bit length (126 to 65535) or 8
// more for a 64 bit one, then 4 for the masking key when masked.
func frameHeaderSize(payloadLength int, sender side) int {
	headerSize := 2
	if payloadLength > 65535 {
		headerSize += 8
	} else if payloadLength > 125 {
		headerSize += 2
	}
	if sender.masks() {
		headerSize += 4
	}
	return headerSize
}

// casePayload is the longest payload whose frame from sender fits in
// frameSize bytes.
func casePayload(frameSize int, sender side) []byte {
	for length := frameSize; length > 0; length-- {
		if length+frameHeaderSize(length, sender) <= frameSize {
			return bytes.Repeat([]byte{'x'}, length)
		}
	}
	return []byte{}
}

// countingConn is a net.Conn that drops what is written and counts the Writes.
type countingConn struct {
	writes int
}

func (conn *countingConn) Read(data []byte) (int, error) { return 0, io.EOF }

func (conn *countingConn) Write(data []byte) (int, error) {
	conn.writes++
	return len(data), nil
}

func (conn *countingConn) Close() error                     { return nil }
func (conn *countingConn) LocalAddr() net.Addr              { return nil }
func (conn *countingConn) RemoteAddr() net.Addr             { return nil }
func (conn *countingConn) SetDeadline(time.Time) error      { return nil }
func (conn *countingConn) SetReadDeadline(time.Time) error  { return nil }
func (conn *countingConn) SetWriteDeadline(time.Time) error { return nil }

// recordedWire is frameCount binary frames of casePayload, sent once by
// wlgows from sender. Every library reads these same bytes.
func recordedWire(sender side, frameSize, frameCount int) ([]byte, error) {
	wire := &bytes.Buffer{}
	conn, err := wlgows.NewConn(
		&countingConn{},
		nil,
		bufio.NewWriterSize(wire, bufferSize),
		wlgows.NewConnConfig{
			PeerIsClient: sender == serverSide,
		},
	)
	if err != nil {
		return nil, err
	}
	payload := casePayload(frameSize, sender)
	for i := 0; i < frameCount; i++ {
		frame := &wlgows.Frame{
			FIN:         true,
			Opcode:      wlgows.OpcodeBinary,
			PayloadData: payload,
		}
		if err := conn.SendFrame(frame); err != nil {
			return nil, err
		}
	}
	return wire.Bytes(), nil
}

// libraryCase is how one library runs one case.
type libraryCase struct {
	id        string
	call      string // the library call it measures, written to the result file
	benchmark func(b *testing.B)
}

// libraryResult is the content of results/<library>.json.
type libraryResult struct {
	Library string       `json:"library"`
	Version string       `json:"version"`
	Go      string       `json:"go"`
	OS      string       `json:"os"`
	Arch    string       `json:"arch"`
	CPU     string       `json:"cpu"`
	Rounds  int          `json:"rounds"`
	Cases   []caseResult `json:"cases"`
}

// caseResult is every run of one case.
type caseResult struct {
	ID   string    `json:"id"`
	Call string    `json:"call"`
	Runs []caseRun `json:"runs"`
}

// caseRun is one run of one case. WritesPerOp is set only by cases that
// report writes/op, the Send cases.
type caseRun struct {
	NsPerOp     float64  `json:"ns_per_op"`
	BytesPerOp  int64    `json:"bytes_per_op"`
	AllocsPerOp int64    `json:"allocs_per_op"`
	WritesPerOp *float64 `json:"writes_per_op,omitempty"`
}

/*
runLibrary runs every case ROUNDS times, round by round, and writes
results/<library>.json. modulePath is the library's module, whose version goes
into the file.

Without ROUNDS it skips, so a plain go test runs only the unit tests.
*/
func runLibrary(t *testing.T, library, modulePath string, cases []libraryCase) {
	value := os.Getenv("ROUNDS")
	if value == "" {
		t.Skip("set ROUNDS to run the benchmark, such as ROUNDS=5")
	}
	rounds, err := roundsFromEnvironment(value)
	if err != nil {
		t.Fatal(err)
	}
	caseResults, err := measureCases(cases, rounds)
	if err != nil {
		t.Fatal(err)
	}
	result := libraryResult{
		Library: library,
		Version: moduleVersion(modulePath),
		Go:      runtime.Version(),
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
		CPU:     cpuName(),
		Rounds:  rounds,
		Cases:   caseResults,
	}
	if err := writeLibraryResult("results", result); err != nil {
		t.Fatal(err)
	}
}

// roundsFromEnvironment reads ROUNDS, a whole number of 1 or more.
func roundsFromEnvironment(value string) (int, error) {
	rounds, err := strconv.Atoi(value)
	if err != nil || rounds < 1 {
		return 0, fmt.Errorf("ROUNDS must be a whole number of 1 or more, got %q", value)
	}
	return rounds, nil
}

// measureCases runs all cases once per round, so a change in the machine's
// state spreads over every case.
func measureCases(cases []libraryCase, rounds int) ([]caseResult, error) {
	caseResults := make([]caseResult, len(cases))
	for i, measuredCase := range cases {
		caseResults[i] = caseResult{
			ID:   measuredCase.id,
			Call: measuredCase.call,
		}
	}
	for round := 0; round < rounds; round++ {
		for i, measuredCase := range cases {
			// testing.Benchmark keeps the result of a run that failed after
			// its first one, so ask the benchmark whether it failed.
			failed := false
			benchmarkResult := testing.Benchmark(func(b *testing.B) {
				defer func() { failed = b.Failed() }()
				measuredCase.benchmark(b)
			})
			if failed || benchmarkResult.N == 0 {
				return nil, fmt.Errorf("%s failed", measuredCase.id)
			}
			run := caseRun{
				NsPerOp:     float64(benchmarkResult.T.Nanoseconds()) / float64(benchmarkResult.N),
				BytesPerOp:  benchmarkResult.AllocedBytesPerOp(),
				AllocsPerOp: benchmarkResult.AllocsPerOp(),
			}
			if writesPerOp, ok := benchmarkResult.Extra["writes/op"]; ok {
				run.WritesPerOp = &writesPerOp
			}
			caseResults[i].Runs = append(caseResults[i].Runs, run)
		}
	}
	return caseResults, nil
}

// moduleVersion is the version of modulePath in go.mod, from go list, since a
// test binary carries no build info for its modules. A replaced module also
// names its replacement, such as v7.0.0 => ../. It is empty when go list fails.
func moduleVersion(modulePath string) string {
	output, err := exec.Command("go", "list", "-m", "-f", "{{.Version}}{{with .Replace}} => {{.Path}}{{end}}", modulePath).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// cpuName is the CPU model on macOS and Linux, and empty elsewhere or when
// the system does not say.
func cpuName() string {
	switch runtime.GOOS {
	case "darwin":
		output, err := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(output))
	case "linux":
		cpuInfo, err := os.ReadFile("/proc/cpuinfo")
		if err != nil {
			return ""
		}
		for _, line := range strings.Split(string(cpuInfo), "\n") {
			if name, found := strings.CutPrefix(line, "model name"); found {
				return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(name), ":"))
			}
		}
	}
	return ""
}

// writeLibraryResult writes result to <directory>/<library>.json.
func writeLibraryResult(directory string, result libraryResult) error {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	// Not HTML escaped, so a version such as v7.0.0 => ../ reads as is.
	content := &bytes.Buffer{}
	encoder := json.NewEncoder(content)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, result.Library+".json"), content.Bytes(), 0o644)
}
