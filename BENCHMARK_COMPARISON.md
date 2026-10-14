**English** · [繁體中文](./BENCHMARK_COMPARISON.zh-TW.md)

# Comparison with gorilla/websocket and gobwas/ws

## Conclusion

| Area | vs gorilla | vs gobwas |
|---|---|---|
| Server send, small frame | **Win** (19 vs 23 ns) | Lose (19 vs 15 ns), and **Win** 0 allocs vs 1 |
| Server send, large frame | **Win** (53 vs 90 ns) | **Win** (53 vs 59 ns) |
| Server send, 64 frames batched | **Win** (672 vs 1480 ns) | **Win** (672 vs 811 ns) |
| Send memory | **Win** (0 allocs, always) | **Win** (0 allocs vs 1 per frame) |
| Client send, small frame | Lose (65 vs 46 ns)<br>**Win** on safety, stronger masking key | Lose (65 vs 28 ns)<br>**Win** on safety, stronger masking key |
| Client send, large frame | Tie (55 µs) | Lose (55 vs 33 µs), and **Win** caller's buffer is never written |
| Read, large frame | **Win** (1.3x to 3.2x faster) | Tie on a client, 20% faster on one 1MB frame<br>2% to 15% slower on a server |
| Read, small frame | **Win** (1.9x to 2.8x faster) | **Win** on a server at 16B (8% to 12% faster)<br>Tie at 1KB on a client, 4% to 6% slower on a server<br>13% to 24% slower on a client at 16B |
| Read memory, 1MB frame | **Win** (1.05 MB vs 2.23 MB) | Tie (1.05 MB) |

wlgows is faster than gorilla in every case but small client sends, and it is
the only one of the three that never allocates on send. Against gobwas it wins
batched and large server sends and 16B server reads, ties single client reads
from 1KB, and loses the client send path, where gobwas trades away safety for
speed. It also trails gobwas on single 16B server sends, small client reads and
large server reads.

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
| wlgows | `SendFrame`; "many" buffers the frames and flushes once | `GetFrameHeaderFromReader`, then `frame.FillFramePayloadFromReader` |
| gorilla | `WriteMessage`; "many" is 64 calls, since every message is flushed | `ReadMessage` |
| gobwas | `ws.WriteFrame` to a `bufio.Writer`, `ws.MaskFrameInPlace` on a client; "many" flushes once | `ws.ReadFrame`, then `ws.Cipher` on a server |

gorilla's `Conn` comes from its own `Upgrader` or `NewClient` over that
connection, with 4096 byte buffers. gobwas and wlgows read at their lowest
level, `ws.ReadFrame` and the two `FromReader` steps; reading through a `Conn`
adds one read lock per frame (`LockRead`).

Every number is the median of 5 runs. The three libraries take turns in each
round, so a change in the machine's state hits all three alike. Gaps under
about 5% are noise.

wlgows:

	go test -run TestBenchmarkReport -report
	go test -run '^$' -bench '//^(16B|1KB|16KB|1MB)$' -count 5

## Where wlgows wins

**Sending never allocates.** Every send, on a server or a client, at every size,
is 0 B and 0 allocs. gobwas allocates 16 B per frame. gorilla allocates 48 B per
client frame and 72 B per server frame from 16KB.

**Batched sends.** Frames can be buffered and flushed together, so 64 small
frames reach the connection in one Write. gorilla flushes every message, so the
same 64 frames are 64 Writes and take twice as long.

| server send, 64 frames | wlgows | gorilla | gobwas |
|---|---|---|---|
| 16B | **672 ns** | 1480 ns | 811 ns |
| 1KB | **1368 ns** | 2148 ns | 1634 ns |
| 1MB | **2885 ns** | 5757 ns | 3802 ns |

**Large server frames.** A payload larger than the buffer goes to the connection
directly, with no copy: 1.7x faster than gorilla, 10% faster than gobwas.

| server send, one frame | wlgows | gorilla | gobwas |
|---|---|---|---|
| 16KB | **53 ns** | 89 ns | 59 ns |
| 1MB | **53 ns** | 90 ns | 59 ns |

