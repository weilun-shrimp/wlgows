[English](./README.md) · **繁體中文**

# Benchmarks

這個資料夾用同一組案例測量 wlgows、gorilla/websocket 和 gobwas/ws。
每個函式庫寫出自己的函式庫檔案，再由 `compare` 把三者並排比較。

## 執行

只需要 Go 1.24 以上，不需要其他東西。

```
cd benchmarks
ROUNDS=5 go test -count 1 -timeout 0 ./...
go run ./compare
```

- `ROUNDS` 是每個案例要跑幾次。沒有設定時，`go test` 只跑單元測試。
- `-count 1` 讓 `go test` 重新執行，不使用快取的結果。
- `-timeout 0` 關掉 `go test` 的 10 分鐘限制。設 `ROUNDS=5` 時，三個測試在
  Apple M5 上共花了大約 11 分鐘。
- 檔案會寫到 `benchmarks/results/`。每個測試會覆寫自己的檔案。

只跑一個函式庫時，指定它的測試名稱：

```
ROUNDS=5 go test -count 1 -timeout 0 -run '^TestWlgows$' .
```

三個測試是 `TestWlgows`、`TestGorilla` 和 `TestGobwas`。`compare` 會讀取
`results/` 裡所有的函式庫檔案，舊的也會讀。請先在同一台機器上跑完三個測試，
再執行 `compare`。

## 案例

每個案例都有一個 ID，例如 `Server-Send:Frame-Many64-16B`：

| 部分 | 值 | 意思 |
|---|---|---|
| 端 | `Server`、`Client` | 案例在連線的哪一端執行。 |
| 操作 | `Send`、`Read` | 送出 frame，或讀取另一端送來的 frame。 |
| 主題 | `Frame` | binary frame，沒有壓縮。 |
| 模式 | `One`、`Many64`、`Many8` | 每個 op 的 frame 數：1、64 或 8。 |
| 大小 | `16B`、`1KB`、`16KB`、`1MB` | 整個 frame 在線路上的大小，含 header 與 payload。 |

一個 op 是把一個案例的所有 frame 送出或讀取一次。

`Many64` 搭配 16B 和 1KB，`Many8` 搭配 16KB 和 1MB，總共 32 個案例。

- Send 案例寫到一個會丟掉 bytes、並計算 Write 次數的連線。
- Read 案例讀取 wlgows 事先送出過一次的線路資料。三個函式庫讀的是同樣的 bytes。

## 各函式庫的呼叫

| | Send | Read |
|---|---|---|
| wlgows | `One` 用 `SendFrame`。`Many64` 用 `SendData` 加 `ChunkSize`：一個 64 frame 的 message，flush 一次。`Many8` 每個 frame 呼叫一次 `SendFrame`。`SendFrame` 每個 frame 都會 flush。 | `Listener` 加 `Data` hook，每遇到一個 FIN frame 算一個 message |
| gorilla/websocket | 每個 frame 呼叫一次 `WriteMessage`，在 `sync.Mutex` 保護下呼叫。每個 message 都會 flush。 | 在 `sync.Mutex` 保護下呼叫 `ReadMessage` |
| gobwas/ws | 每個 frame 呼叫一次 `wsutil.WriteServerMessage`（server）或 `wsutil.WriteClientMessage`（client），在 `sync.Mutex` 保護下呼叫。最後一個 frame 之後 `Flush` 一次。 | 在 `sync.Mutex` 保護下呼叫 `wsutil.ReadClientData`（server）或 `wsutil.ReadServerData`（client） |

每個函式庫用的，都是呼叫端本來就該用的呼叫。這些呼叫符合 RFC 6455，在多個 goroutine
共用一條連線時也是安全的：

- wlgows 在內部自己上鎖。
- gorilla 同一時間只允許一個寫入者和一個讀取者。
- gobwas `wsutil` 不上鎖。
- 所以 gorilla 和 gobwas 的每次呼叫都在 `sync.Mutex` 保護下進行。

三個函式庫都遵守同樣的規則：

- 每個讀取與寫入的緩衝區都是 4 KB。
- 每個限制都是 0，也就是不限制。
- 不設定 deadline。

gorilla 當 client 時，會把超過 4 KB 的 payload 拆成 4 KB 的 frame 送出。
這些案例的 `call` 會寫著 `split into 4 KB frames`。

## 檔案

每個函式庫檔案 `results/<library>.json` 保存每個案例的每一次執行：

| 欄位 | 意思 |
|---|---|
| `library`、`version` | 函式庫和它在 `go.mod` 裡的版本。`=> ../` 表示用的是本 repo 目前的程式碼。 |
| `go`、`os`、`arch`、`cpu` | 在哪裡執行。 |
| `rounds` | 每個案例執行了幾次。 |
| `cases` | 每個案例一筆：Server 在 Client 前面，Send 在 Read 前面，大小從 16B 到 1MB，`One` 在 `Many` 前面。 |
| `cases[].id` | 案例 ID。 |
| `cases[].call` | 這個案例測量的函式庫呼叫。 |
| `cases[].runs[]` | 每一輪一筆，依輪的順序排列。 |
| `cases[].runs[].ns_per_op` | 每個 op 花的時間，單位是奈秒。 |
| `cases[].runs[].bytes_per_op`、`cases[].runs[].allocs_per_op` | 每個 op 配置的記憶體：bytes 數，以及配置次數。 |
| `cases[].runs[].writes_per_op` | 只有 Send 案例有。每個 op 對連線呼叫幾次 Write。 |

`compare` 會再寫出兩個檔案：

- `optimal_comparison.json`：每個案例裡，每個函式庫 `ns_per_op` 最低的那次
  執行。
- `worst_comparison.json`：每個案例裡，每個函式庫 `ns_per_op` 最高的那次
  執行。

兩個檔案裡：

- `environment.<library>` 是該函式庫的 `version`、`go`、`os`、`arch`、`cpu`
  和 `rounds`。
- `cases` 列出每個案例一次。
- `cases[].libraries.<library>` 是該函式庫的 `call`，以及被選中那次執行的欄位。
  這些欄位都來自同一次執行。

`ns_per_op`、`bytes_per_op`、`allocs_per_op` 和 `writes_per_op` 都是越低越好。
