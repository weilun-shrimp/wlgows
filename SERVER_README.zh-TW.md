[English](./SERVER_README.md) · **繁體中文**

# Server

server 如何從一條 TCP 連線，變成一個可以使用的 `*wlgows.Conn`：接受連線、
handshake、從 `net/http` 或 Gin hijack、TLS、subprotocol，以及壓縮。

## Contents

- [接受連線](#接受連線) — 原始 TCP，逾時與大小上限由你處理
- [Handshake](#handshake) — [設定](#設定) · [回傳值](#回傳值) · [拒絕](#拒絕)
- [HTTP Hijacking](#http-hijacking) — `net/http` 與 Gin
- [TLS (wss://)](#tls-wss)
- [Subprotocols](#subprotocols) — [選一個](#選一個) · [一定要有](#一定要有) · [實際送出的值](#實際送出的值)
- [壓縮 (permessage-deflate)](#壓縮-permessage-deflate) — [選哪個回應](#選哪個回應) · [用自己的規則選](#用自己的規則選) · [檢查設定](#檢查設定)
- [檢查 Origin](#檢查-origin) — 給瀏覽器會連上的 server
- [自己跑 handshake](#自己跑-handshake) — 不建議
- [下一步：讀取與傳送](#下一步讀取與傳送)

讀取見 [LISTENER_README.zh-TW.md](./LISTENER_README.zh-TW.md)，傳送見
[SENDING_README.zh-TW.md](./SENDING_README.zh-TW.md)，client 端見
[CLIENT_README.zh-TW.md](./CLIENT_README.zh-TW.md)。

## 接受連線

**我們建議改用 [net/http 或 Gin](#http-hijacking)。** 用它們的話，request 的逾時與
大小上限各只要一個設定。`Run` 與 `Accept` 把這兩件事都交給你。少了它們，client
可以永遠占住一條連線，或一直送 header 直到記憶體耗盡。你可以自己加上，用 read
deadline 與有上限的 reader，但需要更小心，所以我們不建議。但 `Run` 很快。如果你
清楚自己在做什麼，例如來自可信任來源的內部連線，可以使用它。

`Run` 負責 listen。`Accept` 回傳下一條 TCP 連線，其他什麼都不做。讀取 request
與執行 handshake 由你自己來：

```go
server, err := wlgows.Run(":8001") // ":8001" 或 "[::]:8001" 會同時 listen IPv4 與 IPv6
if err != nil {
	panic(err)
}
defer server.Close()

for {
	netConn, err := server.Accept()
	if err != nil {
		continue
	}
	go handle(netConn)
}
```

```go
func handle(netConn net.Conn) {
	defer netConn.Close()

	reader := bufio.NewReader(netConn)
	request, err := http.ReadRequest(reader)
	if err != nil {
		respWriter := wlgows.NewResponseWriter()
		respWriter.DeclineByError(wlgows.ErrHttpMsgFormationInvalid) // 400：格式錯誤的 request（RFC 6455 4.2.1）
		wlgows.SendHandShakeResponse(netConn, respWriter.GenerateResponse())
		return
	}
	writer := bufio.NewWriter(netConn)
	conn, _, err := wlgows.ServerHandShake(netConn, reader, writer, request, wlgows.ServerHandShakeConfig{})
	if err != nil {
		return
	}
	// 用 Listener 讀取，用 conn.Send* 傳送
}
```

`Server` 的 `TCPAddr` 與 `TCPListener` 是匯出的欄位，可以讀取也可以設定。

## Handshake

`ServerHandShake(netConn, reader, writer, request, config)` 會依 RFC 6455 4.2.1 檢查
request 的 method、HTTP 版本、`Host`、`Upgrade`、`Connection`、`Sec-WebSocket-Key` 與
`Sec-WebSocket-Version`，寫出回應，並建立 `Conn`。`Origin` 與其他欄位不檢查。

| 參數 | 必須是 |
|---|---|
| `netConn` | 這條連線。回應會直接寫進它。 |
| `reader` | 你讀取 request 時用的同一個 `*bufio.Reader`。它可能已經讀進第一個 frame 的開頭，換一個新的就會遺失這些 byte。 |
| `writer` | 每個 frame 都經由這個 `*bufio.Writer` 送出。大小由你決定：見 [記憶體與 write buffer](./SENDING_README.zh-TW.md#記憶體與-write-buffer)。 |
| `request` | 已經解析好的 request。 |
| `config` | server 要回應的內容。一般的 handshake 傳 `wlgows.ServerHandShakeConfig{}`。見 [設定](#設定)。 |

回傳的 `Conn` 不做 mask：server 送出的 frame 一律不 mask（5.1）。

### 設定

`ServerHandShakeConfig` 放的是 server 要回應的內容，前提是 client 有提出：

| 欄位 | 回應 | 見 |
|---|---|---|
| `Subprotocol` | `Sec-WebSocket-Protocol`，一個值 | [Subprotocols](#subprotocols) |
| `PermessageDeflates` | `Sec-WebSocket-Extensions`，你的回應中第一個被提議允許的 | [壓縮](#壓縮-permessage-deflate) |

```go
config := wlgows.ServerHandShakeConfig{
	Subprotocol:        "chat.v2",
	PermessageDeflates: []wlgows.PermessageDeflateConfig{{}},
}
conn, resp, err := wlgows.ServerHandShake(netConn, reader, writer, request, config)
```

兩者都不會拒絕沒有提出的 client：連線只是在沒有它的情況下建立。你可以在任何
handshake 之前檢查 `PermessageDeflates`，見 [壓縮](#壓縮-permessage-deflate)。

### 回傳值

| 結果 | `conn` | `resp` | `err` |
|---|---|---|---|
| 升級成功 | 那個 `Conn` | 送出的 101 | `nil` |
| request 違反規則 | `nil` | 送出的拒絕回應 | 原因 |
| 回應寫不出去 | `nil` | 那個回應 | 寫入的 error |
| 升級成功，但 writer 無法 flush | `nil` | 送出的 101 | 寫入的 error |
| `request` 是 `nil` | `nil` | `nil` | `ErrHandshakeRequestNil` |
| `config.PermessageDeflates` 中有設定被拒絕 —— 見 [壓縮](#檢查設定) | `nil` | `nil` | `ErrHandshakeServerDeflateConfigInvalid` 或 `ErrHandshakeServerDeflateConfigUnsupported` |

只要 `err` 不是 `nil`，`conn` 就是 `nil`。`resp` 是實際送上線路的內容，可以拿來記錄。它的 body 已經被讀完。`resp` 是 `nil` 表示什麼都沒送出。

`ServerHandShake` 不會關閉連線。**除非你有更好的理由，否則我們建議在每一種 handshake error 之後都關閉它**：
這條連線不可能再變成 WebSocket。接受連線或 hijack 之後馬上寫
`defer netConn.Close()`，就能涵蓋所有情況。

### 拒絕

違反上述規則的 request 會自動得到回應，狀態碼對應所違反的規則：

| Error | 狀態碼 |
|---|---|
| `ErrHttpMethodNotAllowed` | 405 Method Not Allowed |
| `ErrHttpProtocolOrVersionNotAllowed` | 505 HTTP Version Not Supported |
| `ErrHttpHostHeaderInvalid` | 400 Bad Request（RFC 6455 4.2.1、RFC 7230 5.4） |
| `ErrHttpSecWebSocketKeyHeaderNotSet`、`ErrHttpSecWebSocketKeyInvalid` | 400 Bad Request |
| `ErrHttpSecWebSocketKeyDuplicated`、`ErrHttpSecWebSocketVersionDuplicated` | 400 Bad Request（RFC 6455 11.3.1、11.3.5） |
| `ErrHttpConnectionHeaderNotUpgrade`、`ErrHttpUpgradeHeaderNotWebsocket` | 400 Bad Request |
| `ErrHttpSecWebSocketVersionNotSupported` | 426 Upgrade Required，並帶上 `Sec-WebSocket-Version: 13`（4.2.2、4.4） |

每個 error 都包著對應的 sentinel，所以請用 `errors.Is` 比較：

```go
if _, _, err := wlgows.ServerHandShake(netConn, reader, writer, request, wlgows.ServerHandShakeConfig{}); errors.Is(err, wlgows.ErrHttpMethodNotAllowed) {
	// ...
}
```

## HTTP Hijacking

在 `net/http` 之下，request 已經解析好了。用 `http.Hijacker` 取得連線：

```go
func handler(w http.ResponseWriter, r *http.Request) {
	hijacker, ok := w.(http.Hijacker) // Gin 之下，c.Writer 也是
	if !ok {
		http.Error(w, "could not hijack connection", http.StatusInternalServerError)
		return
	}
	netConn, bufRW, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, "could not hijack connection", http.StatusInternalServerError)
		return
	}
	defer netConn.Close()

	conn, _, err := wlgows.ServerHandShake(netConn, bufRW.Reader, bufRW.Writer, r, wlgows.ServerHandShakeConfig{})
	if err != nil {
		return
	}
	// 讀取與傳送
}
```

- **要傳 `bufRW.Reader`。** net/http 可能已經把第一個 frame 的開頭讀進去了。
- **`bufRW.Writer` 沿用 net/http 的 writer。** `bufio.NewWriterSize(netConn, size)`
  也可以。hijack 之後如何釋放記憶體，見
  [最低記憶體的設定方式](./SENDING_README.zh-TW.md#最低記憶體的設定方式)。
- **要拒絕就在 `Hijack` 之前**，這時 net/http 還能替你回應。`Hijack` 之後連線就是你的，
  寫出的每個 byte 也都由你負責。

net/http 預設不設逾時，所以請設定 `ReadHeaderTimeout` 與 `IdleTimeout`。
`ReadHeaderTimeout` 要等 request 的第一個 byte 到了才開始計時。少了 `IdleTimeout`，
client 可以先送一個普通的 request，然後讓連線一直開著、不再送任何東西。這兩個設定
都不會影響已經 hijack 的連線。`MaxHeaderBytes` 預設是 1 MB。Gin 的 `Run` 也是用預設的 server，所以請用你自己的 `http.Server` 來提供 Gin
engine：

```go
server := &http.Server{
	Addr:              ":8001",
	Handler:           mux, // 或 Gin engine
	ReadHeaderTimeout: 10 * time.Second, // request 開始後，等待它的 header
	IdleTimeout:       60 * time.Second, // 保持連線時，等待下一個 request
	MaxHeaderBytes:    16 * 1024,
}
log.Fatal(server.ListenAndServe())
```

Gin 之下做法相同，改用 `c.Writer.Hijack()` 與 `c.Request`。
[`hijack_http`](./example/hijack_http/main.go) 與
[`hijack_gin`](./example/hijack_gin/main.go) 範例兩種都有。

## TLS (wss://)

有兩種做法：

- **在 net/http 之下。** 用 `http.ListenAndServeTLS` 提供服務，再像上面那樣 hijack。
  拿到的連線已經是 TLS。hijack 範例就是這麼做的。
- **在原始 TCP 上。** `Run` 是一般的 TCP，所以要把每條接受的連線包起來，之後都用包好
  的那一條：

```go
for {
	netConn, err := server.Accept()
	if err != nil {
		continue
	}
	tlsConn := tls.Server(netConn, tlsConfig) // TLS handshake 會在第一次讀取時進行
	go handle(tlsConn)
}
```

## Subprotocols

subprotocol 是你的 message 所遵循的應用層協定，例如 `graphql-transport-ws`。client
在 `Sec-WebSocket-Protocol` 中提出一份清單，server 從中回傳一個，或一個都不回
（RFC 6455 4.2.2）。

`RequestSubprotocols(request)` 會依 client 的順序讀出這份清單。它會讀取每一行
header，並以逗號切開，所以一行的 `"a, b"` 與分成兩行的 `"a"`、`"b"` 會得到相同的
清單。

### 選一個

把你選的那個放進 config：

```go
conn, resp, err := wlgows.ServerHandShake(netConn, reader, writer, request, wlgows.ServerHandShakeConfig{
	Subprotocol: "chat.v2",
})
```

這是單一欄位，因為 server 只能回傳一個 subprotocol。只有在 client 有提出它時才會
送出。否則不送這個 header，連線照樣建立，只是沒有 subprotocol。所以上面這一行的
意思已經是「client 支援 chat.v2 就用它，否則就不用 subprotocol」。

要依你自己的順序從多個中選一個：

```go
subprotocol := ""
for _, supported := range []string{"chat.v2", "chat.v1"} { // 最想用的放最前面
	if slices.Contains(wlgows.RequestSubprotocols(request), supported) {
		subprotocol = supported
		break
	}
}
config := wlgows.ServerHandShakeConfig{Subprotocol: subprotocol}
```

### 一定要有

**什麼時候。** 只有在你的 server 沒有那個 subprotocol 就無法和 client 溝通時才拒絕，
例如它送出與接收的每一則 message 都是 chat.v2 的格式。如果它也能服務一般的 client，
就不要拒絕：選一個，讓連線在沒有 subprotocol 的情況下建立。

**怎麼做。** 在 `ServerHandShake` 之前檢查，並用一般的 HTTP 回應拒絕。RFC 6455 沒有
為此指定狀態碼，常見的做法是 400 Bad Request 加上簡短的原因。不要用「handshake 之後
關閉連線」的方式拒絕：那時 client 以為已經連上了，也拿不到任何原因。

在 net/http 之下，於 `Hijack` 之前拒絕：

```go
if !slices.Contains(wlgows.RequestSubprotocols(r), "chat.v2") {
	http.Error(w, "subprotocol chat.v2 required", http.StatusBadRequest)
	return
}
```

Gin 之下用 `c.String(http.StatusBadRequest, "subprotocol chat.v2 required")`。

在原始 TCP 上沒有人會替你回應，所以用這個套件自己的 `ResponseWriter` 寫出拒絕回應：

```go
if !slices.Contains(wlgows.RequestSubprotocols(request), "chat.v2") {
	respWriter := wlgows.NewResponseWriter()
	respWriter.WriteHeader(http.StatusBadRequest)
	respWriter.Write([]byte("subprotocol chat.v2 required"))
	wlgows.SendHandShakeResponse(netConn, respWriter.GenerateResponse())
	netConn.Close()
	return
}
```

檢查通過後，像上面那樣設定 `Subprotocol: "chat.v2"`。client 會看到 `400` 與原因。
每個 server 範例都有這段程式，以註解的形式保留。

### 實際送出的值

`resp.Header.Get("Sec-WebSocket-Protocol")` 就是實際送出的內容：選定的
subprotocol，沒有協議出任何一個時則是 `""`。

## 壓縮 (permessage-deflate)

在 `config.PermessageDeflates` 列出 server 想要的回應，最想用的放最前面。空的清單表示不
壓縮。每個欄位的意思見
[PERMESSAGE_DEFLATE.zh-TW.md](./PERMESSAGE_DEFLATE.zh-TW.md#permessagedeflateconfig)。

```go
config := wlgows.ServerHandShakeConfig{
	PermessageDeflates: []wlgows.PermessageDeflateConfig{
		{ClientMaxWindowBitsEnabled: true, ClientMaxWindowBits: 10}, // 第一選擇
		{ClientNoContextTakeover: true},                             // 退而求其次
	},
}
```

**你可以在任何 handshake 之前檢查這些設定。** `ValidateServerDeflateConfig` 不需要
client、連線或 handshake，就能告訴你設定是否正確，所以你不必等到 handshake 失敗才知道。
在測試中呼叫它，或在第一次 handshake 之前呼叫一次。`ServerHandShake` 每次 handshake
都已經會對每份設定執行它，所以正式的程式不必再呼叫一次：

```go
func TestDeflateConfigs(t *testing.T) {
	for _, serverConfig := range config.PermessageDeflates {
		if err := wlgows.ValidateServerDeflateConfig(serverConfig); err != nil {
			t.Fatal(err)
		}
	}
}
```

沒有提早檢查，錯誤的設定要等到第一個 client 連上時才會出現，而且每條連線都會以同樣的方式
失敗。哪些設定會被拒絕，見 [檢查設定](#檢查設定)。

server 如何解讀每個欄位：

| 欄位 | 意思 |
|---|---|
| `ServerNoContextTakeover` 為 true | server 每送完一個 message 就重設自己的 window。提議要求時也會設上（RFC 7692 7.1.1.1）。 |
| `ClientNoContextTakeover` 為 true | server 要求 client 每送完一個 message 就重設 window（RFC 7692 7.1.1.2）。這樣 server 在 message 之間不需要保留解壓的狀態。提議中有它時也會設上。 |
| `ServerMaxWindowBits` | 0 或 15。8 到 14 會被拒絕，見 [檢查設定](#檢查設定)。 |
| `ClientMaxWindowBitsEnabled` 為 false | server 接受 client 使用任何 window，最多 15 bit。`ClientMaxWindowBits` 會被忽略。 |
| `ClientMaxWindowBitsEnabled` 為 true | client 的 window 不超過 `ClientMaxWindowBits`，它必須是 8 到 15。 |

### 選哪個回應

你的順序優先（函式庫的選擇：RFC 7692 5 允許 server 接受任何一個提議）。對清單中的每份設定，server 依 client 的順序逐一嘗試提議，回應第一個允許
這份設定的提議（RFC 7692 5）。第一份有提議允許的設定勝出。回應的內容是那份設定，再加上
那個提議要求的部分：

- 提議中有 `server_no_context_takeover` 就加上（RFC 7692 7.1.1.1）；
- 提議中有 `client_no_context_takeover` 就送回去，讓 client 的提示成為雙方的約定
  （RFC 7692 7.1.1.2）；
- 兩個 `max_window_bits` 都會降到提議的值（RFC 7692 7.1.2）。

下列情況，提議不允許這份設定：

- 帶有[不合法項目](./PERMESSAGE_DEFLATE.zh-TW.md#不合法的項目)的提議。server 會拒絕
  它，而不是讓 handshake 失敗（RFC 7692 7）；
- 要求 server 使用 15 bit 以下 window 的提議，wlgows 無法用那麼小的 window 送出（函式庫
  的選擇，見 [15 bit 以下的 window](./PERMESSAGE_DEFLATE.zh-TW.md#15-bit-以下的-window)）；
- 設定把 client 限制在 15 bit 以下，而提議沒有 `client_max_window_bits`。server 只有在
  提議包含這個參數時才能送出它（RFC 7692 7.1.2.2）。wlgows 的 client 從不提出它，這就是
  上面的範例有退而求其次那一項的原因。

沒有任何設定符合時，回應沒有 `Sec-WebSocket-Extensions`，連線會在不壓縮的情況下建立。
`resp.Header.Get("Sec-WebSocket-Extensions")` 就是送出的內容。

### 用自己的規則選

`AcceptDeflateOffer(serverConfig, request)` 就是清單所做的檢查：回傳那份設定的回應，沒有
提議允許時回傳 `nil`。你的規則需要看回應會是什麼時，在 handshake 之前用它。例如負載高時，
只替已經不使用 context 的 client 壓縮，這樣 server 不必替它們保留解壓的 window：

```go
config := wlgows.ServerHandShakeConfig{}
answer := wlgows.AcceptDeflateOffer(wlgows.PermessageDeflateConfig{}, request)
if answer != nil && (openConnections <= limit || answer.ClientNoContextTakeover) {
	config.PermessageDeflates = []wlgows.PermessageDeflateConfig{{}}
}
```

### 檢查設定

`ValidateServerDeflateConfig` 會拒絕下列設定。`ServerHandShake` 會先用它檢查清單中的
每份設定，被拒絕時會在送出任何東西之前回傳，並讓連線保持開啟：

| 設定 | Error |
|---|---|
| `ServerMaxWindowBits` 不是 0 或 8 到 15，或 `ClientMaxWindowBitsEnabled` 時 `ClientMaxWindowBits` 不是 8 到 15（RFC 7692 7.1.2） | `ErrHandshakeServerDeflateConfigInvalid` |
| `ServerMaxWindowBits` 為 8 到 14（函式庫的選擇） | `ErrHandshakeServerDeflateConfigUnsupported` |

要在不經過 handshake 的情況下抓到這些，請在測試中或第一次 handshake 之前呼叫
`ValidateServerDeflateConfig`，作法見 [壓縮](#壓縮-permessage-deflate)。

## 檢查 Origin

瀏覽器一定會送出 `Origin`。如果你的 server 只服務特定網站，請在 `ServerHandShake`
之前檢查，並以 403 Forbidden 拒絕（RFC 6455 10.2、4.2.2），作法與必要的
subprotocol 相同。不是瀏覽器的 client 可能送任何 `Origin`，或根本不送。

## 自己跑 handshake

**不建議這麼做。只有在你清楚自己在做什麼時才使用。**
`ServerHandShake` 會照這個順序執行下列步驟。自己執行時，漏掉或調換任何一步都會破壞
協定，而且不會有任何警告。`config.PermessageDeflates` 中的每份設定都必須先通過
`ValidateServerDeflateConfig`，見 [壓縮](#壓縮-permessage-deflate)。

```go
// 1. 依 RFC 6455 4.2.1 檢查 request。
invalid := wlgows.ValidateHandShakeRequest(request)

// 2. 建立回應：指出違反規則的拒絕回應，或 101。
respWriter := wlgows.NewResponseWriter()
if invalid != nil {
	respWriter.DeclineByError(invalid)
} else {
	respWriter.UpgradeForWebsocket(request.Header.Get("Sec-WebSocket-Key"))
}
resp := respWriter.GenerateResponse()

// 3. 升級時，只有 client 提出過的 subprotocol 才加上（RFC 6455 4.2.2）。
if invalid == nil && slices.Contains(wlgows.RequestSubprotocols(request), "chat.v2") {
	resp.Header.Set("Sec-WebSocket-Protocol", "chat.v2")
}

// 4. 升級時，回應第一份有提議允許的設定（函式庫的選擇：你的順序優先）。
if invalid == nil {
	for _, serverConfig := range config.PermessageDeflates {
		if answer := wlgows.AcceptDeflateOffer(serverConfig, request); answer != nil {
			resp.Header.Set("Sec-WebSocket-Extensions", answer.Format())
			break
		}
	}
}

// 5. 送到 netConn。writer 是給 frame 用的。
if err := wlgows.SendHandShakeResponse(netConn, resp); err != nil {
	return err
}
if invalid != nil {
	return invalid
}

// 6. 建立 Conn。false：server 送出的 frame 一律不 mask（RFC 6455 5.1）。
conn, err := wlgows.NewConn(netConn, reader, writer, false)
```

- **`NewConn` 什麼都不檢查。** handshake 違反規則的連線，或 masking 旗標設錯，從第一個
  frame 起就會破壞協定。
- **它的 error** 只會來自小於 14 byte 的 writer：它會先被 flush 再被替換，而這次
  flush 可能失敗。
- **直接寫 struct literal 會 panic。** `Conn` 帶有未匯出的相依，所以只能用 `NewConn`
  建立。

## 下一步：讀取與傳送

拿到 `Conn` 之後，剩下的就是讀取與傳送：

```go
listener := conn.NewStandardListener() // ping、pong、close 與保留的 opcode 都已替你處理

config := listener.GetConfig()
config.Text = func(frames wlgows.DataFrames) {
	conn.SendText(frames.Bytes(), 0) // echo
}
listener.SetConfig(config)

go conn.StartPingLoop(30*time.Second, nil) // 心跳；會自動停止

if err := listener.Listen(); err != nil {
	if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
		conn.SendClose(payload)
	}
}
```

- **讀取：**[LISTENER_README.zh-TW.md](./LISTENER_README.zh-TW.md) —— hook、上限、
  每種 error 的意義、[偵測已斷線的對方](./LISTENER_README.zh-TW.md#它沒有-ping-pong-存活偵測)，
  以及一次讀一個 frame。
- **傳送：**[SENDING_README.zh-TW.md](./SENDING_README.zh-TW.md) ——
  [該用哪個呼叫](./SENDING_README.zh-TW.md#選擇要用哪個呼叫)、串流傳送大的 message、
  [定期送出 ping](./SENDING_README.zh-TW.md#定期送出-ping)、write buffer，以及 lock。
