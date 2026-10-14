[English](./BENCHMARK_COMPARISON.md) · **繁體中文**

# 與 gorilla/websocket 和 gobwas/ws 的比較

## 結論

| 項目 | 對 gorilla | 對 gobwas |
|---|---|---|
| server 送出，小 frame | **勝**（19 vs 23 ns） | 負（19 vs 15 ns），配置次數**勝**（0 次 vs 1 次） |
| server 送出，大 frame | **勝**（53 vs 90 ns） | **勝**（53 vs 59 ns） |
| server 送出，64 個 frame 批次 | **勝**（672 vs 1480 ns） | **勝**（672 vs 811 ns） |
| 送出的記憶體 | **勝**（永遠 0 次配置） | **勝**（0 次配置 vs 每個 frame 1 次） |
| client 送出，小 frame | 負（65 vs 46 ns）<br>安全性**勝**，masking key 較強 | 負（65 vs 28 ns）<br>安全性**勝**，masking key 較強 |
| client 送出，大 frame | 平手（55 µs） | 負（55 vs 33 µs），安全性**勝**（從不寫入呼叫端的 buffer） |
| 讀取，大 frame | **勝**（快 1.3 到 3.2 倍） | client 平手，讀單一 1MB frame 快 20%<br>server 慢 2% 到 15% |
| 讀取，小 frame | **勝**（快 1.9 到 2.8 倍） | server 16B **勝**（快 8% 到 12%）<br>1KB 時 client 平手，server 慢 4% 到 6%<br>client 16B 慢 13% 到 24% |
| 讀取的記憶體，1MB frame | **勝**（1.05 MB vs 2.23 MB） | 平手（1.05 MB） |

除了 client 送出小 frame 之外，wlgows 在每一項都比 gorilla 快，而且是三者中唯一
送出時從不配置記憶體的。對 gobwas，wlgows 贏下 server 批次送出、server 大 frame
送出與 server 16B 讀取，client 讀取 1KB 以上的單一 frame 平手，輸掉 client 送出；
在 client 送出這一側，gobwas 是拿安全性換速度。server 送出單一 16B frame、client
讀取小 frame 與 server 讀取大 frame 也落後 gobwas。

**兩項 client 送出的落後都是刻意的。** wlgows 把正確性與安全性放在 benchmark
數字之前（見 [正確性優先](./README.zh-TW.md)）。下面每一項落後，都是量測過、
但因為會削弱這個承諾而放棄的更快做法。

## 環境

| | |
|---|---|
| 機器 | Apple M5，10 核心，24 GB |
| 作業系統 | macOS 26.6.2 |
| Go | go1.26.6 darwin/arm64 |
| wlgows | v7 |
| gorilla/websocket | v1.5.3 |
| gobwas/ws | v1.4.0 |

## 方法

每個函式庫都跑 `Benchmark_test.go` 裡的案例，另外兩個透過一個逐案例照著它寫的
測試程式來跑：

- 每條連線都有 4096 byte 的 reader 與 writer。
- 大小指的是線路上整個 frame，含 header 與 payload，從空的到 1MB。
- 「one」是每個 op 一個 frame，「many」是每個 op 64 個 frame。
- 送出時寫到一條丟掉資料、只計算 Write 次數的連線。讀取時重播一份用 wlgows
  事先錄好的線路資料，所以三者讀的是完全相同的 byte。

各函式庫的呼叫方式：

| | 送出 | 讀取 |
|---|---|---|
| wlgows | `SendFrame`；「many」把 frame 先放進 buffer，最後 flush 一次 | `GetFrameHeaderFromReader`，再 `frame.FillFramePayloadFromReader` |
| gorilla | `WriteMessage`；「many」是呼叫 64 次，因為每個 message 都會 flush | `ReadMessage` |
| gobwas | `ws.WriteFrame` 寫到 `bufio.Writer`，client 端用 `ws.MaskFrameInPlace`；「many」flush 一次 | `ws.ReadFrame`，server 端再用 `ws.Cipher` |

gorilla 的 `Conn` 是用它自己的 `Upgrader` 或 `NewClient` 在那條連線上建立的，buffer
同樣是 4096 byte。gobwas 和 wlgows 都用最底層的 API 讀取：`ws.ReadFrame` 與
兩個 `FromReader` 步驟；透過 `Conn` 讀取時，每個 frame 會再加一把讀取鎖（`LockRead`）。

每個數字都是 5 次執行的中位數。每一輪三個函式庫輪流跑，所以機器狀態的變化會同樣
影響三者。5% 左右以內的差距算是雜訊。

wlgows：

	go test -run TestBenchmarkReport -report
	go test -run '^$' -bench '//^(16B|1KB|16KB|1MB)$' -count 5

## wlgows 勝出的地方

**送出從不配置記憶體。** 不論 server 或 client、不論大小，每次送出都是 0 B、
0 次配置。gobwas 每個 frame 配置 16 B。gorilla 在 client 每個 frame 配置 48 B，
在 server 從 16KB 起每個 frame 配置 72 B。

