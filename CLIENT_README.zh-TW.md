[English](./CLIENT_README.md) · **繁體中文**

# Client

client 如何從一個 URL，變成一個可以使用的 `*wlgows.Conn`：建立連線、TLS、
handshake、subprotocol，以及壓縮。

## Contents

- [建立連線](#建立連線)
- [TLS (wss://)](#tls-wss)
- [Handshake](#handshake) — [設定](#設定) · [加上 header](#加上-header) · [回傳值](#回傳值)
- [Subprotocols](#subprotocols) — [提出](#提出) · [檢查回應](#檢查回應) · [一定要有](#一定要有)
- [壓縮 (permessage-deflate)](#壓縮-permessage-deflate) — 設定見 [PERMESSAGE_DEFLATE.zh-TW.md](./PERMESSAGE_DEFLATE.zh-TW.md)
- [自己跑 handshake](#自己跑-handshake) — 不建議
- [下一步：讀取與傳送](#下一步讀取與傳送)

讀取見 [LISTENER_README.zh-TW.md](./LISTENER_README.zh-TW.md)，傳送見
[SENDING_README.zh-TW.md](./SENDING_README.zh-TW.md)，server 端見
[SERVER_README.zh-TW.md](./SERVER_README.zh-TW.md)。

## 建立連線

`DialContext` 只負責連線並建立開場的 request，其他什麼都不做。它不會執行
handshake，所以你還能先在 request 上加 header。

```go
// 兩者都會限制 TCP 連線與 TLS handshake 的時間，以先到者為準。
ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
defer cancel()
dialer := &net.Dialer{Timeout: 15 * time.Second}
netConn, request, err := wlgows.DialContext(ctx, "ws://localhost:8001", dialer, nil)
if err != nil {
	panic(err)
}
defer netConn.Close()
```

- **URL** 用 `ws://` 或 `wss://`。`http://` 與 `https://` 效果相同。
  沒有 port 時，`ws://` 連 80，`wss://` 連 443（RFC 6455 3）。
- **context** 會限制 TCP 連線，以及 `wss://` 的 TLS handshake 所花的時間。
  `DialContext` 回傳後它就不再有作用。要限制 WebSocket handshake 與 frame 的時間，
  請用 `netConn.SetDeadline`。
- **dialer** 用來設定 `Timeout`、`KeepAlive` 等選項。`nil` 表示使用預設值。

`Dial(url, tlsConfig)` 是較短的寫法：等同於用 `context.Background()` 與預設 dialer
呼叫 `DialContext`。它沒有逾時限制，所以如果 `wss://` server 接受了連線卻始終不回應
TLS handshake，`Dial` 會永遠等下去。

開多條連線時有兩條規則，都由你負責：

- **同一個 server 一次只做一個 handshake。** RFC 6455 4.1 規定同一個 IP 與 port
  同時只能有一條連線在進行 handshake。等前一條的 `ClientHandShake` 回傳後再開下一條。
- **重新連線要退避。** 連線中斷後，第一次重試前先隨機等 0 到 5 秒，之後每次失敗都
  等得更久（7.2.3）。

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
conn, resp, err := wlgows.ClientHandShake(netConn, reader, writer, request, wlgows.ClientHandShakeConfig{})
if err != nil {
	netConn.Close() // 每一種 handshake error 之後都建議關閉
	return
}
```

- **`reader`** 必須是你之後讀取 frame 用的那一個。讀取回應時，可能會把第一個 frame
  的開頭一起讀進去。
- **`writer`** 是每個 frame 送出時經過的地方。client 會在裡面替每個 payload 的複本做
  mask，所以它的大小決定了大 message 需要幾次 socket 寫入。見
  [記憶體與 write buffer](./SENDING_README.zh-TW.md#記憶體與-write-buffer)。
  handshake 的 request 本身直接寫進 `netConn`。
- **`config`** 是 handshake 要提出的內容。`ClientHandShakeConfig{}` 什麼都不提出，
  就是一般的 handshake。見 [設定](#設定)。

回傳的 `Conn` 會做 mask：client 送出的每個 frame 都要 mask（5.1）。

### 設定

`ClientHandShakeConfig` 放的是 handshake 要加到 request 上的內容：

| 欄位 | 加上 | 見 |
|---|---|---|
| `Subprotocols` | `Sec-WebSocket-Protocol`，最想用的放最前面 | [Subprotocols](#subprotocols) |
| `PermessageDeflates` | `Sec-WebSocket-Extensions`，每個是一個 permessage-deflate 提議 | [壓縮](#壓縮-permessage-deflate) |

```go
config := wlgows.ClientHandShakeConfig{
	Subprotocols:       []string{"chat.v2", "chat.v1"},
	PermessageDeflates: []wlgows.PermessageDeflateConfig{{}},
}
conn, resp, err := wlgows.ClientHandShake(netConn, reader, writer, request, config)
```

每個欄位都會單獨加成一行，放在 `request` 原有的那幾行之後；原有的行會照原樣送出。
同一個 header 既自己設定又放進 config，不建議這麼做，除非你清楚自己在做什麼。

### 加上 header

在 `DialContext` 與 `ClientHandShake` 之間設定在 `request` 上。你設定的每個 header
都會送出：

```go
request.Header.Set("Origin", "https://example.com")
request.Header.Set("Authorization", "Bearer "+token)
```

`ClientHandShake` 只會設定 RFC 6455 4.1 規定的部分：method `GET`、版本
`HTTP/1.1`、沒有 body，以及 `Upgrade`、`Connection`、`Sec-WebSocket-Key` 與
`Sec-WebSocket-Version`。你沒有設定 `Host` 時，它也會從 URL 填入，並加上 `config`
要求的內容。

### 回傳值

| 結果 | `conn` | `resp` | `err` |
|---|---|---|---|
| 升級成功 | 那個 `Conn` | 收到的 101 | `nil` |
| request 的 URL 沒有 host | `nil` | `nil` | `ErrUrlHostEmpty` |
| `config` 裡有 wlgows 拒絕的提議 —— 見 [壓縮](#壓縮-permessage-deflate) | `nil` | `nil` | `ErrHandshakeRequestDeflateUnsupported` 或 `ErrHandshakeRequestDeflateWindowBitsInvalid` |
| request 送不出去 | `nil` | `nil` | 原因 |
| 回應讀不進來 | `nil` | `nil` | 原因 |
| server 拒絕，或回應不正確 | `nil` | 它的回應 | 原因 |
| 升級成功，但 writer 無法 flush | `nil` | 收到的 101 | 寫入的 error |

只要 `err` 不是 `nil`，`conn` 就是 `nil`。被拒絕時會得到
`ErrHandshakeResponseStatusCodeInvalid`，原因在 `resp.StatusCode` 與 `resp.Body` 裡。
回應不正確的其他情況，各有一個可用 `errors.Is` 比較的 sentinel：

| Error | 回應 | 會關閉連線 |
|---|---|---|
| `ErrHandshakeResponseProtoInvalid` | 是 101，但不是 HTTP/1.1 | 是 |
| `ErrHandshakeResponseConnectionHeaderInvalid` | `Connection` 中沒有 `Upgrade` token | 是 |
| `ErrHandshakeResponseUpgradeHeaderInvalid` | `Upgrade` 中沒有 `websocket` token | 是 |
| `ErrHandshakeResponseAcceptHeaderMissing` | 沒有 `Sec-WebSocket-Accept` | 是 |
| `ErrHandshakeResponseAcceptHeaderMismatch` | `Sec-WebSocket-Accept` 與送出的 key 不符 | 是 |
| `ErrHandshakeResponseSubprotocolRepeated` | `Sec-WebSocket-Protocol` header 出現不只一次 —— 見 [Subprotocols](#subprotocols) | 是 |
| `ErrHandshakeResponseSubprotocolMultiple` | `Sec-WebSocket-Protocol` 同一行有不只一個值 —— 見 [Subprotocols](#subprotocols) | 是 |
| `ErrHandshakeResponseSubprotocolNotOffered` | `Sec-WebSocket-Protocol` 不是你提出的值 —— 見 [Subprotocols](#subprotocols) | 是 |
| `ErrHandshakeResponseExtensionNotOffered` | 接受了兩次 permessage-deflate，或接受的方式沒有任何提議允許 —— 見 [壓縮](#壓縮-permessage-deflate) | 是 |
| `ErrHandshakeResponseDeflateParamsInvalid` | 有 RFC 7692 7 禁止的 permessage-deflate 參數 —— 見 [壓縮](#壓縮-permessage-deflate) | 是 |

**誰來關閉連線。** RFC 6455 4.1 對某些不正確的回應要求「Fail the WebSocket
Connection」，也就是關閉連線（7.1.7）。對這些情況，也就是上表中「是」的那幾列，
`ClientHandShake` 會在回傳 error 之前關閉 `netConn`。這個 error 就是 7.1.7 允許的
回報。其他 error 都會讓 `netConn` 保持開啟，由你負責關閉：送出或讀取失敗，以及不是
101 的狀態碼；4.1 規定後者要當成一般 HTTP 處理，例如
redirect 或 401。

**除非你有更好的理由，否則我們建議在每一種 handshake error 之後都關閉連線。** 不論是哪一種，這條連線都不可能
再變成 WebSocket。如果狀態碼不是 101，想看 `resp` 的話先看，再關閉：跟隨 redirect
或帶上認證重試，都需要一條新的連線。建立連線後馬上寫 `defer netConn.Close()`，就能
涵蓋所有情況。對 `ClientHandShake` 已經關閉的連線，第二次關閉只會回傳
`net.ErrClosed`。

`ClientMustFailConnection(err)` 能分辨這兩種：對上表中「是」的那幾列回傳 `true`。
用它來判斷連線是否已經被關閉；或者，如果你[自己跑 handshake](#自己跑-handshake)，
用它來判斷什麼時候該關閉。

它只認得 handshake 的 error。`Listen` 的 error 請見
[`StandardClosePayloadFor`](./LISTENER_README.zh-TW.md#errors)。

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
也會送出，同樣算是提出的值。已經在裡面的值不會重複加上。

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

這兩種都是 4.1 的 Fail，所以 `ClientHandShake` 會在回傳之前關閉連線，`conn` 是
`nil`。WebSocket 連線還沒建立，所以 7.1.7 不要求送 close frame。

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

### 一定要有

**什麼時候。** 只有在你的 client 沒有那個 subprotocol 就無法和 server 溝通時，才把
它當成必要條件。如果它也能用一般的 WebSocket 溝通，就接受 `""` 繼續進行。

**怎麼做。** 回應是空的就表示 server 一個都沒選。handshake 無法知道這對你
來說是否可以接受，所以這個檢查由你負責。這時連線已經建立，所以先送 close frame
開始 closing handshake，再關閉（7.1.2）：

```go
if resp.Header.Get("Sec-WebSocket-Protocol") == "" {
	conn.SendClose(&wlgows.ClosePayload{StatusCode: wlgows.ClosePolicyViolation, Reason: "subprotocol chat.v2 required"})
	netConn.Close() // server 不支援 chat.v2 也不支援 chat.v1；client SHOULD 先短暫等待 server 關閉（5.5.1）
	return
}
```

如果 server 要求一個你沒有提出的 subprotocol，它會直接拒絕 handshake。這會以
`ErrHandshakeResponseStatusCodeInvalid` 回來，通常是 400，原因在 `resp.Body` 裡。

兩個 client 範例都有提出與檢查的程式，以註解的形式保留。

## 壓縮 (permessage-deflate)

`config.PermessageDeflates` 的每一項就是一個提議，最想用的放最前面。每個欄位的意思見
[PERMESSAGE_DEFLATE.zh-TW.md](./PERMESSAGE_DEFLATE.zh-TW.md#permessagedeflateconfig)。

```go
config := wlgows.ClientHandShakeConfig{
	PermessageDeflates: []wlgows.PermessageDeflateConfig{
		{ClientNoContextTakeover: true, ServerNoContextTakeover: true}, // 第一選擇
		{}, // 退而求其次：一般的 permessage-deflate
	},
}
```

不建議自己設定 `Sec-WebSocket-Extensions`。你自己寫的 header 會照原樣送出，打錯字也
一樣。config 會在 request 送出之前檢查，所以用錯時會得到 error，而不是送上網路。
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
連線會在不壓縮的情況下建立。沒有任何提議允許的回應，或帶有
[不合法項目](./PERMESSAGE_DEFLATE.zh-TW.md#不合法的項目)的回應，會中斷連線
（RFC 7692 5）：

| server 回傳 | Error |
|---|---|
| 兩次 permessage-deflate | `ErrHandshakeResponseExtensionNotOffered` |
| `client_max_window_bits`，wlgows 從不提出它 | `ErrHandshakeResponseExtensionNotOffered` |
| 沒有任何一個提議允許的參數，例如比提議更大的 `server_max_window_bits`，或提議要求了 `server_no_context_takeover` 或 `server_max_window_bits` 卻沒有回傳（RFC 7692 7.1.1.1、7.1.2.1） | `ErrHandshakeResponseExtensionNotOffered` |
| 不合法的項目 | `ErrHandshakeResponseDeflateParamsInvalid` |

`ClientHandShake` 不會回傳雙方約定的內容。請用 `ValidateHandShakeResponseDeflate` 取得：

```go
agreed, err := wlgows.ValidateHandShakeResponseDeflate(resp, request) // server 拒絕時 agreed 是 nil
```

`agreed` 是雙方都遵守的內容，不是回應的複本。如果某個允許這個回應的提議有
`client_no_context_takeover`，就算回應沒有包含它，`agreed` 也會有。那個提議是 client
自己的承諾（RFC 7692 7.1.1.2），而回應不會說它接受的是哪一個提議。

## 自己跑 handshake

**不建議這麼做。只有在你清楚自己在做什麼時才使用。**
`ClientHandShake` 會照這個順序執行下列步驟，並在 RFC 6455 要求時關閉連線。自己執行
時，漏掉或調換任何一步都會破壞協定，而且不會有任何警告。

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
// 4. 檢查回應：RFC 6455 4.1、subprotocol，再來是 permessage-deflate。
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
if _, err := wlgows.ValidateHandShakeResponseDeflate(resp, request); err != nil {
	if wlgows.ClientMustFailConnection(err) {
		netConn.Close() // RFC 7692 5
	}
	return err
}
// 5. 建立 Conn。true：client 送出的每個 frame 都要 mask（RFC 6455 5.1）。
conn, err := wlgows.NewConn(netConn, reader, writer, true)
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
