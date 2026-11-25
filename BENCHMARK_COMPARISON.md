**English** · [繁體中文](./BENCHMARK_COMPARISON.zh-TW.md)

# Comparison with gorilla/websocket and gobwas/ws

## Conclusion

| Area | vs gorilla | vs gobwas |
|---|---|---|
| Server send | **Win** in every case, 1.3x to 1.8x faster | **Win** in every case but One-16B, 1.1x to 1.2x faster<br>Tie on One-16B (35 vs 34 ns). **Win** on memory there: 0 allocs vs 1 |
| Server read | **Win** in every case, 1.4x to 2.1x faster | **Win** in every case, 1.4x to 2.0x faster |
| Client read | **Win** in every case, 1.4x to 3.5x faster | **Win** in every case, 1.8x to 3.4x faster |
| Client send, 16B and 1KB | Lose, 1.1x to 1.3x slower | Lose on One, 1.1x slower<br>Tie on Many64 |
| Client send, 16KB | Lose, 1.1x slower | Lose, 1.2x slower |
| Client send, 1MB | Tie | **Win**, 1.5x faster |
| Send memory | **Win** on client sends and server 16KB and 1MB<br>Tie on server One-16B and One-1KB<br>Lose on server Many64: 3 allocs per `SendData` message vs 0 | **Win**: 0 allocs, except 3 per `SendData` message |
| Read memory, 1MB frame | **Win** (1.05 MB vs 2.23 MB) | **Win** (1.05 MB vs 2.23 MB) |