**Reads, against gorilla.** wlgows reads a frame with its exact payload size.
gorilla's `ReadMessage` grows a buffer through `io.ReadAll`, so a 1MB message
takes 2.2 MB and 25 allocations.

| read, one frame | wlgows | gorilla |
|---|---|---|
| server, 1KB | **153 ns** | 295 ns |
| server, 1MB | **100 µs** | 169 µs |
| client, 1KB | **123 ns** | 259 ns |
| client, 1MB | **46 µs** | 147 µs |

## Where wlgows loses for safety

**Client send, small frames: the masking key comes from `crypto/rand`.**
RFC 6455 section 5.3 requires every masking key to come from "a strong source of
entropy", so that it cannot be predicted.

The key does not keep the payload secret: it travels in clear in the frame
header. It protects proxies on the way (section 10.3). If code that chooses the
payload, such as a script in a browser, can predict the key, it can pick a
payload whose masked bytes look like an HTTP request to a proxy that does not
understand WebSocket. The attacker's server answers with a forged response, and
the proxy may cache it for a real URL and serve it to other users. An unpredictable key means
no one controls the bytes on the wire.

wlgows reads every key from `crypto/rand`. gorilla and gobwas both use
`math/rand`, which Go documents as not for security-sensitive work. Before Go
1.20 it starts from seed 1, and before Go 1.24 any program can call `rand.Seed`
to make every key predictable. On this machine the key alone costs:

| 4 byte key | ns/op |
|---|---|
| `crypto/rand.Read` | 48 |
| `math/rand.Uint32` | 4 |

That 44 ns covers the whole gap on small client frames (wlgows 65 ns, gorilla
46 ns, gobwas 28 ns at 16B).

Faster sources were measured and turned down:

| Source | ns/key | Why not |
|---|---|---|
| `math/rand/v2` top-level | 4 | Go documents it as not for security-sensitive work |
| `math/rand/v2.ChaCha8`, seeded once from `crypto/rand` | 3 | No fresh entropy after the seed: anyone who reads its 320 B of state can predict every later key |
| `crypto/rand`, read 256 B at a time | 4 | Keys sit in memory before use, and each client connection holds 256 B more |

Only `crypto/rand` keeps the promise without conditions, so wlgows pays the
44 ns.

**Client send, large frames: wlgows never writes to the caller's payload.**
`ws.MaskFrameInPlace` XORs the mask into the slice the caller passed. That skips
a copy, so gobwas writes a 1MB frame in 2 Writes at about 32 GB/s. wlgows masks
into its own write buffer instead, which takes 257 Writes at about 19 GB/s.

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
the two `FromReader` steps against `ws.ReadFrame`, 16B server reads are faster
than gobwas and client reads from 1KB are level.

| read | wlgows | gobwas |
|---|---|---|
| server, one 16B | **36 ns** | 41 ns |
| server, many 16B | **2073 ns** | 2256 ns |
| client, one 16B | 34 ns | 30 ns |
| client, many 16B | 1869 ns | 1509 ns |
| server, one 1MB | 100 µs | 98 µs |
| client, one 1MB | **46 µs** | 58 µs |

Two gaps remain. Small client frames are 13% to 24% slower: each one is a
48 byte `*Frame` the caller owns and can keep, where `ws.ReadFrame` returns its
`Frame` by value, and the payload goes through `io.ReadFull` even when it is
already buffered. On a server, large frames are 2% to 15% slower. Client reads,
which do not unmask, are level or faster, so that gap is in unmasking the
payload: `ws.Cipher` XORs 16 bytes per step, `MaskPayload` 8.

## Full results

ns/op, lower is better. B/op and allocs/op are per op; for reads they include
the payload itself. The "many" 1MB reads allocate 64 MB per op and vary by up to
25% between runs.

### Server send

