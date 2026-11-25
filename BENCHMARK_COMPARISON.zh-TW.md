[English](./BENCHMARK_COMPARISON.md) · **繁體中文**

# 與 gorilla/websocket 和 gobwas/ws 的比較

## 結論

| 項目 | 對 gorilla | 對 gobwas |
|---|---|---|
| server 送出 | 每個案例都**勝**，快 1.3 到 1.8 倍 | 除了 One-16B，每個案例都**勝**，快 1.1 到 1.2 倍<br>One-16B 平手（35 vs 34 ns），配置次數**勝**（0 次 vs 1 次） |
| server 讀取 | 每個案例都**勝**，快 1.4 到 2.1 倍 | 每個案例都**勝**，快 1.4 到 2.0 倍 |
| client 讀取 | 每個案例都**勝**，快 1.4 到 3.5 倍 | 每個案例都**勝**，快 1.8 到 3.4 倍 |
| client 送出，16B 和 1KB | 負，慢 1.1 到 1.3 倍 | One 負，慢 1.1 倍<br>Many64 平手 |
| client 送出，16KB | 負，慢 1.1 倍 | 負，慢 1.2 倍 |
| client 送出，1MB | 平手 | **勝**，快 1.5 倍 |
| 送出的記憶體 | client 送出、server 16KB 和 1MB **勝**<br>server One-16B 和 One-1KB 平手<br>server Many64 負：`SendData` 每個 message 配置 3 次，gorilla 0 次 | **勝**：0 次配置，只有 `SendData` 每個 message 配置 3 次 |
| 讀取的記憶體，1MB frame | **勝**（1.05 MB vs 2.23 MB） | **勝**（1.05 MB vs 2.23 MB） |

