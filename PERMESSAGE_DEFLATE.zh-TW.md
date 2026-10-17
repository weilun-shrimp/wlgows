[English](./PERMESSAGE_DEFLATE.md) · **繁體中文**

# permessage-deflate

`PermessageDeflateConfig` 每個欄位的意思，以及一份設定如何寫進
`Sec-WebSocket-Extensions`（RFC 7692）。兩邊各自怎麼使用，見
[CLIENT_README.zh-TW.md](./CLIENT_README.zh-TW.md#壓縮-permessage-deflate) 與
[SERVER_README.zh-TW.md](./SERVER_README.zh-TW.md#壓縮-permessage-deflate)。

## Contents

- [Window 與 context takeover](#window-與-context-takeover)
- [PermessageDeflateConfig](#permessagedeflateconfig)
- [Header 格式](#header-格式) — [語法](#語法) · [寫出一份設定](#寫出一份設定) · [不合法的項目](#不合法的項目)
- [15 bit 以下的 window](#15-bit-以下的-window)

## Window 與 context takeover

permessage-deflate 會用 DEFLATE（RFC 1951）壓縮每個 message。DEFLATE 會把重複的內容
換成往回參照：從多遠之前複製多少 byte。window 是參照最多能往回多遠，為 2^N byte，N 從
8 到 15。沒有限制時是 15 bit，也就是 32 KB。

context takeover 決定參照可以指向哪裡（RFC 7692 7.2.1）：

- **有 context**，也就是預設：目前的 message 加上之前的 message，當成一條連續的資料。
  小而重複的 message 壓縮效果好，但送出與接收的兩方都要在 message 之間保留 window。
- **沒有 context**：只有目前的 message。每個 message 都從空的 window 開始，所以
  message 之間不保留任何東西。

每一方送出的方式各自設定。`client_` 開頭的參數限制 client 送出的內容，`server_` 開頭的
參數限制 server 送出的內容。

## PermessageDeflateConfig

每個欄位都以一個參數命名：

| 欄位 | 參數 | 意思 |
|---|---|---|
| `ClientNoContextTakeover` | `client_no_context_takeover` | client 送出時不使用 context（RFC 7692 7.1.1.2）。 |
| `ServerNoContextTakeover` | `server_no_context_takeover` | server 送出時不使用 context（RFC 7692 7.1.1.1）。 |
| `ClientMaxWindowBitsEnabled` | `client_max_window_bits` | 送出這個參數（RFC 7692 7.1.2.2）。 |
| `ClientMaxWindowBits` | 它的值 | client 的 window 不超過 2^N byte。8 到 15，或 0 表示不帶值。`ClientMaxWindowBitsEnabled` 為 false 時會被忽略。 |
| `ServerMaxWindowBits` | `server_max_window_bits` | server 的 window 不超過 2^N byte。8 到 15，或 0 表示不送出這個參數（RFC 7692 7.1.2.1）。 |

零值就是一般的 `permessage-deflate`：兩邊都保留 context，也都可以使用 15 bit。

`client_max_window_bits` 是唯一可以不帶值的參數，而且只能出現在提議中。在提議中，它
表示 client 支援這個參數，並讓 server 決定值。回應中它必須帶值（RFC 7692 7.1.2.2）。
`server_max_window_bits` 送出時一定帶值。`ServerMaxWindowBits` 為 0 表示不送出這個參數。

## Header 格式

### 語法

`Sec-WebSocket-Extensions` 是以逗號分隔的項目清單（RFC 6455 9.1）。每個項目是一個名稱，
後面接以 `;` 分隔的參數。每個參數是一個名稱，可以接 `=` 和值：

```
Sec-WebSocket-Extensions: permessage-deflate; server_max_window_bits=10; client_max_window_bits, x-other
```

- **值可以加引號**：`server_max_window_bits="10"` 等同於
  `server_max_window_bits=10`。引號內的 `,` 或 `;` 不會切開（RFC 7230 3.2.6）。
- **空白**：名稱、值、`;` 與 `,` 前後的空白都會被忽略。
- **多行**：這個 header 的多行會依序讀成同一份逗號清單（RFC 7230 3.2.2）。
- **空的項目**，例如 `a, , b`，會被略過（RFC 7230 7）。

### 寫出一份設定

`Format()` 會把一份設定寫成一個項目，參數依這個順序：

```
permessage-deflate; server_no_context_takeover; client_no_context_takeover; server_max_window_bits=N; client_max_window_bits[=N]
```

`Format()` 是低階的方法：handshake 會替你呼叫它，除非你清楚自己在做什麼，否則不建議自己
把它的結果送上線路。只是印出來，例如寫進 log 等，則沒有問題：

```go
log.Printf("offering %s", deflateConfig.Format())
```

欄位為 `false` 或 0 的參數不會寫出：

| 設定 | 項目 |
|---|---|
| `{}` | `permessage-deflate` |
| `{ServerNoContextTakeover: true}` | `permessage-deflate; server_no_context_takeover` |
| `{ServerMaxWindowBits: 10}` | `permessage-deflate; server_max_window_bits=10` |
| `{ClientMaxWindowBitsEnabled: true}` | `permessage-deflate; client_max_window_bits` |
| `{ClientMaxWindowBitsEnabled: true, ClientMaxWindowBits: 12}` | `permessage-deflate; client_max_window_bits=12` |
| `{ClientMaxWindowBits: 12}` | `permessage-deflate` |

多份設定會依序寫成同一行的多個項目：

```
permessage-deflate; client_no_context_takeover, permessage-deflate
```

### 不合法的項目

項目有下列任何一種情況，就違反 RFC 7692 7。server 會拒絕這樣的提議，client 收到這樣的
回應會中斷連線：

- 上述四個以外的參數；
- 同一個參數出現兩次；
- `server_no_context_takeover` 或 `client_no_context_takeover` 帶了值；
- `server_max_window_bits` 沒有帶值；
- window bit 不是沒有前導零的十進位 8 到 15，例如 `7`、`16` 或 `010`
  （RFC 7692 7.1.2.1）。

## 15 bit 以下的 window

wlgows 用 Go 的 `compress/flate` 壓縮，而它無法用 15 bit 以下的 window 送出。所以
wlgows 永遠不會同意用 8 到 14 bit 送出（函式庫的選擇）。每一方因此拒絕哪些設定，見各自
的 README。

接收沒有這個限制。Go 的解壓器可以讀取 15 bit 以內的任何 window，所以對方用任何 window
送出，wlgows 都能接受。