| frames | size | wlgows | gorilla | gobwas | wlgows B/allocs | gorilla B/allocs | gobwas B/allocs |
|---|---|---|---|---|---|---|---|
| one | 16B | 19 | 23 | 15 | 0 / 0 | 0 / 0 | 16 / 1 |
| one | 1KB | 27 | 34 | 26 | 0 / 0 | 0 / 0 | 16 / 1 |
| one | 16KB | 53 | 89 | 59 | 0 / 0 | 72 / 2 | 16 / 1 |
| one | 1MB | 53 | 90 | 59 | 0 / 0 | 72 / 2 | 16 / 1 |
| many | 16B | 672 | 1480 | 811 | 0 / 0 | 0 / 0 | 1024 / 64 |
| many | 1KB | 1368 | 2148 | 1634 | 0 / 0 | 0 / 0 | 1024 / 64 |
| many | 1MB | 2885 | 5757 | 3802 | 0 / 0 | 4608 / 128 | 1024 / 64 |

### Client send

| frames | size | wlgows | gorilla | gobwas | wlgows B/allocs | gorilla B/allocs | gobwas B/allocs |
|---|---|---|---|---|---|---|---|
| one | 16B | 65 | 46 | 28 | 0 / 0 | 48 / 1 | 16 / 1 |
| one | 1KB | 111 | 94 | 64 | 0 / 0 | 48 / 1 | 16 / 1 |
| one | 16KB | 889 | 826 | 572 | 0 / 0 | 48 / 1 | 16 / 1 |
| one | 1MB | 54629 | 54915 | 32655 | 0 / 0 | 48 / 1 | 16 / 1 |
| many | 16B | 3395 | 2950 | 1641 | 0 / 0 | 3072 / 64 | 1024 / 64 |
| many | 1KB | 6719 | 6030 | 4100 | 0 / 0 | 3072 / 64 | 1024 / 64 |
| many | 1MB | 3493092 | 3515985 | 2080063 | 0 / 0 | 3072 / 64 | 1024 / 64 |

### Server read

| frames | size | wlgows | gorilla | gobwas | wlgows B/allocs | gorilla B/allocs | gobwas B/allocs |
|---|---|---|---|---|---|---|---|
| one | 16B | 36 | 93 | 41 | 64 / 2 | 520 / 2 | 32 / 2 |
| one | 1KB | 153 | 295 | 147 | 1072 / 2 | 2184 / 5 | 1040 / 2 |
| one | 16KB | 1681 | 3550 | 1592 | 16432 / 2 | 37768 / 14 | 16400 / 2 |
| one | 1MB | 100178 | 169412 | 97963 | 1048631 / 2 | 2227995 / 25 | 1048598 / 2 |
| many | 16B | 2073 | 5556 | 2256 | 4096 / 128 | 33280 / 128 | 2048 / 128 |
| many | 1KB | 9884 | 19074 | 9298 | 68608 / 128 | 139776 / 320 | 66560 / 128 |
| many | 1MB | 4516326 | 6009597 | 3911598 | 67111941 / 128 | 142590520 / 1600 | 67109893 / 128 |

### Client read

| frames | size | wlgows | gorilla | gobwas | wlgows B/allocs | gorilla B/allocs | gobwas B/allocs |
|---|---|---|---|---|---|---|---|
| one | 16B | 34 | 89 | 30 | 64 / 2 | 520 / 2 | 32 / 2 |
| one | 1KB | 123 | 259 | 123 | 1072 / 2 | 2184 / 5 | 1040 / 2 |
| one | 16KB | 1223 | 3149 | 1227 | 16432 / 2 | 37768 / 14 | 16400 / 2 |
| one | 1MB | 46178 | 147292 | 57680 | 1048628 / 2 | 2227980 / 25 | 1048601 / 2 |
| many | 16B | 1869 | 5149 | 1509 | 4096 / 128 | 33280 / 128 | 2048 / 128 |
| many | 1KB | 7749 | 16764 | 7745 | 68608 / 128 | 139776 / 320 | 66560 / 128 |
| many | 1MB | 1745745 | 4483379 | 1736500 | 67111943 / 128 | 142590486 / 1600 | 67109895 / 128 |
