**English** · [繁體中文](./BENCHMARK_COMPARISON.zh-TW.md)

# Comparison with gorilla/websocket and gobwas/ws

## Conclusion

| Area | vs gorilla | vs gobwas |
|---|---|---|
| Server send, small frame | **Win** (35 vs 42 ns) | Lose (35 vs 28 ns), but 0 allocs vs 1 |
| Server send, large frame | **Win** (104 vs 175 ns) | Tie (104 vs 112 ns) |
| Server send, 64 frames batched | **Win** (1390 vs 2823 ns) | **Win** (1390 vs 1590 ns) |
| Send memory | **Win** (0 allocs, always) | **Win** (0 allocs vs 1 per frame) |
| Client send, small frame | Lose (129 vs 88 ns), stronger masking key | Lose (129 vs 53 ns), stronger masking key |
| Client send, large frame | Tie (107 µs) | Lose (107 vs 64 µs), caller's buffer is never written |
| Read, large frame | **Win** (1.4x to 2.8x faster) | Tie |
| Read, small frame | **Win** (1.7x to 2x faster) | Lose (25% to 40% slower) |
| Read memory, 1MB frame | **Win** (1.05 MB vs 2.23 MB) | Tie (1.05 MB) |

wlgows is faster than gorilla in every case but small client sends, and it is
the only one of the three that never allocates on send. Against gobwas it wins
batched server sends, ties large frames, and loses the client send path, where
gobwas trades away safety for speed.

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
| wlgows | v6 (commit cad02b7) |
| gorilla/websocket | v1.5.3 |
| gobwas/ws | v1.4.0 |

## Method

Every library runs the cases from `Benchmark_test.go`:

- A 4096 byte reader and writer on every connection.
- Sizes are the whole frame on the wire, header and payload, from empty to 1MB.
- "one" is a single frame per op, "many" is 64 frames per op.
- Sends go to a connection that drops the bytes and counts the Writes. Reads
  come from a wire recorded once and replayed.

How each library is driven:

| | Send | Read |
|---|---|---|
| wlgows | `SendFrame`; "many" buffers the frames and flushes once | `GetNextFrame` |
| gorilla | `WriteMessage`; "many" is 64 calls, since every message is flushed | `ReadMessage` |
| gobwas | `ws.WriteFrame` to a `bufio.Writer`, `ws.MaskFrameInPlace` on a client; "many" flushes once | `ws.ReadFrame`, then `ws.Cipher` on a server |

gobwas runs at its lowest level, which is its fastest. Each case is one run, so
gaps under about 10% are noise.

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
| 16B | **1390 ns** | 2823 ns | 1590 ns |
| 1KB | **2724 ns** | 4162 ns | 3286 ns |
| 1MB | **5702 ns** | 11269 ns | 7910 ns |

**Large server frames.** A payload larger than the buffer goes to the connection
directly, with no copy: 1.7x faster than gorilla, level with gobwas.

| server send, one frame | wlgows | gorilla | gobwas |
|---|---|---|---|
| 16KB | **104 ns** | 174 ns | 109 ns |
| 1MB | **104 ns** | 175 ns | 112 ns |

**Reads, against gorilla.** wlgows reads a frame with its exact payload size.
gorilla's `ReadMessage` grows a buffer through `io.ReadAll`, so a 1MB message
takes 2.2 MB and 25 allocations.

| read, one frame | wlgows | gorilla |
|---|---|---|
| server, 1KB | **321 ns** | 545 ns |
| server, 1MB | **157 µs** | 269 µs |
| client, 1KB | **257 ns** | 472 ns |
| client, 1MB | **65 µs** | 180 µs |

## Where wlgows loses for safety

**Client send, small frames: the masking key comes from `crypto/rand`.**
RFC 6455 section 5.3 asks for a masking key from "a strong source of entropy".
wlgows reads every key from `crypto/rand`. gorilla and gobwas both use
`math/rand`, which Go documents as not for security-sensitive work. Before Go
1.20 it starts from seed 1, and any program can call `rand.Seed` to make every
key predictable. On this machine the key alone costs:

| 4 byte key | ns/op |
|---|---|
| `crypto/rand.Read` | 95 |
| `math/rand.Uint32` | 8 |

