**English** · [繁體中文](./BENCHMARK_COMPARISON.zh-TW.md)

# Comparison with gorilla/websocket and gobwas/ws

## Conclusion

| Area | vs gorilla | vs gobwas |
|---|---|---|
| Server send, small frame | **Win** (35 vs 45 ns) | Tie (35 vs 32 ns), and **Win** 0 allocs vs 1 |
| Server send, large frame | **Win** (107 vs 179 ns) | Tie (107 vs 112 ns) |
| Server send, 64 frames batched | **Win** (1308 vs 2741 ns) | **Win** (1308 vs 1515 ns) |
| Send memory | **Win** (0 allocs, always) | **Win** (0 allocs vs 1 per frame) |
| Client send, small frame | Lose (120 vs 90 ns)<br>**Win** on safety, stronger masking key | Lose (120 vs 56 ns)<br>**Win** on safety, stronger masking key |
| Client send, large frame | Tie (107 µs) | Lose (107 vs 64 µs), and **Win** caller's buffer is never written |
| Read, large frame | **Win** (1.5x to 3x faster) | Tie on a client, 6% to 15% slower on a server |
| Read, small frame | **Win** (2x to 3.4x faster) | **Win** on a server (up to 15% faster)<br>Tie on a client, one frame<br>14% to 19% slower on a client, many small frames back to back |
| Read memory, 1MB frame | **Win** (1.05 MB vs 2.23 MB) | Tie (1.05 MB) |

wlgows is faster than gorilla in every case but small client sends, and it is
the only one of the three that never allocates on send. Against gobwas it wins
batched server sends and small server reads, ties single server sends and
single client reads, and loses the client send path, where gobwas trades away
safety for speed. It also trails gobwas on small client frames read back to back
and on large server reads.

**Both client send losses are on purpose.** wlgows puts correctness and safety
before benchmark numbers (see [Correctness first](./README.md)). Each loss
below is a faster path that was measured and turned down because it would
weaken that promise.

## Environment

| | |
|---|---|
| Machine | Apple M5, 10 cores, 24 GB |
| OS | macOS 26.6.2 |
| Go | go1.26.6 darwin/arm64 |
| wlgows | v7 |
| gorilla/websocket | v1.5.3 |
| gobwas/ws | v1.4.0 |

## Method

Every library runs the cases from `Benchmark_test.go`, the other two through a
harness that mirrors it case for case:

- A 4096 byte reader and writer on every connection.
- Sizes are the whole frame on the wire, header and payload, from empty to 1MB.
- "one" is a single frame per op, "many" is 64 frames per op.
- Sends go to a connection that drops the bytes and counts the Writes. Reads
  come from a wire recorded once with wlgows and replayed, so all three read the
  same bytes.

How each library is driven:

| | Send | Read |
|---|---|---|
| wlgows | `SendFrame`; "many" buffers the frames and flushes once | `GetFrameFromReader` |
| gorilla | `WriteMessage`; "many" is 64 calls, since every message is flushed | `ReadMessage` |
| gobwas | `ws.WriteFrame` to a `bufio.Writer`, `ws.MaskFrameInPlace` on a client; "many" flushes once | `ws.ReadFrame`, then `ws.Cipher` on a server |

gorilla's `Conn` comes from its own `Upgrader` or `Dialer` over that
connection, with 4096 byte buffers. gobwas and wlgows read at their lowest
level, `ws.ReadFrame` and `GetFrameFromReader`; `Conn.GetNextFrame` adds a read
lock on top, about 6 ns per frame.

Sends are one run each, so gaps under about 10% are noise; the small server send
gap against gobwas was checked over 5 runs, and those numbers are the medians.
Reads are the median of 5 runs for all three.

wlgows:

	go test -run TestBenchmarkReport -report

## Where wlgows wins

**Sending never allocates.** Every send, on a server or a client, at every size,
is 0 B and 0 allocs. gobwas allocates 16 B per frame. gorilla allocates 48 B per
client frame and 72 B per server frame from 16KB.

**Batched sends.** Frames can be buffered and flushed together, so 64 small
frames reach the connection in one Write. gorilla flushes every message, so the
same 64 frames are 64 Writes and take twice as long.

| server send, 64 frames | wlgows | gorilla | gobwas |
|---|---|---|---|
| 16B | **1308 ns** | 2741 ns | 1515 ns |
| 1KB | **2690 ns** | 4174 ns | 3173 ns |
| 1MB | **5727 ns** | 11339 ns | 6929 ns |