**批次送出。** frame 可以先放進 buffer 再一起 flush，所以 64 個小 frame 只用一次
Write 就送到連線上。gorilla 每個 message 都會 flush，同樣 64 個 frame 要 64 次
Write，花兩倍時間。

| server 送出，64 個 frame | wlgows | gorilla | gobwas |
|---|---|---|---|
| 16B | **672 ns** | 1480 ns | 811 ns |
| 1KB | **1368 ns** | 2148 ns | 1634 ns |
| 1MB | **2885 ns** | 5757 ns | 3802 ns |

**server 的大 frame。** 比 buffer 大的 payload 直接寫到連線上，不做複製：比 gorilla
快 1.7 倍，比 gobwas 快 10%。

| server 送出，一個 frame | wlgows | gorilla | gobwas |
|---|---|---|---|
| 16KB | **53 ns** | 89 ns | 59 ns |
| 1MB | **53 ns** | 90 ns | 59 ns |

**讀取，對 gorilla。** wlgows 依 payload 的確切大小讀取 frame。gorilla 的
`ReadMessage` 透過 `io.ReadAll` 逐步擴大 buffer，所以一個 1MB 的 message 要用
2.2 MB、25 次配置。

| 讀取，一個 frame | wlgows | gorilla |
|---|---|---|
| server，1KB | **153 ns** | 295 ns |
| server，1MB | **100 µs** | 169 µs |
| client，1KB | **123 ns** | 259 ns |
| client，1MB | **46 µs** | 147 µs |

## wlgows 為了安全而落後的地方

**client 送出小 frame：masking key 來自 `crypto/rand`。**
RFC 6455 第 5.3 節要求每一把 masking key 都來自「a strong source of entropy」，
讓它無法被預測。

masking key 不是用來保密 payload 的：它以明文放在 frame header 裡。它保護的是
路上的 proxy（第 10.3 節）。如果決定 payload 內容的程式，例如瀏覽器裡的 script，
能預測 key，它就能挑出一段 payload，讓加過 mask 之後的 byte 在不懂 WebSocket 的
proxy 看來像是 HTTP request。攻擊者的 server 再回一個偽造的 response，proxy 可能
把它當作某個真實網址的內容快取起來，再提供給其他使用者。key 無法預測，就沒有人能
控制線路上的 byte。

wlgows 每一把 key 都從 `crypto/rand` 讀取。gorilla 和 gobwas 都用 `math/rand`，而 Go
文件明說它不適合用在安全相關的用途。Go 1.20 之前它從 seed 1 開始，而且在 Go 1.24
之前任何程式都能呼叫 `rand.Seed`，讓每一把 key 都變得可預測。在這台機器上，光是 key 的成本：

| 4 byte key | ns/op |
|---|---|
| `crypto/rand.Read` | 48 |
| `math/rand.Uint32` | 4 |

這 44 ns 涵蓋了 client 小 frame 的全部差距（16B 時 wlgows 65 ns、gorilla 46 ns、
gobwas 28 ns）。

量測過但放棄的更快來源：

| 來源 | ns/key | 為什麼不用 |
|---|---|---|
| `math/rand/v2` 頂層函式 | 4 | Go 文件明說它不適合用在安全相關的用途 |
| `math/rand/v2.ChaCha8`，只從 `crypto/rand` 取一次 seed | 3 | seed 之後不再有新的 entropy：任何能讀到它 320 B 狀態的人，都能預測之後的每一把 key |
| `crypto/rand`，一次讀 256 B | 4 | key 在使用前就放在記憶體裡，而且每條 client 連線多佔 256 B |

只有 `crypto/rand` 能無條件守住這個承諾，所以 wlgows 付出這 44 ns。

**client 送出大 frame：wlgows 從不寫入呼叫端的 payload。**
`ws.MaskFrameInPlace` 把 mask 直接 XOR 進呼叫端傳入的 slice。這省下一次複製，
所以 gobwas 送出 1MB frame 只要 2 次 Write，約 32 GB/s。wlgows 則是把 mask 套在
自己的 write buffer 上，需要 257 次 Write，約 19 GB/s。

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

## 讀取，對 gobwas

v7 用 `Peek` 直接在 `bufio.Reader` 的 buffer 裡解析每個 header，所以讀一個 frame
只會配置 `Frame` 和它的 payload。在同一層級比較，也就是兩個 `FromReader` 步驟對
`ws.ReadFrame`，server 的 16B 讀取比 gobwas 快，client 讀取 1KB 以上持平。

| 讀取 | wlgows | gobwas |
|---|---|---|
| server，一個 16B | **36 ns** | 41 ns |
| server，many 16B | **2073 ns** | 2256 ns |
| client，一個 16B | 34 ns | 30 ns |
| client，many 16B | 1869 ns | 1509 ns |
| server，一個 1MB | 100 µs | 98 µs |
| client，一個 1MB | **46 µs** | 58 µs |

