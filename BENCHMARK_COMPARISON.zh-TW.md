[English](./BENCHMARK_COMPARISON.md) · **繁體中文**

# 與 gorilla/websocket 和 gobwas/ws 的比較

## 結論

| 項目 | 對 gorilla | 對 gobwas |
|---|---|---|
| server 送出，小 frame | **勝**（35 vs 42 ns） | 負（35 vs 28 ns），但 0 次配置 vs 1 次 |
| server 送出，大 frame | **勝**（104 vs 175 ns） | 平手（104 vs 112 ns） |
| server 送出，64 個 frame 批次 | **勝**（1390 vs 2823 ns） | **勝**（1390 vs 1590 ns） |
| 送出的記憶體 | **勝**（永遠 0 次配置） | **勝**（0 次配置 vs 每個 frame 1 次） |
| client 送出，小 frame | 負（129 vs 88 ns），masking key 較強 | 負（129 vs 53 ns），masking key 較強 |
| client 送出，大 frame | 平手（107 µs） | 負（107 vs 64 µs），從不寫入呼叫端的 buffer |
| 讀取，大 frame | **勝**（快 1.4 到 2.8 倍） | 平手 |
| 讀取，小 frame | **勝**（快 1.7 到 2 倍） | 負（慢 25% 到 40%） |
| 讀取的記憶體，1MB frame | **勝**（1.05 MB vs 2.23 MB） | 平手（1.05 MB） |

除了 client 送出小 frame 之外，wlgows 在每一項都比 gorilla 快，而且是三者中唯一
送出時從不配置記憶體的。對 gobwas，wlgows 贏下 server 批次送出，大 frame 平手，
輸掉 client 送出；在 client 送出這一側，gobwas 是拿安全性換速度。

**兩項 client 送出的落後都是刻意的。** wlgows 把正確性與安全性放在 benchmark
數字之前（見 [正確性優先](./README.zh-TW.md)）。下面每一項落後，都是量測過、
但因為會削弱這個承諾而放棄的更快做法。

## 環境

| | |
|---|---|
| 機器 | Apple M5，10 核心，24 GB |
| 作業系統 | macOS 26.6.2 |
| Go | go1.26.6 darwin/arm64 |
| wlgows | v6（commit cad02b7） |
| gorilla/websocket | v1.5.3 |
| gobwas/ws | v1.4.0 |

## 方法

每個函式庫都跑 `Benchmark_test.go` 裡的案例：

- 每條連線都有 4096 byte 的 reader 與 writer。
- 大小指的是線路上整個 frame，含 header 與 payload，從空的到 1MB。
- 「one」是每個 op 一個 frame，「many」是每個 op 64 個 frame。
- 送出時寫到一條丟掉資料、只計算 Write 次數的連線。讀取時重播一份事先錄好的
  線路資料。

各函式庫的呼叫方式：

| | 送出 | 讀取 |
|---|---|---|
| wlgows | `SendFrame`；「many」把 frame 先放進 buffer，最後 flush 一次 | `GetNextFrame` |
| gorilla | `WriteMessage`；「many」是呼叫 64 次，因為每個 message 都會 flush | `ReadMessage` |
| gobwas | `ws.WriteFrame` 寫到 `bufio.Writer`，client 端用 `ws.MaskFrameInPlace`；「many」flush 一次 | `ws.ReadFrame`，server 端再用 `ws.Cipher` |

gobwas 用的是它最底層、也是最快的 API。每個案例只跑一次，所以 10% 左右以內的
差距算是雜訊。

wlgows：

	go test -run TestBenchmarkReport -report

## wlgows 勝出的地方

**送出從不配置記憶體。** 不論 server 或 client、不論大小，每次送出都是 0 B、
0 次配置。gobwas 每個 frame 配置 16 B。gorilla 在 client 每個 frame 配置 48 B，
在 server 從 16KB 起每個 frame 配置 72 B。

