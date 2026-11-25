**English** · [繁體中文](./README.zh-TW.md)

# Benchmarks

This folder measures wlgows, gorilla/websocket, gobwas/ws and coder/websocket
on the same cases. Each library writes its own library file. Then `compare`
puts the four side by side.

## Run it

You need Go 1.24 or later. Nothing else.

```
cd benchmarks
ROUNDS=5 go test -count 1 -timeout 0 ./...
go run ./compare
```

- `ROUNDS` is how many times each case runs. Without it, `go test` runs only
  the unit tests.
- `-count 1` makes `go test` run again instead of using a cached result.
- `-timeout 0` turns off the 10 minute limit of `go test`. With `ROUNDS=5`,
  all four tests take about 15 minutes on an Apple M5.
- The files go to `benchmarks/results/`. Each test replaces its own file.

To run one library, name its test:

```
ROUNDS=5 go test -count 1 -timeout 0 -run '^TestWlgows$' .
```

The tests are `TestWlgows`, `TestGorilla`, `TestGobwas` and `TestCoder`.
`compare` reads every library file in `results/`, old ones too. Run all four
tests on the same machine first, then `compare`.

## The cases

Every case has an ID, such as `Server-Send:Frame-Many64-16B`:

| Part | Values | Meaning |
|---|---|---|
| Side | `Server`, `Client` | The end of the connection the case runs on. |
| Operation | `Send`, `Read` | Send frames, or read frames that the other side sent. |
| Topic | `Frame` | Binary frames, not compressed. |
| Mode | `One`, `Many64`, `Many8` | Frames per op: 1, 64 or 8. |
| Size | `16B`, `1KB`, `16KB`, `1MB` | The whole frame on the wire, header and payload. |

An op sends or reads all frames of one case once.

`Many64` goes with 16B and 1KB. `Many8` goes with 16KB and 1MB. That makes 32
cases.

- A Send case writes to a connection that drops the bytes and counts the
  Writes.
- A Read case reads a wire that wlgows sent once. All four libraries read the
  same bytes.

## What each library calls

| | Send | Read |
|---|---|---|
| wlgows | `SendFrame` for `One`. `SendData` with `ChunkSize` for `Many64`: one message of 64 frames, one flush. `SendFrame` once per frame for `Many8`. `SendFrame` flushes each frame. | `Listener` with a `Data` hook, which counts a message at each FIN frame |
| gorilla/websocket | `WriteMessage` once per frame, under a `sync.Mutex`. Each message flushes. | `ReadMessage` under a `sync.Mutex` |
| gobwas/ws | `wsutil.WriteServerMessage` (server) or `wsutil.WriteClientMessage` (client) once per frame, under a `sync.Mutex`. One `Flush` after the last frame. | `wsutil.ReadClientData` (server) or `wsutil.ReadServerData` (client) under a `sync.Mutex` |
| coder/websocket | `Write` once per frame. coder allows several writers at once, so no lock. Each message flushes. | `Read` under a `sync.Mutex` |

Each library uses the calls a caller should use. They follow RFC 6455 and are
safe when several goroutines share one connection:

- wlgows takes its locks inside.
- gorilla allows one writer and one reader at a time.
- gobwas `wsutil` takes no lock.
- coder allows several writers at once, but one reader at a time.
- So each gorilla and gobwas call, and each coder `Read`, runs under a
  `sync.Mutex`.

The same rules hold for all four:

- Every reader and writer buffer is 4 KB.
- Every limit is 0, which means no limit. coder's read limit is -1, its own
  way to say no limit.
- No deadline is set.

As a client, gorilla sends a payload larger than 4 KB as 4 KB frames. Those
cases say `split into 4 KB frames` in their `call`.

## The files

Each library file, `results/<library>.json`, holds every run of every case:

| Field | Meaning |
|---|---|
| `library`, `version` | The library and its version in `go.mod`. `=> ../` means this checkout. |
| `go`, `os`, `arch`, `cpu` | Where the runs took place. |
| `rounds` | How many runs each case has. |
| `cases` | One entry per case: Server before Client, Send before Read, sizes from 16B to 1MB, `One` before `Many`. |
| `cases[].id` | The case ID. |
| `cases[].call` | The library calls the case measures. |
| `cases[].runs[]` | One entry per round, in round order. |
| `cases[].runs[].ns_per_op` | The time per op, in nanoseconds. |
| `cases[].runs[].bytes_per_op`, `cases[].runs[].allocs_per_op` | The memory allocated per op: bytes, and how many allocations. |
| `cases[].runs[].writes_per_op` | Send cases only. How many Writes reach the connection per op. |

`compare` writes two more files:

- `optimal_comparison.json`: each library's run with the lowest `ns_per_op`
  for each case.
- `worst_comparison.json`: each library's run with the highest `ns_per_op` for
  each case.

In both files:

- `environment.<library>` holds that library's `version`, `go`, `os`, `arch`,
  `cpu` and `rounds`.
- `cases` lists every case once.
- `cases[].libraries.<library>` holds that library's `call` and the fields of
  the picked run. All of them come from the same run.

Lower is better for `ns_per_op`, `bytes_per_op`, `allocs_per_op` and
`writes_per_op`.
