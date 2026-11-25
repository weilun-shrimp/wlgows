**English** · [繁體中文](./BENCHMARK_COMPARISON.zh-TW.md)

# Comparison with gorilla/websocket, gobwas/ws and coder/websocket

## Conclusion

On speed, wlgows beats coder in every case but two ties. It beats gorilla
and gobwas almost everywhere, and loses only on 16B client sends, for a
safety reason: see
[Why wlgows loses only on 16B client sends](#why-wlgows-loses-only-on-16b-client-sends).

On memory, wlgows uses under half the memory on every read, and 0 allocs on
most sends.

| Area | vs gorilla | vs gobwas | vs coder |
|---|---|---|---|
| Server send | **Win** in every case, 1.3x to 1.7x faster | **Win** in every case but One-16B, 1.1x to 1.2x faster<br>Tie on One-16B (35 vs 34 ns). **Win** on memory there: 0 allocs vs 1 | **Win** in every case, 2.8x to 7.8x faster |
| Server read | **Win** in every case, 1.4x to 2.5x faster | **Win** in every case, 2.0x to 2.5x faster | **Win** in every case, 1.4x to 3.9x faster |
| Client read | **Win** in every case, 1.4x to 3.3x faster | **Win** in every case, 1.8x to 3.2x faster | **Win** in every case, 2.1x to 4.0x faster |
| Client send, 16B | Lose, 1.2x to 1.3x slower | Lose on One, 1.1x slower<br>Tie on Many64 | **Win**, 2.7x to 2.9x faster |
| Client send, 1KB and 16KB | **Win**, 1.1x to 1.7x faster | **Win**, 1.2x to 1.6x faster | **Win**, 1.2x to 2.3x faster |
| Client send, 1MB | **Win**, 1.8x faster | **Win**, 2.7x faster | Tie (59 vs 60 µs) |
| Send memory | **Win** on client sends and server 16KB and 1MB<br>Tie on server One-16B and One-1KB<br>Lose on server Many64: 3 allocs per `SendData` message vs 0 | **Win**: 0 allocs, except 3 per `SendData` message | Tie: 0 allocs<br>Lose on Many64: 3 allocs per `SendData` message vs 0 |
| Read memory, 1MB frame | **Win** (1.05 MB vs 2.23 MB) | **Win** (1.05 MB vs 2.23 MB) | **Win** (1.05 MB vs 2.23 MB) |

### Why wlgows loses only on 16B client sends

RFC 6455 5.3:

> When preparing a masked frame, the client MUST pick a fresh masking key from
> the set of allowed 32-bit values. The masking key needs to be unpredictable;
> thus, the masking key MUST be derived from a strong source of entropy, and
> the masking key for a given frame MUST NOT make it simple for a
> server/proxy to predict the masking key for a subsequent frame.

Only a client masks its frames, so client sends are the only cases that pay
for a key.

> **wlgows takes masking keys from a secure random source.** wlgows and coder
> use `crypto/rand`. gorilla and gobwas use `math/rand`, which Go documents as
> not for security-sensitive work (see
> [Safety and correctness first](./README.md)).

- 16B: a key from `crypto/rand` costs 44 ns more than one from `math/rand`.
- From 1KB up, the payload costs more than the key, so wlgows wins.
- coder pays for the same key, and wlgows wins every 16B case against it.

## Environment

| | |
|---|---|
| Machine | Apple M5, 10 cores, 24 GB |
| OS | macOS 26.6.2 |
| Go | go1.26.6 darwin/arm64 |
| wlgows | v7 (this checkout) |
| gorilla/websocket | v1.5.3 |
| gobwas/ws | v1.4.0 |
| coder/websocket | v1.8.15 |

## Method

The cases, the calls each library makes and how to run them are in
[benchmarks/README.md](./benchmarks/README.md). Every number below is each
library's optimal run, its fastest one, from `optimal_comparison.json`. A gap
under about 5% is called a tie.

## Full results

ns/op, lower is better, with the fastest of the four in bold.

### Server send

| Case | wlgows | gorilla | gobwas | coder | wlgows B / allocs | gorilla B / allocs | gobwas B / allocs | coder B / allocs | Writes (wlgows / gorilla / gobwas / coder) |
|---|---|---|---|---|---|---|---|---|---|
| One-16B | 35 | 46 | **34** | 219 | 0 / 0 | 0 / 0 | 16 / 1 | 0 / 0 | 1 / 1 / 1 / 1 |
| Many64-16B | **1765** | 2910 | 2067 | 13764 | 80 / 3 | 0 / 0 | 1024 / 64 | 0 / 0 | 1 / 64 / 1 / 64 |
| One-1KB | **53** | 69 | 59 | 237 | 0 / 0 | 0 / 0 | 16 / 1 | 0 / 0 | 1 / 1 / 1 / 1 |
| Many64-1KB | **3210** | 4412 | 3720 | 15045 | 80 / 3 | 0 / 0 | 1024 / 64 | 0 / 0 | 16 / 64 / 16 / 64 |
| One-16KB | **101** | 177 | 124 | 286 | 0 / 0 | 72 / 2 | 16 / 1 | 0 / 0 | 2 / 2 / 2 / 2 |
| Many8-16KB | **820** | 1421 | 977 | 2314 | 0 / 0 | 576 / 16 | 128 / 8 | 0 / 0 | 16 / 16 / 16 / 16 |
| One-1MB | **102** | 176 | 124 | 287 | 0 / 0 | 72 / 2 | 16 / 1 | 0 / 0 | 2 / 2 / 2 / 2 |
| Many8-1MB | **824** | 1419 | 992 | 2316 | 0 / 0 | 576 / 16 | 128 / 8 | 0 / 0 | 16 / 16 / 16 / 16 |

### Server read

| Case | wlgows | gorilla | gobwas | coder | wlgows B / allocs | gorilla B / allocs | gobwas B / allocs | coder B / allocs |
|---|---|---|---|---|---|---|---|---|
| One-16B | **129** | 177 | 259 | 471 | 64 / 2 | 520 / 2 | 752 / 4 | 512 / 1 |
| Many64-16B | **7630** | 10866 | 15207 | 29715 | 4096 / 128 | 33280 / 128 | 48128 / 256 | 32768 / 64 |
| One-1KB | **305** | 562 | 628 | 964 | 1072 / 2 | 2184 / 5 | 2416 / 7 | 2176 / 4 |
| Many64-1KB | **19277** | 35817 | 39575 | 61897 | 68608 / 128 | 139776 / 320 | 154624 / 448 | 139264 / 256 |
| One-16KB | **2528** | 6398 | 6272 | 5516 | 16432 / 2 | 37768 / 14 | 38000 / 16 | 37760 / 13 |
| Many8-16KB | **21444** | 51399 | 51126 | 45611 | 131456 / 16 | 302145 / 112 | 304001 / 128 | 302080 / 104 |
| One-1MB | **131539** | 309070 | 290690 | 178389 | 1048633 / 2 | 2227985 / 25 | 2228222 / 27 | 2227968 / 24 |
| Many8-1MB | **763332** | 1620638 | 1603926 | 1421432 | 8388998 / 16 | 17823846 / 200 | 17825700 / 216 | 17823746 / 192 |

### Client send

| Case | wlgows | gorilla | gobwas | coder | wlgows B / allocs | gorilla B / allocs | gobwas B / allocs | coder B / allocs | Writes (wlgows / gorilla / gobwas / coder) |
|---|---|---|---|---|---|---|---|---|---|
| One-16B | 120 | **93** | 112 | 328 | 0 / 0 | 48 / 1 | 56 / 3 | 0 / 0 | 1 / 1 / 1 / 1 |
| Many64-16B | 7222 | **5893** | 6987 | 20765 | 80 / 3 | 3072 / 64 | 3584 / 192 | 0 / 0 | 1 / 64 / 1 / 64 |
| One-1KB | **173** | 187 | 204 | 375 | 0 / 0 | 48 / 1 | 40 / 2 | 0 / 0 | 1 / 1 / 1 / 1 |
| Many64-1KB | **10532** | 11959 | 13011 | 23957 | 80 / 3 | 3072 / 64 | 2561 / 128 | 0 / 0 | 16 / 64 / 16 / 64 |
| One-16KB | **933** | 1596 | 1448 | 1128 | 0 / 0 | 48 / 1 | 40 / 2 | 0 / 0 | 4 / 4 / 2 / 4 |
| Many8-16KB | **7460** | 12738 | 11598 | 9001 | 0 / 0 | 384 / 8 | 320 / 16 | 0 / 0 | 32 / 32 / 16 / 32 |
| One-1MB | **59103** | 106460 | 159077 | 60457 | 0 / 0 | 48 / 1 | 1048620 / 3 | 0 / 0 | 257 / 256 / 2 / 256 |
| Many8-1MB | **472917** | 850413 | 1276972 | 484477 | 0 / 0 | 384 / 8 | 8388962 / 24 | 0 / 0 | 2056 / 2048 / 16 / 2048 |

### Client read

| Case | wlgows | gorilla | gobwas | coder | wlgows B / allocs | gorilla B / allocs | gobwas B / allocs | coder B / allocs |
|---|---|---|---|---|---|---|---|---|
| One-16B | **121** | 168 | 226 | 459 | 64 / 2 | 520 / 2 | 720 / 3 | 512 / 1 |
| Many64-16B | **7214** | 10193 | 13056 | 28737 | 4096 / 128 | 33280 / 128 | 46080 / 192 | 32768 / 64 |
| One-1KB | **274** | 478 | 551 | 915 | 1072 / 2 | 2184 / 5 | 2384 / 6 | 2176 / 4 |
| Many64-1KB | **17624** | 30360 | 34865 | 58492 | 68608 / 128 | 139776 / 320 | 152576 / 384 | 139264 / 256 |
| One-16KB | **2164** | 5452 | 5457 | 4760 | 16432 / 2 | 37768 / 14 | 37968 / 15 | 37760 / 13 |
| Many8-16KB | **18148** | 44801 | 44611 | 39036 | 131456 / 16 | 302145 / 112 | 303745 / 120 | 302080 / 104 |
| One-1MB | **72127** | 241422 | 233699 | 152957 | 1048629 / 2 | 2227977 / 25 | 2228179 / 26 | 2227968 / 24 |
| Many8-1MB | **501168** | 1177043 | 1182366 | 1348155 | 8388995 / 16 | 17823830 / 200 | 17825458 / 208 | 17823744 / 192 |