**Large server frames.** A payload larger than the buffer goes to the connection
directly, with no copy: 1.7x faster than gorilla, level with gobwas.

| server send, one frame | wlgows | gorilla | gobwas |
|---|---|---|---|
| 16KB | **104 ns** | 175 ns | 111 ns |
| 1MB | **107 ns** | 179 ns | 112 ns |

**Reads, against gorilla.** wlgows reads a frame with its exact payload size.
gorilla's `ReadMessage` grows a buffer through `io.ReadAll`, so a 1MB message
takes 2.2 MB and 25 allocations.

| read, one frame | wlgows | gorilla |
|---|---|---|
| server, 1KB | **279 ns** | 548 ns |
| server, 1MB | **160 µs** | 245 µs |
| client, 1KB | **217 ns** | 474 ns |
| client, 1MB | **65 µs** | 178 µs |

## Where wlgows loses for safety

**Client send, small frames: the masking key comes from `crypto/rand`.**
RFC 6455 section 5.3 requires every masking key to come from "a strong source of
entropy", so that it cannot be predicted.

The key does not keep the payload secret: it travels in clear in the frame
header. It protects proxies on the way (section 10.3). If code that chooses the
payload, such as a script in a browser, can predict the key, it can pick a
payload whose masked bytes look like an HTTP request and response to a proxy
that does not understand WebSocket. The proxy may then cache that forged
response for a real URL and serve it to other users. An unpredictable key means
no one controls the bytes on the wire.

wlgows reads every key from `crypto/rand`. gorilla and gobwas both use
`math/rand`, which Go documents as not for security-sensitive work. Before Go
1.20 it starts from seed 1, and any program can call `rand.Seed` to make every
key predictable. On this machine the key alone costs:

| 4 byte key | ns/op |
|---|---|
| `crypto/rand.Read` | 95 |
| `math/rand.Uint32` | 8 |

That 87 ns covers the whole gap on small client frames (wlgows 120 ns, gorilla
90 ns, gobwas 56 ns at 16B).

Faster sources were measured and turned down:

| Source | ns/key | Why not |
|---|---|---|
| `math/rand/v2` top-level | 7 | Go documents it as not for security-sensitive work |
| `math/rand/v2.ChaCha8`, seeded once from `crypto/rand` | 5 | No fresh entropy after the seed: anyone who reads its 320 B of state can predict every later key |
| `crypto/rand`, read 256 B at a time | 7 | Keys sit in memory before use, and each client connection holds 256 B more |

Only `crypto/rand` keeps the promise without conditions, so wlgows pays the
87 ns.

**Client send, large frames: wlgows never writes to the caller's payload.**
`ws.MaskFrameInPlace` XORs the mask into the slice the caller passed. That skips
a copy, so gobwas writes a 1MB frame in 2 Writes at about 16 GB/s. wlgows masks
into its own write buffer instead, which takes 257 Writes at about 10 GB/s.

Masking in place means the caller cannot pass:

- A `[]byte` made from a string with `unsafe`, such as
  `unsafe.Slice(unsafe.StringData(s), len(s))`. For a string literal the bytes
  are read-only, and the program dies with `unexpected fault address`, which
  `recover` cannot catch. For any other string, a string Go treats as immutable
  is changed under everything that holds it.
- Read-only memory such as an `mmap` region, which crashes the same way.
- A buffer still in use elsewhere, such as a message sent to many connections or
  a pooled buffer. After the call it holds masked bytes.

wlgows accepts all of these, because the README promises that your payload is
only read. The copy is the price of that promise.

## Reads, against gobwas

v7 parses each header in place from the `bufio.Reader` with `Peek`, so a read
allocates only the `Frame` and its payload. Read at the same level,
`GetFrameFromReader` against `ws.ReadFrame`, small server reads are faster than
gobwas and single client reads are level.

| read | wlgows | gobwas |
|---|---|---|
| server, one 16B | **71 ns** | 84 ns |
| server, many 16B | **3898 ns** | 4318 ns |
| client, one 16B | 64 ns | 61 ns |
| client, many 16B | 3428 ns | 2872 ns |
| server, one 1MB | 160 µs | 142 µs |
| client, one 1MB | 65 µs | 65 µs |

Two gaps remain. Small client frames read back to back are 14% to 19% slower:
each one is a 48 byte `*Frame` the caller owns and can keep, where
`ws.ReadFrame` returns its `Frame` by value, and the payload goes through
`io.ReadFull` even when it is already buffered. On a server, large frames are
6% to 15% slower. Client reads, which do not unmask, are level, so that gap is
in unmasking the payload: `ws.Cipher` XORs 16 bytes per step, `MaskPayload` 8.