還剩兩個差距。client 的小 frame 慢了 13% 到 24%：每個 frame 都是一個 48 byte、
呼叫端擁有且可以保留的 `*Frame`，而 `ws.ReadFrame` 是以值回傳 `Frame`；而且即使
payload 已經在 buffer 裡，也還是透過 `io.ReadFull` 讀取。在 server 端，大 frame 慢了
2% 到 15%。不需要解除 mask 的 client 讀取持平或更快，所以這個差距在於解除 payload 的
mask：`ws.Cipher` 每一步 XOR 16 byte，`MaskPayload` 是 8 byte。

## 完整結果

ns/op，越低越好。B/op 與配置次數都是每個 op 的值；讀取的部分包含 payload 本身。
「many」的 1MB 讀取每個 op 配置 64 MB，每次執行之間最多有 25% 的差異。

### server 送出

| frame 數 | 大小 | wlgows | gorilla | gobwas | wlgows B/配置 | gorilla B/配置 | gobwas B/配置 |
|---|---|---|---|---|---|---|---|
| one | 16B | 19 | 23 | 15 | 0 / 0 | 0 / 0 | 16 / 1 |
| one | 1KB | 27 | 34 | 26 | 0 / 0 | 0 / 0 | 16 / 1 |
| one | 16KB | 53 | 89 | 59 | 0 / 0 | 72 / 2 | 16 / 1 |
| one | 1MB | 53 | 90 | 59 | 0 / 0 | 72 / 2 | 16 / 1 |
| many | 16B | 672 | 1480 | 811 | 0 / 0 | 0 / 0 | 1024 / 64 |
| many | 1KB | 1368 | 2148 | 1634 | 0 / 0 | 0 / 0 | 1024 / 64 |
| many | 1MB | 2885 | 5757 | 3802 | 0 / 0 | 4608 / 128 | 1024 / 64 |

### client 送出

| frame 數 | 大小 | wlgows | gorilla | gobwas | wlgows B/配置 | gorilla B/配置 | gobwas B/配置 |
|---|---|---|---|---|---|---|---|
| one | 16B | 65 | 46 | 28 | 0 / 0 | 48 / 1 | 16 / 1 |
| one | 1KB | 111 | 94 | 64 | 0 / 0 | 48 / 1 | 16 / 1 |
| one | 16KB | 889 | 826 | 572 | 0 / 0 | 48 / 1 | 16 / 1 |
| one | 1MB | 54629 | 54915 | 32655 | 0 / 0 | 48 / 1 | 16 / 1 |
| many | 16B | 3395 | 2950 | 1641 | 0 / 0 | 3072 / 64 | 1024 / 64 |
| many | 1KB | 6719 | 6030 | 4100 | 0 / 0 | 3072 / 64 | 1024 / 64 |
| many | 1MB | 3493092 | 3515985 | 2080063 | 0 / 0 | 3072 / 64 | 1024 / 64 |

### server 讀取

| frame 數 | 大小 | wlgows | gorilla | gobwas | wlgows B/配置 | gorilla B/配置 | gobwas B/配置 |
|---|---|---|---|---|---|---|---|
| one | 16B | 36 | 93 | 41 | 64 / 2 | 520 / 2 | 32 / 2 |
| one | 1KB | 153 | 295 | 147 | 1072 / 2 | 2184 / 5 | 1040 / 2 |
| one | 16KB | 1681 | 3550 | 1592 | 16432 / 2 | 37768 / 14 | 16400 / 2 |
| one | 1MB | 100178 | 169412 | 97963 | 1048631 / 2 | 2227995 / 25 | 1048598 / 2 |
| many | 16B | 2073 | 5556 | 2256 | 4096 / 128 | 33280 / 128 | 2048 / 128 |
| many | 1KB | 9884 | 19074 | 9298 | 68608 / 128 | 139776 / 320 | 66560 / 128 |
| many | 1MB | 4516326 | 6009597 | 3911598 | 67111941 / 128 | 142590520 / 1600 | 67109893 / 128 |

### client 讀取

| frame 數 | 大小 | wlgows | gorilla | gobwas | wlgows B/配置 | gorilla B/配置 | gobwas B/配置 |
|---|---|---|---|---|---|---|---|
| one | 16B | 34 | 89 | 30 | 64 / 2 | 520 / 2 | 32 / 2 |
| one | 1KB | 123 | 259 | 123 | 1072 / 2 | 2184 / 5 | 1040 / 2 |
| one | 16KB | 1223 | 3149 | 1227 | 16432 / 2 | 37768 / 14 | 16400 / 2 |
| one | 1MB | 46178 | 147292 | 57680 | 1048628 / 2 | 2227980 / 25 | 1048601 / 2 |
| many | 16B | 1869 | 5149 | 1509 | 4096 / 128 | 33280 / 128 | 2048 / 128 |
| many | 1KB | 7749 | 16764 | 7745 | 68608 / 128 | 139776 / 320 | 66560 / 128 |
| many | 1MB | 1745745 | 4483379 | 1736500 | 67111943 / 128 | 142590486 / 1600 | 67109895 / 128 |