除了一個平手，wlgows 在 server 送出、server 讀取和 client 讀取全部勝出。它只在
16KB 以下的 client 送出時輸，而且是有原因的，請看
[為什麼 wlgows 只在 client 送出時輸](#為什麼-wlgows-只在-client-送出時輸)。

### 為什麼 wlgows 只在 client 送出時輸

RFC 6455 5.3：

> When preparing a masked frame, the client MUST pick a fresh masking key from
> the set of allowed 32-bit values. The masking key needs to be unpredictable;
> thus, the masking key MUST be derived from a strong source of entropy, and
> the masking key for a given frame MUST NOT make it simple for a
> server/proxy to predict the masking key for a subsequent frame.

只有 client 會對 frame 做 masking，所以只有 client 送出的案例要付 masking key
的成本。

> **三者之中，只有 wlgows 從安全的亂數來源取得 masking key。** wlgows 用
> `crypto/rand`。gorilla 和 gobwas 用 `math/rand`，Go 的文件寫明它不該用在
> 和安全相關的用途（請看[安全與正確性優先](./README.zh-TW.md)）。

- 16B 和 1KB：從 `crypto/rand` 取一把 key，比從 `math/rand` 多花 44 ns。
- 16KB：wlgows 每個 frame 呼叫 4 次 Write，gobwas 只要 2 次。

## 環境

| | |
|---|---|
| 機器 | Apple M5，10 核心，24 GB |
| 作業系統 | macOS 26.6.2 |
| Go | go1.26.6 darwin/arm64 |
| wlgows | v7（本 repo 目前的程式碼） |
| gorilla/websocket | v1.5.3 |
| gobwas/ws | v1.4.0 |

## 方法

案例、各函式庫的呼叫，以及執行方式，都在
[benchmarks/README.zh-TW.md](./benchmarks/README.zh-TW.md)。下面每個數字都是各函式庫
最快的那次執行，取自 `optimal_comparison.json`。差距小於約 5% 視為平手。

## 完整結果

ns/op，越低越好，三者中最快的用粗體標示。

### server 送出

| 案例 | wlgows | gorilla | gobwas | wlgows B / 配置次數 | gorilla B / 配置次數 | gobwas B / 配置次數 | Write 次數（wlgows / gorilla / gobwas） |
|---|---|---|---|---|---|---|---|
| One-16B | 35 | 46 | **34** | 0 / 0 | 0 / 0 | 16 / 1 | 1 / 1 / 1 |
| Many64-16B | **1731** | 2910 | 2067 | 80 / 3 | 0 / 0 | 1024 / 64 | 1 / 64 / 1 |
| One-1KB | **53** | 69 | 59 | 0 / 0 | 0 / 0 | 16 / 1 | 1 / 1 / 1 |
| Many64-1KB | **3144** | 4412 | 3720 | 80 / 3 | 0 / 0 | 1024 / 64 | 16 / 64 / 16 |
| One-16KB | **101** | 177 | 124 | 0 / 0 | 72 / 2 | 16 / 1 | 2 / 2 / 2 |
| Many8-16KB | **811** | 1421 | 977 | 0 / 0 | 576 / 16 | 128 / 8 | 16 / 16 / 16 |
| One-1MB | **102** | 176 | 124 | 0 / 0 | 72 / 2 | 16 / 1 | 2 / 2 / 2 |
| Many8-1MB | **817** | 1419 | 992 | 0 / 0 | 576 / 16 | 128 / 8 | 16 / 16 / 16 |

### server 讀取

| 案例 | wlgows | gorilla | gobwas | wlgows B / 配置次數 | gorilla B / 配置次數 | gobwas B / 配置次數 |
|---|---|---|---|---|---|---|
| One-16B | **130** | 177 | 259 | 64 / 2 | 520 / 2 | 752 / 4 |
| Many64-16B | **7697** | 10866 | 15207 | 4096 / 128 | 33280 / 128 | 48128 / 256 |
| One-1KB | **340** | 562 | 628 | 1072 / 2 | 2184 / 5 | 2416 / 7 |
| Many64-1KB | **21576** | 35817 | 39575 | 68608 / 128 | 139776 / 320 | 154624 / 448 |
| One-16KB | **3085** | 6398 | 6272 | 16432 / 2 | 37768 / 14 | 38000 / 16 |
| Many8-16KB | **25662** | 51399 | 51126 | 131456 / 16 | 302145 / 112 | 304001 / 128 |
| One-1MB | **182565** | 309070 | 290690 | 1048632 / 2 | 2227985 / 25 | 2228222 / 27 |
| Many8-1MB | **1156009** | 1620638 | 1603926 | 8388998 / 16 | 17823846 / 200 | 17825700 / 216 |

### client 送出

| 案例 | wlgows | gorilla | gobwas | wlgows B / 配置次數 | gorilla B / 配置次數 | gobwas B / 配置次數 | Write 次數（wlgows / gorilla / gobwas） |
|---|---|---|---|---|---|---|---|
| One-16B | 120 | **93** | 112 | 0 / 0 | 48 / 1 | 56 / 3 | 1 / 1 / 1 |
| Many64-16B | 7225 | **5893** | 6987 | 80 / 3 | 3072 / 64 | 3584 / 192 | 1 / 64 / 1 |
| One-1KB | 217 | **187** | 204 | 0 / 0 | 48 / 1 | 40 / 2 | 1 / 1 / 1 |
| Many64-1KB | 13565 | **11959** | 13011 | 80 / 3 | 3072 / 64 | 2561 / 128 | 16 / 64 / 16 |
| One-16KB | 1716 | 1596 | **1448** | 0 / 0 | 48 / 1 | 40 / 2 | 4 / 4 / 2 |
| Many8-16KB | 13739 | 12738 | **11598** | 0 / 0 | 384 / 8 | 320 / 16 | 32 / 32 / 16 |
| One-1MB | **106082** | 106460 | 159077 | 0 / 0 | 48 / 1 | 1048620 / 3 | 257 / 256 / 2 |
| Many8-1MB | **850333** | 850413 | 1276972 | 0 / 0 | 384 / 8 | 8388962 / 24 | 2056 / 2048 / 16 |

### client 讀取

| 案例 | wlgows | gorilla | gobwas | wlgows B / 配置次數 | gorilla B / 配置次數 | gobwas B / 配置次數 |
|---|---|---|---|---|---|---|
| One-16B | **123** | 168 | 226 | 64 / 2 | 520 / 2 | 720 / 3 |
| Many64-16B | **7321** | 10193 | 13056 | 4096 / 128 | 33280 / 128 | 46080 / 192 |
| One-1KB | **276** | 478 | 551 | 1072 / 2 | 2184 / 5 | 2384 / 6 |
| Many64-1KB | **17720** | 30360 | 34865 | 68608 / 128 | 139776 / 320 | 152576 / 384 |
| One-16KB | **2128** | 5452 | 5457 | 16432 / 2 | 37768 / 14 | 37968 / 15 |
| Many8-16KB | **17832** | 44801 | 44611 | 131456 / 16 | 302145 / 112 | 303745 / 120 |
| One-1MB | **68848** | 241422 | 233699 | 1048629 / 2 | 2227977 / 25 | 2228179 / 26 |
| Many8-1MB | **495060** | 1177043 | 1182366 | 8388997 / 16 | 17823830 / 200 | 17825458 / 208 |
