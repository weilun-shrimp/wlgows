[English](./SERVER_README.md) · **繁體中文**

# Server

server 如何從一條 TCP 連線，變成一個可以使用的 `*wlgows.Conn`：接受連線、
handshake、從 `net/http` 或 Gin hijack、TLS，以及 subprotocol。

## Contents

- [接受連線](#接受連線)
- [Handshake](#handshake) — [回傳值](#回傳值) · [拒絕](#拒絕)
- [HTTP Hijacking](#http-hijacking) — `net/http` 與 Gin
- [TLS (wss://)](#tls-wss)
- [Subprotocols](#subprotocols) — [選一個](#選一個) · [一定要有](#一定要有) · [實際送出的值](#實際送出的值)
- [檢查 Origin](#檢查-origin) — 給瀏覽器會連上的 server
- [自己建立 Conn](#自己建立-conn) — 不建議
- [下一步：讀取與傳送](#下一步讀取與傳送)

讀取見 [LISTENER_README.zh-TW.md](./LISTENER_README.zh-TW.md)，傳送見
[SENDING_README.zh-TW.md](./SENDING_README.zh-TW.md)，client 端見
[CLIENT_README.zh-TW.md](./CLIENT_README.zh-TW.md)。

## 接受連線

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
	conn, _, err := wlgows.ServerHandShake(netConn, reader, bufio.NewWriter(netConn), request, wlgows.ServerHandShakeConfig{})
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
| `config` | 一般的 handshake 傳 `wlgows.ServerHandShakeConfig{}`。見 [Subprotocols](#subprotocols)。 |

回傳的 `Conn` 不做 mask：server 送出的 frame 一律不 mask（5.1）。

### 回傳值

| 結果 | `conn` | `resp` | `err` |
|---|---|---|---|
| 升級成功 | 那個 `Conn` | 送出的 101 | `nil` |
| request 違反規則 | `nil` | 送出的拒絕回應 | 原因 |
| 回應寫不出去 | `nil` | 那個回應 | 寫入的 error |
| 升級成功，但 writer 無法 flush | `nil` | 送出的 101 | 寫入的 error |
| `request` 是 `nil` | `nil` | `nil` | `ErrHandshakeRequestNil` |

只要 `err` 不是 `nil`，`conn` 就是 `nil`。`resp` 是實際送上線路的內容，可以拿來記錄。它的 body 已經被讀完。

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
if _, _, err := wlgows.ServerHandShake(netConn, reader, bufio.NewWriter(netConn), request, wlgows.ServerHandShakeConfig{}); errors.Is(err, wlgows.ErrHttpMethodNotAllowed) {
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

## 檢查 Origin

瀏覽器一定會送出 `Origin`。如果你的 server 只服務特定網站，請在 `ServerHandShake`
之前檢查，並以 403 Forbidden 拒絕（RFC 6455 10.2、4.2.2），作法與必要的
subprotocol 相同。不是瀏覽器的 client 可能送任何 `Origin`，或根本不送。

## 自己建立 Conn

**不建議這麼做。只有在你清楚自己在做什麼時才使用。**
`ServerHandShake` 會替你建立 `Conn`，過程中也會依 RFC 6455 檢查 request。`NewConn`
什麼都不檢查。handshake 違反規則的連線，或 masking 旗標設錯，從第一個 frame 起就會
破壞協定。如果 handshake 是你自己跑的，要替那條連線建立 `Conn`：

```go
conn, err := wlgows.NewConn(netConn, reader, writer, false) // false：server 不做 mask（5.1）
```

`Conn` 帶有未匯出的相依，所以直接寫 struct literal 會在第一次使用時 panic。error 只會
來自小於 14 byte 的 writer：它會先被 flush 再被替換，而這次 flush 可能失敗。

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