wlgows wins every server send, server read and client read but one tie. It
loses only on client sends of 16KB and smaller, for a reason: see
[Why wlgows loses only on client sends](#why-wlgows-loses-only-on-client-sends).

### Why wlgows loses only on client sends

RFC 6455 5.3:

> When preparing a masked frame, the client MUST pick a fresh masking key from
> the set of allowed 32-bit values. The masking key needs to be unpredictable;
> thus, the masking key MUST be derived from a strong source of entropy, and
> the masking key for a given frame MUST NOT make it simple for a
> server/proxy to predict the masking key for a subsequent frame.

Only a client masks its frames, so client sends are the only cases that pay
for a key.

> **wlgows is the only one of the three that takes masking keys from a secure
> random source.** wlgows uses `crypto/rand`. gorilla and gobwas use
> `math/rand`, which Go documents as not for security-sensitive work (see
> [Safety and correctness first](./README.md)).

- 16B and 1KB: a key from `crypto/rand` costs 44 ns more than one from
  `math/rand`.
- 16KB: wlgows takes 4 Writes per frame, gobwas 2.

## Environment

| | |
|---|---|
| Machine | Apple M5, 10 cores, 24 GB |
| OS | macOS 26.6.2 |
| Go | go1.26.6 darwin/arm64 |
| wlgows | v7 (this checkout) |
| gorilla/websocket | v1.5.3 |
| gobwas/ws | v1.4.0 |

## Method

The cases, the calls each library makes and how to run them are in
[benchmarks/README.md](./benchmarks/README.md). Every number below is each
library's fastest run, from `optimal_comparison.json`. A gap under about 5% is
called a tie.

## Full results

ns/op, lower is better, with the fastest of the three in bold.

### Server send

| Case | wlgows | gorilla | gobwas | wlgows B / allocs | gorilla B / allocs | gobwas B / allocs | Writes (wlgows / gorilla / gobwas) |
|---|---|---|---|---|---|---|---|
| One-16B | 35 | 46 | **34** | 0 / 0 | 0 / 0 | 16 / 1 | 1 / 1 / 1 |
| Many64-16B | **1731** | 2910 | 2067 | 80 / 3 | 0 / 0 | 1024 / 64 | 1 / 64 / 1 |
| One-1KB | **53** | 69 | 59 | 0 / 0 | 0 / 0 | 16 / 1 | 1 / 1 / 1 |
| Many64-1KB | **3144** | 4412 | 3720 | 80 / 3 | 0 / 0 | 1024 / 64 | 16 / 64 / 16 |
| One-16KB | **101** | 177 | 124 | 0 / 0 | 72 / 2 | 16 / 1 | 2 / 2 / 2 |
| Many8-16KB | **811** | 1421 | 977 | 0 / 0 | 576 / 16 | 128 / 8 | 16 / 16 / 16 |
| One-1MB | **102** | 176 | 124 | 0 / 0 | 72 / 2 | 16 / 1 | 2 / 2 / 2 |
| Many8-1MB | **817** | 1419 | 992 | 0 / 0 | 576 / 16 | 128 / 8 | 16 / 16 / 16 |

### Server read

| Case | wlgows | gorilla | gobwas | wlgows B / allocs | gorilla B / allocs | gobwas B / allocs |
|---|---|---|---|---|---|---|
| One-16B | **130** | 177 | 259 | 64 / 2 | 520 / 2 | 752 / 4 |
| Many64-16B | **7697** | 10866 | 15207 | 4096 / 128 | 33280 / 128 | 48128 / 256 |
| One-1KB | **340** | 562 | 628 | 1072 / 2 | 2184 / 5 | 2416 / 7 |
| Many64-1KB | **21576** | 35817 | 39575 | 68608 / 128 | 139776 / 320 | 154624 / 448 |
| One-16KB | **3085** | 6398 | 6272 | 16432 / 2 | 37768 / 14 | 38000 / 16 |
| Many8-16KB | **25662** | 51399 | 51126 | 131456 / 16 | 302145 / 112 | 304001 / 128 |
| One-1MB | **182565** | 309070 | 290690 | 1048632 / 2 | 2227985 / 25 | 2228222 / 27 |
| Many8-1MB | **1156009** | 1620638 | 1603926 | 8388998 / 16 | 17823846 / 200 | 17825700 / 216 |

### Client send

| Case | wlgows | gorilla | gobwas | wlgows B / allocs | gorilla B / allocs | gobwas B / allocs | Writes (wlgows / gorilla / gobwas) |
|---|---|---|---|---|---|---|---|
| One-16B | 120 | **93** | 112 | 0 / 0 | 48 / 1 | 56 / 3 | 1 / 1 / 1 |
| Many64-16B | 7225 | **5893** | 6987 | 80 / 3 | 3072 / 64 | 3584 / 192 | 1 / 64 / 1 |
| One-1KB | 217 | **187** | 204 | 0 / 0 | 48 / 1 | 40 / 2 | 1 / 1 / 1 |
| Many64-1KB | 13565 | **11959** | 13011 | 80 / 3 | 3072 / 64 | 2561 / 128 | 16 / 64 / 16 |
| One-16KB | 1716 | 1596 | **1448** | 0 / 0 | 48 / 1 | 40 / 2 | 4 / 4 / 2 |
| Many8-16KB | 13739 | 12738 | **11598** | 0 / 0 | 384 / 8 | 320 / 16 | 32 / 32 / 16 |
| One-1MB | **106082** | 106460 | 159077 | 0 / 0 | 48 / 1 | 1048620 / 3 | 257 / 256 / 2 |
| Many8-1MB | **850333** | 850413 | 1276972 | 0 / 0 | 384 / 8 | 8388962 / 24 | 2056 / 2048 / 16 |

### Client read

| Case | wlgows | gorilla | gobwas | wlgows B / allocs | gorilla B / allocs | gobwas B / allocs |
|---|---|---|---|---|---|---|
| One-16B | **123** | 168 | 226 | 64 / 2 | 520 / 2 | 720 / 3 |
| Many64-16B | **7321** | 10193 | 13056 | 4096 / 128 | 33280 / 128 | 46080 / 192 |
| One-1KB | **276** | 478 | 551 | 1072 / 2 | 2184 / 5 | 2384 / 6 |
| Many64-1KB | **17720** | 30360 | 34865 | 68608 / 128 | 139776 / 320 | 152576 / 384 |
| One-16KB | **2128** | 5452 | 5457 | 16432 / 2 | 37768 / 14 | 37968 / 15 |
| Many8-16KB | **17832** | 44801 | 44611 | 131456 / 16 | 302145 / 112 | 303745 / 120 |
| One-1MB | **68848** | 241422 | 233699 | 1048629 / 2 | 2227977 / 25 | 2228179 / 26 |
| Many8-1MB | **495060** | 1177043 | 1182366 | 8388997 / 16 | 17823830 / 200 | 17825458 / 208 |