## Full results

ns/op, lower is better. B/op and allocs/op are per op; for reads they include
the payload itself. The "many" 1MB reads allocate 64 MB per op and vary by up to
30% between runs.

### Server send

| frames | size | wlgows | gorilla | gobwas | wlgows B/allocs | gorilla B/allocs | gobwas B/allocs |
|---|---|---|---|---|---|---|---|
| one | 16B | 35 | 45 | 32 | 0 / 0 | 0 / 0 | 16 / 1 |
| one | 1KB | 52 | 67 | 52 | 0 / 0 | 0 / 0 | 16 / 1 |
| one | 16KB | 104 | 175 | 111 | 0 / 0 | 72 / 2 | 16 / 1 |
| one | 1MB | 107 | 179 | 112 | 0 / 0 | 72 / 2 | 16 / 1 |
| many | 16B | 1308 | 2741 | 1515 | 0 / 0 | 0 / 0 | 1024 / 64 |
| many | 1KB | 2690 | 4174 | 3173 | 0 / 0 | 0 / 0 | 1024 / 64 |
| many | 1MB | 5727 | 11339 | 6929 | 0 / 0 | 4608 / 128 | 1024 / 64 |

### Client send

| frames | size | wlgows | gorilla | gobwas | wlgows B/allocs | gorilla B/allocs | gobwas B/allocs |
|---|---|---|---|---|---|---|---|
| one | 16B | 120 | 90 | 56 | 0 / 0 | 48 / 1 | 16 / 1 |
| one | 1KB | 218 | 185 | 126 | 0 / 0 | 48 / 1 | 16 / 1 |
| one | 16KB | 1725 | 1599 | 1112 | 0 / 0 | 48 / 1 | 16 / 1 |
| one | 1MB | 106618 | 106892 | 64025 | 0 / 0 | 48 / 1 | 16 / 1 |
| many | 16B | 6716 | 5750 | 3206 | 0 / 0 | 3072 / 64 | 1024 / 64 |
| many | 1KB | 13144 | 11770 | 8050 | 0 / 0 | 3072 / 64 | 1024 / 64 |
| many | 1MB | 6845576 | 6862762 | 4088968 | 0 / 0 | 3072 / 64 | 1024 / 64 |

### Server read

| frames | size | wlgows | gorilla | gobwas | wlgows B/allocs | gorilla B/allocs | gobwas B/allocs |
|---|---|---|---|---|---|---|---|
| one | 16B | 71 | 176 | 84 | 64 / 2 | 520 / 2 | 32 / 2 |
| one | 1KB | 279 | 548 | 268 | 1072 / 2 | 2184 / 5 | 1040 / 2 |
| one | 16KB | 2975 | 6312 | 2748 | 16432 / 2 | 37768 / 14 | 16400 / 2 |
| one | 1MB | 159546 | 245472 | 141980 | 1048630 / 2 | 2227983 / 25 | 1048596 / 2 |
| many | 16B | 3898 | 10488 | 4318 | 4096 / 128 | 33280 / 128 | 2048 / 128 |
| many | 1KB | 18090 | 35408 | 16722 | 68608 / 128 | 139776 / 320 | 66560 / 128 |
| many | 1MB | 10126184 | 11865124 | 7624184 | 67111941 / 128 | 142590481 / 1600 | 67109892 / 128 |

### Client read

| frames | size | wlgows | gorilla | gobwas | wlgows B/allocs | gorilla B/allocs | gobwas B/allocs |
|---|---|---|---|---|---|---|---|
| one | 16B | 64 | 166 | 61 | 64 / 2 | 520 / 2 | 32 / 2 |
| one | 1KB | 217 | 474 | 221 | 1072 / 2 | 2184 / 5 | 1040 / 2 |
| one | 16KB | 2175 | 5430 | 2092 | 16432 / 2 | 37768 / 14 | 16400 / 2 |
| one | 1MB | 65280 | 178005 | 64544 | 1048631 / 2 | 2227983 / 25 | 1048594 / 2 |
| many | 16B | 3428 | 9997 | 2872 | 4096 / 128 | 33280 / 128 | 2048 / 128 |
| many | 1KB | 14172 | 30488 | 13452 | 68608 / 128 | 139776 / 320 | 66560 / 128 |
| many | 1MB | 3968289 | 10904299 | 4102774 | 67111963 / 128 | 142590480 / 1600 | 67109894 / 128 |
