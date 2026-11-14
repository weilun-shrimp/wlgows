[English](./PERMESSAGE_DEFLATE.md) · **繁體中文**

# permessage-deflate

`PermessageDeflateConfig` 每個欄位的意思，以及一份設定如何寫進
`Sec-WebSocket-Extensions`（RFC 7692）。兩邊各自怎麼使用，見
[CLIENT.zh-TW.md](./CLIENT.zh-TW.md#壓縮-permessage-deflate) 與
[SERVER.zh-TW.md](./SERVER.zh-TW.md#壓縮-permessage-deflate)。送出壓縮的 message 見
[SENDING.zh-TW.md](./SENDING.zh-TW.md#壓縮-permessage-deflate)，讀取見
[READING.zh-TW.md](./READING.zh-TW.md#壓縮的-message)。

## Contents

- [Window 與 context takeover](#window-與-context-takeover)
- [PermessageDeflateConfig](#permessagedeflateconfig)
- [Header 格式](#header-格式) — [不合法的項目](#不合法的項目)
- [15 bit 以下的 window](#15-bit-以下的-window)
- [記憶體](#記憶體) — [每個連線](#每個連線) · [大量連線](#大量連線)
- [大型資料：自己壓縮](#大型資料自己壓縮)

## Window 與 context takeover

window 是壓縮後的 message 最多能往回指多遠，用來重複前面出現過的 byte：2^N byte，N 從
8 到 15。沒有限制時是 15 bit，也就是 32 KB。

context takeover 決定 message 可以往回指到哪裡（RFC 7692 7.2.1）：

- **有 context**，也就是預設：目前的 message 加上之前的 message，當成一條連續的資料。
  小而重複的 message 壓縮效果好，但送出端和接收端都要在 message 之間保留 window。
- **沒有 context**：只有目前的 message。每個 message 都從空的 window 開始，所以
  message 之間不保留任何東西。

兩邊的送出方式各自設定。`client_` 開頭的參數限制 client 怎麼送，`server_` 開頭的參數限制
server 怎麼送。

## PermessageDeflateConfig

**建議用 `PermessageDeflateConfig` 來設定 header。** handshake 會依你的設定寫出
`Sec-WebSocket-Extensions` 並檢查，所以會符合 RFC 7692。自己寫的 header 可能會違反這些
規則。

每個欄位都以對應的參數命名：

| 欄位 | 參數 | 意思 |
|---|---|---|
| `ClientNoContextTakeover` | `client_no_context_takeover` | client 送出時不使用 context（RFC 7692 7.1.1.2）。 |
| `ServerNoContextTakeover` | `server_no_context_takeover` | server 送出時不使用 context（RFC 7692 7.1.1.1）。 |
| `ClientMaxWindowBitsEnabled` | `client_max_window_bits` | 送出這個參數（RFC 7692 7.1.2.2）。 |
| `ClientMaxWindowBits` | 它的值 | client 的 window 不超過 2^N byte。8 到 15，或 0 表示不帶值。`ClientMaxWindowBitsEnabled` 為 false 時不使用這個值。 |
| `ServerMaxWindowBits` | `server_max_window_bits` | server 的 window 不超過 2^N byte。8 到 15，或 0 表示不送出這個參數（RFC 7692 7.1.2.1）。 |

零值就是一般的 `permessage-deflate`：兩邊都保留 context，也都可以使用 15 bit。

**我們建議兩邊送出時都不使用 context，任何情況都一樣。** 有 context 時，每個連線都要留著
自己的 writer，約 1 MB。沒有 context 時，連線共用 writer，閒置的連線一個都不佔。見
[記憶體](#記憶體)。不過決定權還是在你：只要你清楚自己在做什麼，怎麼設都可以。

```go
wlgows.PermessageDeflateConfig{
	ServerNoContextTakeover: true, // server 送出時不使用 context
	ClientNoContextTakeover: true, // client 送出時不使用 context
}
```

兩邊用同一份設定：放進 client 的提議，也放進 server 的清單。

`client_max_window_bits` 是唯一可以不帶值的參數，而且只能出現在提議中。放在提議中時，
表示 client 支援這個參數，並讓 server 決定值。在回應中則一定要帶值（RFC 7692 7.1.2.2）。
`server_max_window_bits` 送出時一定帶值。`ServerMaxWindowBits` 為 0 表示不送出這個參數。

## Header 格式

`Format()` 會產生一份設定的 header 文字，可以用在 log。但不建議拿它來自己設定 header：

```go
log.Printf("offering %s", deflateConfig.Format())
```

### 不合法的項目

項目有下列任何一種情況，就違反 RFC 7692 7。server 會拒絕這樣的提議，client 收到這樣的
回應會中斷連線：

- 上述四個以外的參數；
- 同一個參數出現兩次；
- `server_no_context_takeover` 或 `client_no_context_takeover` 帶了值；
- `server_max_window_bits` 沒有帶值；
- window bits 的值不是沒有前導零的十進位 8 到 15，例如 `7`、`16` 或 `010`
  （RFC 7692 7.1.2.1）。

`PermessageDeflateConfig` 和 handshake 絕不會寫出這樣的項目。這就是我們建議用它們來設定
header 的原因，見 [PermessageDeflateConfig](#permessagedeflateconfig)。

## 15 bit 以下的 window

wlgows 用 Go 的 `compress/flate` 壓縮，而它無法用 15 bit 以下的 window 送出。所以
wlgows 永遠不會同意用 8 到 14 bit 送出（函式庫的選擇）。兩邊因此會拒絕哪些設定，見
[CLIENT.zh-TW.md](./CLIENT.zh-TW.md#壓縮-permessage-deflate) 與
[SERVER.zh-TW.md](./SERVER.zh-TW.md#壓縮-permessage-deflate)。

接收沒有這個限制。Go 的解壓器可以讀取 15 bit 以內的任何 window，所以對方用任何 window
送出，wlgows 都能接受。

## 記憶體

壓縮會用到記憶體。下面的大小來自 Go 的 `compress/flate`，用 Go 1.26 量測。不同 Go 版本
會有些微差異。

| 項目 | 大小 |
|---|---|
| 一個 writer，送出用 | `BestSpeed` 約 1170 KB，壓縮等級 2 到 9 約 790 KB，`HuffmanOnly` 或 `NoCompression` 約 715 KB |
| 一個 reader，接收用 | 約 40 KB |
| 歷史資料，有 context 時 | 最多約 64 KB |

歷史資料是最近送出或收到的 byte，另外複製一份留著。有 context 時，message 可以往回參照到它
（RFC 7692 7.2.1）。

### 每個連線

一個連線送出並收到一則壓縮的 message 之後，用 `BestSpeed` 時會留著：

| 這一方送出時 | 對方送出時 | 一個連線留著 |
|---|---|---|
| 有 context | 有 context | 約 1340 KB：writer 1170 + 歷史資料 64 + reader 40 + 歷史資料 64 |
| 有 context | 沒有 context | 約 1234 KB：writer 1170 + 歷史資料 64。使用 Data hook 時，再加 reader 的 40 KB。 |
| 沒有 context | 有 context | 約 104 KB：reader 40 + 歷史資料 64 |
| 沒有 context | 沒有 context | 0。使用 Data hook 時，再加 reader 的 40 KB。 |

有 context 時，一個連線要留著超過 1 MB。沒有 context 時，幾乎什麼都不用留。所以我們建議
兩邊都不使用 context takeover，見 [PermessageDeflateConfig](#permessagedeflateconfig)。

- 用其他壓縮等級時，writer 改用上表中該等級的大小。
- 沒有算進去的：
  - result buffer，它會長到你最大的那則壓縮 message 的大小，見
    [SENDING.zh-TW.md](./SENDING.zh-TW.md#記憶體result-buffer)；
  - 使用 Data hook 時，已經 put 進去、但還沒解壓縮的 frame：最多到
    `MaxDataFramesSize`，見 [READING.zh-TW.md](./READING.zh-TW.md#有壓縮)；
  - 送出或讀取 message 時，從 pool 暫時拿來的 writer 或 reader。
- 串流進行中時，送出時沒有 context 的連線也會留著一個 writer 和歷史資料，就像「有
  context」那幾列。

**用 Data hook 讀取大型 message。** 讀取一則壓縮過的 message 時，不管對方有沒有保留
context，一條連線都會佔用約 104 KB：reader 40 + 歷史資料 64。另外還要加上還在排隊的
frame，見 [READING.zh-TW.md](./READING.zh-TW.md#有壓縮)。讀完之後，如果對方沒有
context，就只會留下 reader 的 40 KB。

### 大量連線

有 context 時，每個連線只要送出過一則壓縮的 message，就會留著一個 writer。10 000 個
連線用 `BestSpeed`，約佔 12 GB。

沒有 context 時，writer 是共用的，只有同一時間正在送出的連線才會佔用。

- server 的設定有 `ServerNoContextTakeover`，或 client 的提議要求時，server 送出時就
  不使用 context（RFC 7692 7.1.1.1）。
- wlgows client 的提議有 `ClientNoContextTakeover`，或 server 的回應有它時，client
  送出時就不使用 context（RFC 7692 7.1.1.2）。

每個參數只限制名稱裡那一方的送出。沒有 context 的代價：小而重複的 message 壓縮效果較差。

## 大型資料：自己壓縮

**大型資料不建議用 permessage-deflate。** 請在送出前自己壓縮，送出時不要再 deflate，
由接收方自己解壓縮。

原因：

- 有 deflate 的串流一律使用連線自己的 writer，約 1 MB，就算沒有 context 也一樣。見
  [每個連線](#每個連線)。
- permessage-deflate 每個連線都要重新壓縮一次。自己壓縮的資料只要壓一次，同一份 byte
  可以送給每個連線。
- 格式由你決定，例如 gzip 或 zstd。接收方可以直接存下來，不用解壓縮。

兩邊要約定好格式。這由你的 app 決定，不是 WebSocket 協定的一部分。
