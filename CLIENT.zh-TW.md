[English](./CLIENT.md) · **繁體中文**

# Client

client 怎麼從一個 URL 拿到可以用的 `*wlgows.Conn`：建立連線、TLS、handshake、
subprotocol，以及壓縮。

## Contents

- [建立連線](#建立連線)
- [TLS (wss://)](#tls-wss)
- [Handshake](#handshake) — [設定](#設定) · [加上 header](#加上-header) · [回傳值](#回傳值)
- [Subprotocols](#subprotocols) — [提出](#提出) · [檢查回應](#檢查回應) · [強制使用](#強制使用-subprotocol)
- [壓縮 (permessage-deflate)](#壓縮-permessage-deflate) — 設定見 [PERMESSAGE_DEFLATE.zh-TW.md](./PERMESSAGE_DEFLATE.zh-TW.md)
- [自己跑 handshake](#自己跑-handshake) — 不建議
- [下一步：讀取與傳送](#下一步讀取與傳送)

讀取見 [READING.zh-TW.md](./READING.zh-TW.md)，傳送見
[SENDING.zh-TW.md](./SENDING.zh-TW.md)，server 端見
[SERVER.zh-TW.md](./SERVER.zh-TW.md)。

## 建立連線

`DialContext` 只負責連線並建立開場的 request，其他什麼都不做。它不會執行
handshake，所以你還能先在 request 上加 header。

```go
// 兩者都會限制 TCP 連線與 TLS handshake 的時間，以較短的為準。
ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
defer cancel()
dialer := &net.Dialer{Timeout: 15 * time.Second}
netConn, request, err := wlgows.DialContext(ctx, "ws://localhost:8001", dialer, nil)
if err != nil {
	panic(err)
}
defer netConn.Close()
```

- **URL** 用 `ws://` 或 `wss://`。`http://` 與 `https://` 分別和 `ws://`、`wss://`
  效果相同。沒有 port 時，`ws://` 連 80，`wss://` 連 443（RFC 6455 3）。
- **context** 會限制 TCP 連線，以及 `wss://` 的 TLS handshake 所花的時間。
  `DialContext` 回傳後它就不再有作用。要限制 WebSocket handshake 與 frame 的時間，
  請用 `netConn.SetDeadline`。
- **dialer** 用來設定 `Timeout`、`KeepAlive` 等選項。`nil` 表示使用預設的 dialer。

`Dial(url, tlsConfig)` 是較短的寫法：等同於用 `context.Background()` 與預設 dialer
呼叫 `DialContext`。它沒有逾時限制，所以如果 `wss://` server 接受了連線卻始終不回應
TLS handshake，`Dial` 會永遠等下去。

開多條連線時有兩條規則，都由你負責：

- **同一個 server 一次只做一個 handshake。** RFC 6455 4.1 規定同一個 IP 與 port
  同時只能有一條連線在進行 handshake。等前一條的 `ClientHandShake` 回傳後再開下一條。
- **重新連線前要等，而且越等越久。** 連線中斷後，第一次重試前先隨機等 0 到 5 秒，
  之後每次失敗都等得更久（7.2.3）。

## TLS (wss://)

傳入一個 `*tls.Config`。`nil` 會信任系統的根憑證。要信任你自己的 CA：

```go
caCert, _ := os.ReadFile("ca.crt")
caCertPool := x509.NewCertPool()
caCertPool.AppendCertsFromPEM(caCert)

netConn, request, err := wlgows.DialContext(ctx, "wss://localhost:8001", dialer, &tls.Config{RootCAs: caCertPool})
```

## Handshake

`ClientHandShake(netConn, reader, writer, request, config)` 會加上升級用的 header
與 `config` 要求的內容、送出 request、讀取回應、依 RFC 6455 4.1 檢查回應，再建立
`Conn`。

```go
reader := bufio.NewReader(netConn)
writer := bufio.NewWriter(netConn)
// ClientHandShake 沒有設逾時。沒有這一行的話，只要 server 一直不回應，
// 它就會一直卡住。
netConn.SetReadDeadline(time.Now().Add(10 * time.Second))
conn, resp, err := wlgows.ClientHandShake(netConn, reader, writer, request, wlgows.ClientHandShakeConfig{})
if err != nil {
	netConn.Close() // 每次 handshake 出錯都建議關閉
	return
}
// 記得清掉，否則之後讀 frame 也會被它中斷。
netConn.SetReadDeadline(time.Time{})
```

- **`reader`** 必須是你之後讀取 frame 用的那一個。讀取回應時，可能會把第一個 frame
  的開頭一起讀進去。
- **`writer`** 是每個 frame 送出時都會經過的 buffer。它的大小決定一個大 message
  要寫幾次 socket。見 [記憶體與 write buffer](./SENDING.zh-TW.md#記憶體與-write-buffer)。
- **`config`** 是 handshake 要提出的內容。`ClientHandShakeConfig{}` 什麼都不提出，
  就是一般的 handshake。見 [設定](#設定)。

### 設定

`ClientHandShakeConfig` 放的是 handshake 要加到 request 上的內容：

| 欄位 | 加上 | 見 |
|---|---|---|
| `Subprotocols` | `Sec-WebSocket-Protocol`，最想用的放最前面 | [Subprotocols](#subprotocols) |
| `PermessageDeflates` | `Sec-WebSocket-Extensions`，每一項是一個 permessage-deflate 提議 | [壓縮](#壓縮-permessage-deflate) |

```go
config := wlgows.ClientHandShakeConfig{
	Subprotocols:       []string{"chat.v2", "chat.v1"},
	PermessageDeflates: []wlgows.PermessageDeflateConfig{
		{ServerNoContextTakeover: true, ClientNoContextTakeover: true},
	},
}
conn, resp, err := wlgows.ClientHandShake(netConn, reader, writer, request, config)
```

每個欄位都會單獨加成一行，放在 `request` 中同一個 header 原有的行之後。你自己設定的
行會照原樣送出。不建議同一個 header 既自己設定、又放進 config，除非你清楚自己在做
什麼。

### 加上 header

在 `DialContext` 之後、`ClientHandShake` 之前，把 header 設定到 `request` 上。你設定
的每個 header 都會送出：

```go
request.Header.Set("Origin", "https://example.com")
request.Header.Set("Authorization", "Bearer "+token)
```

`ClientHandShake` 只會設定 RFC 6455 4.1 規定的部分：method `GET`、版本
`HTTP/1.1`、沒有 body，以及 `Upgrade`、`Connection`、`Sec-WebSocket-Key` 與
`Sec-WebSocket-Version`。你沒有設定 `Host` 時，它會從 URL 填入。它也會加上
`config` 要求的內容。

### 回傳值

| 結果 | `conn` | `resp` | `err` |
|---|---|---|---|
| 升級成功 | 那個 `Conn` | 收到的 101 | `nil` |
| `request` 是 `nil` | `nil` | `nil` | `ErrHandshakeRequestNil` |
| request 的 URL 沒有 host | `nil` | `nil` | `ErrUrlHostEmpty` |
| `config` 裡有 wlgows 拒絕的提議 —— 見 [壓縮](#壓縮-permessage-deflate) | `nil` | `nil` | `ErrHandshakeRequestDeflateUnsupported` 或 `ErrHandshakeRequestDeflateWindowBitsInvalid` |
| request 送不出去 | `nil` | `nil` | 原因 |
| 回應讀不進來 | `nil` | `nil` | 原因 |
| server 拒絕，或回應不正確 | `nil` | server 的回應 | 原因 |
| 升級成功，但 writer 無法 flush | `nil` | 收到的 101 | 寫入的 error |

只要 `err` 不是 `nil`，`conn` 就是 `nil`。被拒絕時會得到
`ErrHandshakeResponseStatusCodeInvalid`，原因在 `resp.StatusCode` 與 `resp.Body` 裡。
回應不正確的其他情況，各有一個可用 `errors.Is` 比較的 sentinel。下表的每一種 error，
`ClientHandShake` 都會關閉連線：

| Error | 回應 |
|---|---|
| `ErrHandshakeResponseProtoInvalid` | 是 101，但不是 HTTP/1.1 |
| `ErrHandshakeResponseConnectionHeaderInvalid` | `Connection` 中沒有 `Upgrade` token |
| `ErrHandshakeResponseUpgradeHeaderInvalid` | `Upgrade` 中沒有 `websocket` token |
| `ErrHandshakeResponseAcceptHeaderMissing` | 沒有 `Sec-WebSocket-Accept` |
| `ErrHandshakeResponseAcceptHeaderMismatch` | `Sec-WebSocket-Accept` 與送出的 key 不符 |
| `ErrHandshakeResponseSubprotocolRepeated` | `Sec-WebSocket-Protocol` header 出現不只一次 —— 見 [Subprotocols](#subprotocols) |
| `ErrHandshakeResponseSubprotocolMultiple` | `Sec-WebSocket-Protocol` 同一行有不只一個值 —— 見 [Subprotocols](#subprotocols) |
| `ErrHandshakeResponseSubprotocolNotOffered` | `Sec-WebSocket-Protocol` 不是你提出的值 —— 見 [Subprotocols](#subprotocols) |
| `ErrHandshakeResponseExtensionNotOffered` | 有你沒提出的 extension、接受了兩次 permessage-deflate，或接受的方式沒有任何提議允許 —— 見 [壓縮](#壓縮-permessage-deflate) |
| `ErrHandshakeResponseDeflateParamsInvalid` | 有 RFC 7692 7 禁止的 permessage-deflate 參數 —— 見 [壓縮](#壓縮-permessage-deflate) |

**誰來關閉連線。** 對某些不正確的回應，RFC 6455 4.1 與 RFC 7692 5 要求「Fail the
WebSocket Connection」，也就是關閉連線（RFC 6455 7.1.7）。上表的每一種 error 都是
這些情況：`ClientHandShake` 會在回傳前關閉 `netConn`，再用回傳的 error 告訴你失敗
原因。其他 error 都不會關閉 `netConn`，你必須自己關閉，像是送出或讀取失敗，或狀態碼
不是 101。RFC 6455 4.1 規定，狀態碼不是 101 時要當成一般 HTTP 處理，例如 redirect
或 401。

**我們建議每次 handshake 出錯都關閉連線。** 出錯之後，這條連線不可能再變成
WebSocket。狀態碼不是 101 時，可以先讀 `resp` 再關閉：要照 redirect 重連，或帶上
認證重試，都得開一條新的連線。建立連線後馬上寫 `defer netConn.Close()`，就能
涵蓋所有情況。對 `ClientHandShake` 已經關閉的連線，第二次關閉只會回傳
`net.ErrClosed`。

`ClientMustFailConnection(err)` 能分辨這兩種：上表的每一種 error 都會回傳 `true`。
用它可以知道連線是不是已經關了。如果你[自己跑 handshake](#自己跑-handshake)，也用它
決定什麼時候該關閉。

它只認得 handshake 的 error。`Listen` 的 error 請見
[`StandardClosePayloadFor`](./LISTENER.zh-TW.md#errors)。

## Subprotocols

subprotocol 是你的 message 所遵循的應用層協定，例如 `graphql-transport-ws`。client
提出一份清單，server 從中回傳一個，或一個都不回（RFC 6455 4.2.2）。

### 提出

放進 `config.Subprotocols`，最想用的放最前面。它們會放在同一個
`Sec-WebSocket-Protocol` header 裡送出：

```go
config := wlgows.ClientHandShakeConfig{Subprotocols: []string{"chat.v2", "chat.v1"}}
```

順序只是建議，由 server 決定。你自己設定在 `request` 上的 `Sec-WebSocket-Protocol`
也會送出，同樣算是提出的值。已經在裡面的值不會重複加上。不建議自己設定這個 header，
請用 `config.Subprotocols`。

### 檢查回應

RFC 6455 4.1 規定：如果 server 回傳了 client 沒有提出的 subprotocol，client 必須
中斷連線。server 也只能回傳一個值（4.2.2）。`ClientHandShake` 會替你檢查這兩點，
error 訊息中會寫出 server 送來的值：

| server 回傳 | Error |
|---|---|
| header 出現不只一次（11.3.4） | `ErrHandshakeResponseSubprotocolRepeated` |
| 同一行有不只一個值 | `ErrHandshakeResponseSubprotocolMultiple` |
| 你沒有提出的值，或你什麼都沒提出時的任何值 | `ErrHandshakeResponseSubprotocolNotOffered` |

沒有 `Sec-WebSocket-Protocol` header 表示 server 一個都沒選（4.2.2），會通過檢查。
空的 header 則不同：server 送了東西，只是送的東西不合法，所以會以
`ErrHandshakeResponseSubprotocolNotOffered` 失敗。

這三種都是 4.1 的 Fail，所以 `ClientHandShake` 會在回傳之前關閉連線，`conn` 是
`nil`。

```go
config := wlgows.ClientHandShakeConfig{Subprotocols: []string{"chat.v2", "chat.v1"}}

conn, resp, err := wlgows.ClientHandShake(netConn, reader, writer, request, config)
if err != nil {
	netConn.Close() // subprotocol 檢查失敗時連線已經關閉；第二次關閉不會有影響
	return
}
subprotocol := resp.Header.Get("Sec-WebSocket-Protocol") // 你提出的其中一個，server 一個都沒選時是 ""
```

如果要自己做同樣的檢查，見 [自己跑 handshake](#自己跑-handshake)。

### 強制使用 subprotocol

**什麼時候該強制使用？** 只有在你的 client 沒有那個 subprotocol 就無法和 server 溝通
時，才強制使用。如果它也能用一般的 WebSocket 溝通，就接受 `""` 繼續進行。

**怎麼做？** 回應是空的就表示 server 一個都沒選。handshake 不知道這對你
來說行不行，所以這個檢查由你負責。這時連線已經建立，所以先送 close frame
開始 closing handshake，再關閉（7.1.2）：

```go
if resp.Header.Get("Sec-WebSocket-Protocol") == "" {
	conn.SendClose(&wlgows.ClosePayload{StatusCode: wlgows.ClosePolicyViolation, Reason: "subprotocol chat.v2 or chat.v1 required"})
	// server 一個都沒選。client SHOULD 先短暫等待 server 關閉（RFC 6455 5.5.1）。
	netConn.Close()
	return
}
```

如果 server 要求一個你沒有提出的 subprotocol，它會直接拒絕 handshake。你會收到
`ErrHandshakeResponseStatusCodeInvalid`，通常是 400，原因在 `resp.Body` 裡。

兩個 client 範例都有提出與檢查的程式，只是先註解掉了。

## 壓縮 (permessage-deflate)

`config.PermessageDeflates` 的每一項就是一個提議，最想用的放最前面。每個欄位的意思見
[PERMESSAGE_DEFLATE.zh-TW.md](./PERMESSAGE_DEFLATE.zh-TW.md#permessagedeflateconfig)。
協商成功後怎麼送出壓縮的 message，見
[SENDING.zh-TW.md](./SENDING.zh-TW.md#壓縮-permessage-deflate)。

**我們建議兩邊送出時都不使用 context。** 這樣每條連線在 message 之間幾乎不佔壓縮用的
記憶體。數字見 [PERMESSAGE_DEFLATE.zh-TW.md](./PERMESSAGE_DEFLATE.zh-TW.md#每個連線)。
server 的清單也用同一份設定：

```go
config := wlgows.ClientHandShakeConfig{
	PermessageDeflates: []wlgows.PermessageDeflateConfig{
		{
			ServerNoContextTakeover: true, // server 送出時不使用 context
			ClientNoContextTakeover: true, // client 送出時不使用 context
		},
	},
}
```

不建議自己設定 `Sec-WebSocket-Extensions`。你自己寫的 header 會照原樣送出，打錯字也
一樣。config 會在 request 送出之前檢查，所以用錯時會得到 error，而不會送出去。
`ClientHandShake` 會拒絕兩種設定，並讓 `netConn` 保持開啟：

| 設定 | Error |
|---|---|
| `ClientMaxWindowBitsEnabled` 為 true | `ErrHandshakeRequestDeflateUnsupported` |
| `ServerMaxWindowBits` 不是 0 或 8 到 15（RFC 7692 7.1.2.1） | `ErrHandshakeRequestDeflateWindowBitsInvalid` |

第一種是函式庫的選擇。`client_max_window_bits` 會讓 server 能把 client 的 window 設在
15 bit 以下，而 wlgows 無法用那麼小的 window 送出（見
[15 bit 以下的 window](./PERMESSAGE_DEFLATE.zh-TW.md#15-bit-以下的-window)）。不送它，
client 永遠可以使用 15 bit。

server 會接受其中一個提議，或一個都不接受。回應中沒有 permessage-deflate 不是 error：
連線照樣建立，只是不壓縮。沒有任何提議允許的回應，或帶有
[不合法項目](./PERMESSAGE_DEFLATE.zh-TW.md#不合法的項目)的回應，會中斷連線
（RFC 6455 4.1、RFC 7692 5）：

| server 回傳 | Error |
|---|---|
| 你沒提出的 extension（RFC 6455 4.1） | `ErrHandshakeResponseExtensionNotOffered` |
| 兩次 permessage-deflate | `ErrHandshakeResponseExtensionNotOffered` |
| `client_max_window_bits=N`，wlgows 從不提出它 | `ErrHandshakeResponseExtensionNotOffered` |
| 沒有任何一個提議允許的參數，例如比提議更大的 `server_max_window_bits`，或提議要求了 `server_no_context_takeover` 或 `server_max_window_bits` 卻沒有回傳（RFC 7692 7.1.1.1、7.1.2.1） | `ErrHandshakeResponseExtensionNotOffered` |
| 不合法的項目 | `ErrHandshakeResponseDeflateParamsInvalid` |

如果你自己在 request 的 header 裡提出 permessage-deflate 以外的 extension，server 在
回應裡接受它時，client 不會把它當成錯誤。wlgows 不懂那個 extension，要怎麼照它的規則
處理由你負責。

`ClientHandShake` 不會回傳協商結果。請用 `ValidateHandShakeResponseExtensions` 取得：

```go
agreed, err := wlgows.ValidateHandShakeResponseExtensions(resp, request) // server 拒絕時 agreed 是 nil
```

`agreed` 是一個 `*PermessageDeflateConfig`：雙方都遵守的內容，不是回應的複本。
只要有一個符合這個回應的提議帶有 `client_no_context_takeover`，就算回應裡沒有它，
`agreed` 也會有。那個提議是 client 自己的承諾（RFC 7692 7.1.1.2），而回應不會說
它接受的是哪一個提議。

## 自己跑 handshake

**不建議這麼做。只有在你清楚自己在做什麼時才使用。**
`ClientHandShake` 會照這個順序執行下列步驟，並在 RFC 6455 或 RFC 7692 要求時關閉連線。
自己執行時，漏掉或調換任何一步都會破壞協定，而且不會有任何警告。

```go
reader := bufio.NewReader(netConn)
writer := bufio.NewWriter(netConn)

// 1. 把 request 變成開場的 handshake，再加上 config 要求的內容。
if err := wlgows.UpgradeRequest(request, config); err != nil {
	return err
}
// 2. 送到 netConn。writer 是給 frame 用的。
if err := wlgows.SendHandShakeRequest(netConn, request); err != nil {
	return err
}
// 3. 從之後要讀 frame 的那個 reader 讀取回應。
resp, err := wlgows.ReadHandShakeResponse(reader, request)
if err != nil {
	return err
}
// 4. 檢查回應：RFC 6455 4.1、subprotocol，再來是 extension。
if err := wlgows.ValidateHandShakeResponse(resp, request.Header.Get("Sec-WebSocket-Key")); err != nil {
	if wlgows.ClientMustFailConnection(err) {
		netConn.Close() // RFC 6455 7.1.7
	}
	return err
}
if err := wlgows.ValidateHandShakeResponseSubprotocol(resp, request); err != nil {
	if wlgows.ClientMustFailConnection(err) {
		netConn.Close() // RFC 6455 7.1.7
	}
	return err
}
agreed, err := wlgows.ValidateHandShakeResponseExtensions(resp, request)
if err != nil {
	if wlgows.ClientMustFailConnection(err) {
		netConn.Close() // RFC 6455 7.1.7
	}
	return err
}
// 5. 建立 Conn。對方是 server，所以這個 Conn 送出的每個 frame 都要 mask
// （RFC 6455 5.1）。沒有協商出 deflate 時，agreed 為 nil。
conn, err := wlgows.NewConn(netConn, reader, writer, wlgows.NewConnConfig{PeerIsClient: false, PermessageDeflate: agreed})
```

- **`NewConn` 什麼都不檢查。** 如果 handshake 違反了規則，或 `PeerIsClient`、協商結果
  給錯，這條連線從第一個 frame 起就會破壞協定。
- **它回傳的 error** 是寫入的 error，發生在 flush 小於 14 byte 的 `writer` 時。
- **直接寫 struct literal 會 panic。** `Conn` 帶有未匯出的相依物件，所以只能用 `NewConn`
  建立。

## 下一步：讀取與傳送

拿到 `Conn` 之後，剩下的就是讀取與傳送：

- **讀取：**[READING.zh-TW.md](./READING.zh-TW.md) —— message 的 byte、大小上限、
  error，以及壓縮的 message。
- **Listener：**[LISTENER.zh-TW.md](./LISTENER.zh-TW.md) —— hook、上限、每種 error 的
  意義，以及[偵測已斷線的對方](./LISTENER.zh-TW.md#它沒有-ping-pong-存活偵測)。
- **傳送：**[SENDING.zh-TW.md](./SENDING.zh-TW.md) ——
  [該用哪個呼叫](./SENDING.zh-TW.md#選擇要用哪個呼叫)、串流傳送大的 message、
  [定期送出 ping](./SENDING.zh-TW.md#定期送出-ping)、write buffer，以及 lock。