**批次送出。** frame 可以先放進 buffer 再一起 flush，所以 64 個小 frame 只用一次
Write 就送到連線上。gorilla 每個 message 都會 flush，同樣 64 個 frame 要 64 次
Write，花兩倍時間。

| server 送出，64 個 frame | wlgows | gorilla | gobwas |
|---|---|---|---|
| 16B | **1390 ns** | 2823 ns | 1590 ns |
| 1KB | **2724 ns** | 4162 ns | 3286 ns |
| 1MB | **5702 ns** | 11269 ns | 7910 ns |

**server 的大 frame。** 比 buffer 大的 payload 直接寫到連線上，不做複製：比 gorilla
快 1.7 倍，與 gobwas 持平。

| server 送出，一個 frame | wlgows | gorilla | gobwas |
|---|---|---|---|
| 16KB | **104 ns** | 174 ns | 109 ns |
| 1MB | **104 ns** | 175 ns | 112 ns |

**讀取，對 gorilla。** wlgows 依 payload 的確切大小讀取 frame。gorilla 的
`ReadMessage` 透過 `io.ReadAll` 逐步擴大 buffer，所以一個 1MB 的 message 要用
2.2 MB、25 次配置。

| 讀取，一個 frame | wlgows | gorilla |
|---|---|---|
| server，1KB | **321 ns** | 545 ns |
| server，1MB | **157 µs** | 269 µs |
| client，1KB | **257 ns** | 472 ns |
| client，1MB | **65 µs** | 180 µs |

## wlgows 為了安全而落後的地方

**client 送出小 frame：masking key 來自 `crypto/rand`。**
RFC 6455 第 5.3 節要求 masking key 來自「a strong source of entropy」。wlgows
每一把 key 都從 `crypto/rand` 讀取。gorilla 和 gobwas 都用 `math/rand`，而 Go
文件明說它不適合用在安全相關的用途。Go 1.20 之前它從 seed 1 開始，而且任何程式
都能呼叫 `rand.Seed`，讓每一把 key 都變得可預測。在這台機器上，光是 key 的成本：

| 4 byte key | ns/op |
|---|---|
| `crypto/rand.Read` | 95 |
| `math/rand.Uint32` | 8 |

這 87 ns 就是 client 小 frame 的全部差距（16B 時 wlgows 129 ns、gorilla 88 ns、
gobwas 53 ns）。

量測過但放棄的更快來源：

| 來源 | ns/key | 為什麼不用 |
|---|---|---|
| `math/rand/v2` 頂層函式 | 7 | Go 文件明說它不適合用在安全相關的用途 |
| `math/rand/v2.ChaCha8`，只從 `crypto/rand` 取一次 seed | 6 | seed 之後不再有新的 entropy：任何能讀到它 320 B 狀態的人，都能預測之後的每一把 key |
| `crypto/rand`，一次讀 256 B | 7 | key 在使用前就放在記憶體裡，而且每條 client 連線多佔 256 B |

只有 `crypto/rand` 能無條件守住這個承諾，所以 wlgows 付出這 87 ns。

**client 送出大 frame：wlgows 從不寫入呼叫端的 payload。**
`ws.MaskFrameInPlace` 把 mask 直接 XOR 進呼叫端傳入的 slice。這省下一次複製，
所以 gobwas 送出 1MB frame 只要 2 次 Write，約 16 GB/s。wlgows 則是把 mask 套在
自己的 write buffer 上，需要 257 次 Write，約 10 GB/s。

原地加 mask 代表呼叫端不能傳入：

- 用 `unsafe` 從 string 轉出來的 `[]byte`，例如
  `unsafe.Slice(unsafe.StringData(s), len(s))`。如果是 string literal，這些 byte
  是唯讀的，程式會以 `unexpected fault address` 直接終止，`recover` 也接不住。
  如果是其他 string，Go 視為不可變的 string 會在所有持有它的地方被改掉。
- 唯讀記憶體，例如 `mmap` 區塊，同樣會讓程式崩潰。
- 別處還在使用的 buffer，例如要送給多條連線的 message，或還在用的 pooled
  buffer。呼叫之後裡面就是加過 mask 的 byte。