That 87 ns is the whole gap on small client frames (wlgows 129 ns, gorilla 88
ns, gobwas 53 ns at 16B).

Faster sources were measured and turned down:

| Source | ns/key | Why not |
|---|---|---|
| `math/rand/v2` top-level | 7 | Go documents it as not for security-sensitive work |
| `math/rand/v2.ChaCha8`, seeded once from `crypto/rand` | 6 | No fresh entropy after the seed: anyone who reads its 320 B of state can predict every later key |
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

## Small reads, against gobwas

`GetNextFrame` hands back a `*Frame` that the caller owns and can keep, while
`ws.ReadFrame` returns its `Frame` by value. On small frames that difference
shows; from 128KB up the two are level.

| read, one frame | wlgows | gobwas |
|---|---|---|
| server, 16B | 108 ns | 82 ns |
| client, 16B | 86 ns | 61 ns |
| client, 1MB | 65 µs | 65 µs |

## Full results

ns/op, lower is better. B/op and allocs/op are per op; for reads they include
the payload itself.

### Server send

| frames | size | wlgows | gorilla | gobwas | wlgows B/allocs | gorilla B/allocs | gobwas B/allocs |
|---|---|---|---|---|---|---|---|
| one | 16B | 35 | 42 | 28 | 0 / 0 | 0 / 0 | 16 / 1 |
| one | 1KB | 53 | 65 | 50 | 0 / 0 | 0 / 0 | 16 / 1 |
| one | 16KB | 104 | 174 | 109 | 0 / 0 | 72 / 2 | 16 / 1 |
| one | 1MB | 104 | 175 | 112 | 0 / 0 | 72 / 2 | 16 / 1 |
| many | 16B | 1390 | 2823 | 1590 | 0 / 0 | 0 / 0 | 1024 / 64 |
| many | 1KB | 2724 | 4162 | 3286 | 0 / 0 | 0 / 0 | 1024 / 64 |
| many | 1MB | 5702 | 11269 | 7910 | 0 / 0 | 4608 / 128 | 1024 / 64 |

### Client send

| frames | size | wlgows | gorilla | gobwas | wlgows B/allocs | gorilla B/allocs | gobwas B/allocs |
|---|---|---|---|---|---|---|---|
| one | 16B | 129 | 88 | 53 | 0 / 0 | 48 / 1 | 16 / 1 |
| one | 1KB | 225 | 183 | 126 | 0 / 0 | 48 / 1 | 16 / 1 |
| one | 16KB | 1742 | 1590 | 1124 | 0 / 0 | 48 / 1 | 16 / 1 |
| one | 1MB | 106595 | 106776 | 64457 | 0 / 0 | 48 / 1 | 16 / 1 |
| many | 16B | 6716 | 5724 | 3199 | 0 / 0 | 3072 / 64 | 1024 / 64 |
| many | 1KB | 13150 | 11752 | 8020 | 0 / 0 | 3072 / 64 | 1024 / 64 |
| many | 1MB | 6831948 | 6844251 | 4097135 | 0 / 0 | 3072 / 64 | 1024 / 64 |

### Server read

| frames | size | wlgows | gorilla | gobwas |
|---|---|---|---|---|
| one | 16B | 108 | 185 | 82 |
| one | 1KB | 321 | 545 | 270 |
| one | 16KB | 3071 | 6395 | 2881 |
| one | 1MB | 157407 | 268552 | 146920 |
| many | 16B | 6243 | 10477 | 4345 |
| many | 1KB | 20336 | 35343 | 17001 |
| many | 1MB | 8931950 | 12177735 | 7726554 |

### Client read

| frames | size | wlgows | gorilla | gobwas |
|---|---|---|---|---|
| one | 16B | 86 | 174 | 61 |
| one | 1KB | 257 | 472 | 219 |
| one | 16KB | 2133 | 5301 | 2092 |
| one | 1MB | 64632 | 179558 | 64896 |
| many | 16B | 4817 | 9904 | 2887 |
| many | 1KB | 16061 | 33915 | 14104 |
| many | 1MB | 3991400 | 8458970 | 3959760 |