wlgows 這些都能接受，因為 README 承諾你的 payload 只會被讀取。這次複製就是
這個承諾的代價。

## 小 frame 讀取，對 gobwas

`GetNextFrame` 回傳一個呼叫端擁有、可以保留的 `*Frame`，而 `ws.ReadFrame` 以值
回傳它的 `Frame`。小 frame 時看得出這個差異；從 128KB 起兩者持平。

| 讀取，一個 frame | wlgows | gobwas |
|---|---|---|
| server，16B | 108 ns | 82 ns |
| client，16B | 86 ns | 61 ns |
| client，1MB | 65 µs | 65 µs |

## 完整結果

ns/op 越低越好。B/op 與 allocs/op 都是每個 op；讀取的數字包含 payload 本身。

### server 送出

| frames | 大小 | wlgows | gorilla | gobwas | wlgows B/allocs | gorilla B/allocs | gobwas B/allocs |
|---|---|---|---|---|---|---|---|
| one | 16B | 35 | 42 | 28 | 0 / 0 | 0 / 0 | 16 / 1 |
| one | 1KB | 53 | 65 | 50 | 0 / 0 | 0 / 0 | 16 / 1 |
| one | 16KB | 104 | 174 | 109 | 0 / 0 | 72 / 2 | 16 / 1 |
| one | 1MB | 104 | 175 | 112 | 0 / 0 | 72 / 2 | 16 / 1 |
| many | 16B | 1390 | 2823 | 1590 | 0 / 0 | 0 / 0 | 1024 / 64 |
| many | 1KB | 2724 | 4162 | 3286 | 0 / 0 | 0 / 0 | 1024 / 64 |
| many | 1MB | 5702 | 11269 | 7910 | 0 / 0 | 4608 / 128 | 1024 / 64 |

### client 送出

| frames | 大小 | wlgows | gorilla | gobwas | wlgows B/allocs | gorilla B/allocs | gobwas B/allocs |
|---|---|---|---|---|---|---|---|
| one | 16B | 129 | 88 | 53 | 0 / 0 | 48 / 1 | 16 / 1 |
| one | 1KB | 225 | 183 | 126 | 0 / 0 | 48 / 1 | 16 / 1 |
| one | 16KB | 1742 | 1590 | 1124 | 0 / 0 | 48 / 1 | 16 / 1 |
| one | 1MB | 106595 | 106776 | 64457 | 0 / 0 | 48 / 1 | 16 / 1 |
| many | 16B | 6716 | 5724 | 3199 | 0 / 0 | 3072 / 64 | 1024 / 64 |
| many | 1KB | 13150 | 11752 | 8020 | 0 / 0 | 3072 / 64 | 1024 / 64 |
| many | 1MB | 6831948 | 6844251 | 4097135 | 0 / 0 | 3072 / 64 | 1024 / 64 |

### server 讀取

| frames | 大小 | wlgows | gorilla | gobwas |
|---|---|---|---|---|
| one | 16B | 108 | 185 | 82 |
| one | 1KB | 321 | 545 | 270 |
| one | 16KB | 3071 | 6395 | 2881 |
| one | 1MB | 157407 | 268552 | 146920 |
| many | 16B | 6243 | 10477 | 4345 |
| many | 1KB | 20336 | 35343 | 17001 |
| many | 1MB | 8931950 | 12177735 | 7726554 |

### client 讀取

| frames | 大小 | wlgows | gorilla | gobwas |
|---|---|---|---|---|
| one | 16B | 86 | 174 | 61 |
| one | 1KB | 257 | 472 | 219 |
| one | 16KB | 2133 | 5301 | 2092 |
| one | 1MB | 64632 | 179558 | 64896 |
| many | 16B | 4817 | 9904 | 2887 |
| many | 1KB | 16061 | 33915 | 14104 |
| many | 1MB | 3991400 | 8458970 | 3959760 |
