[English](./README.md) · **繁體中文**

# WLGOWS

一個簡單、直覺且功能完整的 Go WebSocket 函式庫：容易上手，也不對底層協定做任何
隱藏。

下方 Quick Start 的程式碼複製貼上就能執行，是一個完整的 echo server：一個
constructor、一個 hook，再加上 `conn.NewStandardListener()` 自動處理 ping、pong
和 close。需要更多控制時，該有的功能都有：串流傳送比記憶體還大的 message、自行
處理每一個 data frame、依自己的節奏發送 ping，甚至完全手動組出 frame 再送出。
這個函式庫以 frame 為單位運作，而不是包一層抽象，所以沒有任何細節被藏起來：
server 與 client、手動 handshake，以及 RFC 6455 要求由你負責的每一條規則，都
清楚擺在你看得到的地方。

**正確性優先。** 不同於許多追求 benchmark 數字的套件，正確性是我們的第一優先。
我們永遠追求最快的速度，但前提是程式依然正確且安全：不使用 `unsafe`，也不繞過
bounds check。如果你清楚自己在做什麼，你自己的程式可以更進一步 —— 見
[更快、更省記憶體](./SENDING_README.zh-TW.md#更快更省記憶體用-unsafe-送出-string)。

## Features

- **WebSocket Server**：直接在 raw TCP 上執行，搭配 HTTP handshake
- **WebSocket Client**：支援 `ws://` 與 `wss://`
- **TLS/SSL**：兩端皆支援
- **HTTP Hijacking**：支援 `http.Server` 與 Gin
- **Listener**：單一 read loop，依 RFC 6455 驗證每個 frame 後交給你的 hook
- **Frame-level control**：需要時可自行組出 frame 並送出
- **Streaming**：以一個個 fragment 傳送比記憶體還大的 message
- **Memory in your hands**：每條連線的 write buffer 由你提供，最小 14 byte，而且隨時可以更換；每次傳送也能自己選 frame 大小 —— 見 [SENDING_README.zh-TW.md](./SENDING_README.zh-TW.md#掌控權在你手上)
- **Keepalive**：定期發送 ping，每次的 payload 由你決定
- **Concurrent sending**：以 lock 保護，且 control frame 不會被卡在大型 message
  後面

## Contents

- [Installation](#installation)
- [Quick Start](#quick-start) — [Server](#server) · [Client](#client) · [TLS](#tls-wss) · [HTTP Hijacking](#http-hijacking)
- [Examples](#examples) — 三個 echo server、一個 client，以及一組 streaming 範例
- [從 v6 升級](#從-v6-升級)
- [Design](#design)
- [記憶體 vs. 速度](#記憶體-vs-速度自己決定-buffer-的大小) — [Reader](#reader) · [Writer](#writer) · [最小的連線](#最小的連線)
- [Benchmark 比較](#benchmark-比較) — 與 gorilla/websocket 和 gobwas/ws 比較
- [Reading](#reading) — 使用 `Listener`，或一次讀取一個 frame
- [Sending](#sending) — 完整 message、control frame、streaming、自訂 frame
- [Keepalive](#keepalive) — 定期 ping，以及如何偵測對方已無回應
- [Concurrency](#concurrency) — 每個 lock 分別保護什麼
- [Errors](#errors) — sentinel error 與 `StandardClosePayloadFor`
- [Testing](#testing) — [替換相依](#替換相依)
- [License](#license)

`Listener` 有獨立的說明文件：**[LISTENER_README.zh-TW.md](./LISTENER_README.zh-TW.md)**，
傳送也有：**[SENDING_README.zh-TW.md](./SENDING_README.zh-TW.md)**。

## Installation

```bash
go get github.com/weilun-shrimp/wlgows/v7
```

依照 Go 對 major version 2 以上的規定，import path 需要加上 `/v7` 後綴；package
名稱仍然是 `wlgows`，所以程式中的寫法依然是 `wlgows.Dial(...)`。

## Quick Start

### Server

```go
package main

import (
	"bufio"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/weilun-shrimp/wlgows/v7"
)

func main() {
	server, err := wlgows.Run(":8001")
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
}

func handle(netConn net.Conn) {
	defer netConn.Close()

	// Accept 只負責取得 TCP 連線，讀取 request 與執行 handshake 都由你處理，
	// 確保每個送上線的 byte 都在你的掌握之中。
	r := bufio.NewReader(netConn) // 每條連線 4096 byte；想縮小請見「記憶體 vs. 速度」
	req, err := http.ReadRequest(r)
	if err != nil {
		return
	}
	conn, _, err := wlgows.ServerHandShake(netConn, r, bufio.NewWriter(netConn), req) // 每條連線 4096 byte
	if err != nil {
		return
	}

	// ping、pong、close 與保留 opcode 都已處理好，masking 也已依連線端別設定完成。
	listener := conn.NewStandardListener()

	// SetConfig 會整份替換設定，所以請從標準 hook 設好的那份開始修改。
	config := listener.GetConfig()
	config.MaxDataFramesSize = 10 * 1024 * 1024
	config.Text = func(frames wlgows.DataFrames) {
		conn.SendText(frames.Bytes(), 0) // echo
	}
	listener.SetConfig(config)

	// 心跳，在獨立的 goroutine 中執行，連線結束時會自動停止。若要偵測
	// 「連線還在卻一直不回 pong」的對方，請見 Keepalive。
	go conn.StartPingLoop(30*time.Second, nil)

	if err := listener.Listen(); err != nil {
		if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
			conn.SendClose(payload)
		}
		log.Println("closing:", err)
	}
}
```

### Client

架構和 server 相同。masking 的方向相反（server 送出的 frame 不做 mask），但這裡
不需要特別處理：`Conn` 知道自己是哪一端，`NewStandardListener` 會直接沿用。

```go
package main

import (
	"bufio"
	"log"
	"time"

	"github.com/weilun-shrimp/wlgows/v7"
)

func main() {
	// Dial 只負責建立連線並組好 request，handshake 由你執行，所以送出前仍可
	// 替 req 加上 header。
	netConn, req, err := wlgows.Dial("ws://localhost:8001", nil)
	if err != nil {
		panic(err)
	}
	defer netConn.Close()

	r := bufio.NewReader(netConn) // 每條連線 4096 byte；想縮小請見「記憶體 vs. 速度」
	conn, _, err := wlgows.ClientHandShake(netConn, r, bufio.NewWriter(netConn), req) // 每條連線 4096 byte
	if err != nil {
		panic(err)
	}

	listener := conn.NewStandardListener()

	config := listener.GetConfig() // 標準 hook 設好的那份
	config.MaxDataFramesSize = 10 * 1024 * 1024
	config.Text = func(frames wlgows.DataFrames) {
		log.Println("received:", frames.String())
	}
	listener.SetConfig(config)

	go conn.StartPingLoop(30*time.Second, nil) // 心跳；會自動停止

	conn.SendText([]byte("Hello, WebSocket!"), 0)

	if err := listener.Listen(); err != nil {
		if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
			conn.SendClose(payload)
		}
		log.Println("closing:", err)
	}
}
```

兩端都不必處理 masking：`ClientHandShake` 與 `ServerHandShake` 在建立回傳的
`Conn` 時就會設定好 `maskSendFrame`，因此 client 呼叫 `SendText`、`SendPong`
時會做 mask，server 則不會。

兩個 handshake 都在 reader 旁邊接收一個 `*bufio.Writer`：每個 frame 都經由它送出。
想自己決定大小請見 [記憶體 vs. 速度](#記憶體-vs-速度自己決定-buffer-的大小)。

### TLS (wss://)

```go
caCert, _ := os.ReadFile("ca.crt")
caCertPool := x509.NewCertPool()
caCertPool.AppendCertsFromPEM(caCert)

netConn, req, err := wlgows.Dial("wss://localhost:8001", &tls.Config{RootCAs: caCertPool})
```

### HTTP Hijacking

```go
func handler(w http.ResponseWriter, r *http.Request) {
	hijacker, ok := w.(http.Hijacker) // Gin 之下，c.Writer 也是
	if !ok {
		return
	}
	netConn, bufRW, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer netConn.Close()

	// net/http 已經解析過 r(*http.Request)，不需要再讀一次。要傳 bufRW.Reader：
	// net/http 可能已經把第一個 frame 的開頭讀進去了。
	// bufRW.Writer 沿用 net/http 的 writer；bufio.NewWriterSize(netConn, size) 也可以。
	conn, _, err := wlgows.ServerHandShake(netConn, bufRW.Reader, bufRW.Writer, r)
	if err != nil {
		return
	}
	// ... read and send
}
```

## Examples

共有六個範例。三個 server 是同一支 echo 程式，只是以三種不同方式接入，所以互動式
client 可以連到其中任何一個；streaming 範例則自成一組：

| Example | |
|---|---|
| [`echo`](./example/echo/main.go) | 在 raw TCP 上執行的 echo server，使用 `wlgows.Run` 與 `Accept` |
| [`hijack_http`](./example/hijack_http/main.go) | 同一支 server，改掛在 `net/http` 之下，使用 `http.Hijacker` |
| [`hijack_gin`](./example/hijack_gin/main.go) | 同一支 server，改掛在 Gin 之下，使用 `c.Writer.Hijack()`。`GET /ping` 依然正常回傳 JSON，與 WebSocket 並存 |
| [`client`](./example/client/main.go) | 互動式 client。輸入一行文字即送出，輸入 `exit` 會正常關閉連線。啟動時會詢問 CA 路徑，因此也能連線 `wss://` |
| [`stream_server`](./example/stream_server/main.go) | 逐一接收 streaming message 的 frame 並直接寫入磁碟。使用 `Data` hook，適合大到無法放進記憶體的 message |
| [`stream_client`](./example/stream_client/main.go) | 將一個檔案當作單一 binary message 串流送出，一次一個 chunk |

範例是獨立的 module，所以要在 `example/` 底下執行：

```bash
cd example
go run ./echo      # 或 hijack_http、hijack_gin
go run ./client    # 另開一個終端機，然後開始輸入
```

streaming 範例是獨立的示範，兩端會各自印出 SHA-256，而且兩者會一致：

```bash
cd example
go run ./stream_server
go run ./stream_client   # 另開一個終端機，按兩次 Enter 就會傳送 README.md
```

兩個 hijack server 啟動時都會詢問憑證與金鑰的路徑：直接按兩次 Enter 會以一般的
`ws://` 提供服務，輸入路徑則以 `wss://` 提供服務。

每個範例都使用了 [`Listener`](./LISTENER_README.zh-TW.md)，所以它們會回應 ping、
完成 close handshake、擋下 RFC 6455 不允許的 frame，而這些邏輯都不會出現在範例的
程式碼裡。每個檔案留下的，就只有你本來就要寫的部分：hook。

## 從 v6 升級

v7 直接從你的 `bufio.Reader` 讀出 frame header，所以讀一個 frame 只會配置 `Frame`
和它的 payload；Listener 的上限也依照它們實際計算的東西重新命名。`Conn` 的程式碼不用
改。直接呼叫 frame reader 的地方，以及用到改名項目的程式碼，會編譯失敗：

| v6 | v7 |
|---|---|
| `go get .../wlgows/v6` | `go get github.com/weilun-shrimp/wlgows/v7` |
| `wlgows.GetFrameFromReader(netConn, max)` | `wlgows.GetFrameFromReader(r, max)`，其中 `r := bufio.NewReader(netConn)`。每條連線只用同一個 `r`：它保存著已經讀進來的 bytes。 |
| `wlgows.ReadFromReader(r, length)` | 已移除：`buffer := make([]byte, length)`，再 `io.ReadFull(r, buffer)` |
| `ListenerConfig{MaxMsgFrameCount: n}` | `ListenerConfig{MaxDataFrameCount: n}` |
| `ListenerConfig{MaxMsgPayloadByteLen: n}` | `ListenerConfig{MaxDataFramesSize: n}` |
| `wlgows.ErrMsgFrameCountExceeded` | `wlgows.ErrDataFrameCountExceeded` |
| `wlgows.Frames`、`func(frames wlgows.Frames)` hook | `wlgows.DataFrames`、`func(frames wlgows.DataFrames)` |

不會出現編譯錯誤、但行為改變的地方：

- **`io.EOF` 現在只代表 stream 在兩個 frame 之間結束。** 如果 stream 在 frame 的第一個
  byte 之後才結束，會回傳 `io.ErrUnexpectedEOF`。在 v6 中，frame 剛好在前 2 個 bytes
  之後、或剛好在 header 之後被截斷時，回傳的是 `io.EOF`。
- **64 bit 長度的最高位元（most significant bit）為 1 時，會回傳
  `ErrPayloadLengthMSBSet`。** RFC 6455 5.2 禁止這個位元為 1。
  `StandardClosePayloadFor` 已經把它對應到 1002，所以像範例那樣送出它回傳值的迴圈，
  不需要任何修改。在 v6 中，這樣的 frame 會讓 `make` panic。
- **`MaxDataFramesSize` 現在連 frame header 也一起計算。** 每個 data frame 除了
  payload，也會扣掉它 2 到 14 byte 的 header，空的 continuation frame 也一樣。在 v6
  中只計算 payload，所以原本剛好放得下的 message 現在可能會被拒絕。請在你接受的最大
  message 之上多留一點空間。
- **空的 continuation frame 不再被丟掉。** `Text`、`Binary` 和 `Data` 會依抵達順序
  收到這個 message 的每一個 frame，每一個也都會算進 `MaxDataFrameCount`。在 v6 中，
  message 中間的空 frame 會被直接略過。
- **`TransmitData` 會把空的 chunk 以空的 frame 送出。** 在 v6 中它會被略過。如果你不想
  送出那個 frame，請像 streaming 範例那樣先檢查長度。

## Design

**單一 package。** 所有功能都在根目錄的 `wlgows` package 中：

```go
import "github.com/weilun-shrimp/wlgows/v7"

netConn, req, _ := wlgows.Dial(url, nil)
s, _             := wlgows.Run(":8001")
```

**`Conn` 必須透過 constructor 建立。** 它包含未匯出的相依欄位，若自行撰寫 struct
literal，第一次使用時就會 panic。`ClientHandShake` 與 `ServerHandShake` 已經處理好
這件事：先用 `Dial`、`Server.Accept` 或 `http.Hijacker` 建立連線（或接受連線、
hijack），再呼叫其中一個函式取得設定完成的 `Conn`。只有直接建立 `Conn` 時需要
特別注意：

```go
c, err := wlgows.NewConn(netConn, r, bufio.NewWriter(netConn), false) // false：server 不做 mask（5.1）
```

error 只會來自小於 14 byte 的 writer：它在被換掉之前會先 flush，而那次 flush 可能
失敗。

最後一個參數對應 RFC 6455 5.1，取決於你是哪一端：client 送出的每個 frame 都必須
mask，server 則一律不 mask，對方收到不符規定的 frame 會直接中斷連線。這個值在建立
時就決定好，因此之後任何傳送都不可能設錯。`ClientHandShake` 與 `ServerHandShake`
會自動幫你填入。

`Server` 匯出的欄位（`TCPAddr`、`TCPListener`）可以讀取，也可以修改。

## 記憶體 vs. 速度：自己決定 buffer 的大小

`Conn` 持有兩塊 buffer：你交給它的 `*bufio.Reader` 與 `*bufio.Writer`。兩者都是固定的，也都是用每條連線的記憶體換取較少的 syscall。

### Reader

上面每個範例使用的 `bufio.NewReader(netConn)`，預設會為每條連線配置 4096 byte 的
buffer。`ClientHandShake`、`ServerHandShake` 與 `NewConn` 都不限制這個大小，你傳入
什麼樣的 `*bufio.Reader` 就使用什麼。如果願意犧牲一點速度，換取每條連線大幅減少的
記憶體用量，只要自行指定大小即可：

```go
r := bufio.NewReaderSize(netConn, 16) // 16 是下限，小於 16 會被 bufio 調整為 16
```

下限 16 並非隨意決定：frame header（2 byte）加上最長的 extended length（8 byte）
再加上 masking key（4 byte）共 14 byte，剛好小於 16。因此無論 buffer 設多大，
frame 中 payload 以外的部分都能一次讀完。buffer 大於 16 的好處，在於較小的
payload 可以跟 header 在同一次讀取中一起進來；一旦 payload 超過 buffer 的剩餘
空間，無論 buffer 多大都需要另外讀取，所以 message 越大，兩者的差距就越小。

記憶體用量會隨同時連線數增加：

| | 16 byte | 4096 byte（預設） |
|---|---|---|
| 每條連線 | 16 B | 4096 B |
| 1,000 條連線 | 約 16 KB | 約 4 MB |
| 100,000 條連線 | 約 1.6 MB | 約 400 MB |

讀取次數則會隨 frame 大小與連續到達的數量增加：

| | 16 byte | 4096 byte（預設） |
|---|---|---|
| 一個 10 B 的 frame | 1 次讀取 | 1 次讀取 |
| 連續 100 個 10 B 的 frame（共 1000 B） | 約 63 次讀取 | 最少 1 次讀取 |
| 一個 1 MB 的 frame | payload 不經過 buffer，兩者讀取次數相同 | payload 不經過 buffer，兩者讀取次數相同 |

（100 個 frame 那一列假設呼叫 `Read` 時資料都已到達，這是穩定串流下的常態；如果
對方傳送得很慢或斷斷續續，差距就會縮小。）

### Writer

`bufio.NewWriter(netConn)` 同樣是 4096 byte，用同樣的方式自行指定大小即可。下限是
14，也就是最長的 frame header：更小的 writer 會被 flush，再換成 14 byte 的。

```go
w := bufio.NewWriterSize(netConn, 14) // 每條連線 14 byte
conn, _, err := wlgows.ServerHandShake(netConn, r, w, req)
```

取捨相同，只是方向相反：放得進 buffer 的小 frame 會共用一次 socket 寫入。在 server
上，大的 payload 會略過 buffer，所以 14 byte 的代價很小；client 則要在 buffer 裡
加上 mask，所以會送出大 message 的 client 需要大的 buffer。
`conn.RenewWriter(w)` 隨時可以更換。實測的寫入次數見
[記憶體與 write buffer](./SENDING_README.zh-TW.md#記憶體與-write-buffer)。

### 最小的連線

以一條閒置連線實測：一個 `Conn`、它的 `NewStandardListener`，以及執行 `Listen` 的
goroutine。不包含 `net.Conn` 本身與作業系統的 socket。

| | 每條閒置連線 | 100,000 條連線 |
|---|---|---|
| Reader 4096、writer 4096 | 約 14 KB | 約 1.4 GB |
| Reader 16、writer 14 | 約 5–6 KB | 約 550 MB |
| Reader 16、writer 14，再加上 `StartPingLoop` | 約 9 KB | 約 900 MB |

用到最小的大小時，buffer 只剩 30 byte，剩下的大多是 goroutine 的 stack：`Listen`
約 4 KB，每個 `StartPingLoop` 再多約 3 KB。wlgows 沒辦法縮小這些。你可以做的是：

- **連線安靜時換成小的 writer。** 如果你知道這條連線有一陣子不會送任何東西，
  `conn.RenewWriter(bufio.NewWriterSize(conn, 14))` 會把大的 buffer 還回去；下一波
  傳送之前，再換回大的 writer 即可。每次更換都會 flush，並配置新的 writer，所以請在
  安靜開始時換，不要在 message 之間換。hijack 之後，從 Go 1.25 起，net/http 會一直
  持有 `bufRW.Writer`，直到 handler 返回，所以請在另一個 goroutine 裡執行
  WebSocket，並讓 handler 先返回；這樣換掉之後它就會被回收。那個 goroutine 不能
  保留 `http.ResponseWriter`，因為它也持有這個 writer。
- **用一個 goroutine 送 ping，而不是每條連線一個。** `SendPing` 可以從任何
  goroutine 呼叫，所以你自己的一個 loop 就能對所有連線送 ping。對方卡住時寫入會讓
  這個 loop 跟著卡住，所以請替每條連線設定 write deadline。
- **省掉每則 message 的複製。** 使用 `unsafe` 時，一則 message 只會存在一份，而不是
  兩份 —— 見 [傳送](./SENDING_README.zh-TW.md#更快更省記憶體用-unsafe-送出-string)與
  [讀取](./LISTENER_README.zh-TW.md#不複製地讀取-textunsafe)。
- **用串流接收大的 message。** `Data` hook 一次只持有一個 frame，而不是整則 message
  —— 見 [LISTENER_README.zh-TW.md](./LISTENER_README.zh-TW.md#hooks)。

## Benchmark 比較

與 [gorilla/websocket](https://github.com/gorilla/websocket) v1.5.3 和
[gobwas/ws](https://github.com/gobwas/ws) v1.4.0 比較，使用相同的案例，每條連線都有
4096 byte 的 reader 與 writer：

| 項目 | 對 gorilla | 對 gobwas |
|---|---|---|
| server 送出 | 每種大小都**勝** | 批次送出**勝**，單一 frame 平手 |
| 送出的記憶體 | **勝**：永遠 0 次配置 | **勝**：0 次配置 vs 每個 frame 1 次 |
| client 送出 | 小 frame 落後 | 落後，為了安全而刻意如此 |
| 讀取 | **勝**：快 1.5 到 3.4 倍 | server 小 frame **勝**，client 單一 frame 平手，其餘落後 |

client 送出的落後是刻意的。masking key 來自 `crypto/rand`，從不用 `math/rand`；
你的 payload 只會被讀取，從不原地加 mask。這兩者都是量測過、但為了
[正確性優先](#wlgows)而放棄的更快做法。

環境、方法、所有數字，以及每項落後為什麼保留：
**[BENCHMARK_COMPARISON.zh-TW.md](./BENCHMARK_COMPARISON.zh-TW.md)**。

## Reading

有兩種方式，差別在於你想自己處理多少細節。

**`Listener`** 負責讀取、依 RFC 6455 驗證、將分段的 message 組合起來，再依 opcode
呼叫對應的 hook。它本身從不寫入、也從不關閉連線；RFC 對接收端的每一項要求，最終
都交由某個 hook 處理。`SetConfig` 以複製的方式整份替換設定，隨時都可以呼叫，包括
在 hook 內部：

```go
listener := conn.NewStandardListener() // 或 wlgows.NewListener(conn)，不含任何 hook

config := listener.GetConfig()
config.MaxDataFramesSize = 10 * 1024 * 1024
config.Text = func(frames wlgows.DataFrames) { log.Print(frames.String()) }
listener.SetConfig(config)

err := listener.Listen()
```

`NewStandardListener` 跟一般的 Listener 相同，只是已經處理好協定本身規定的部分
（ping、pong、close 與保留 opcode），而且 `PeerIsClient` 會依連線端別自動設定。
這些 hook 每一個都可以替換。

詳細內容請見 **[LISTENER_README.zh-TW.md](./LISTENER_README.zh-TW.md)**，包括 hook、
各項上限、暫停，以及每個 error 的意義。

**`GetNextFrame`** 一次回傳一個 frame，由你自行組合：

```go
var frames wlgows.DataFrames
for {
	f, err := conn.GetNextFrame(10 * 1024 * 1024) // 0 表示不限制
	if err != nil {
		return err
	}
	frames = append(frames, f)
	if f.FIN {
		break
	}
}
```

一次處理一個 frame，才能把超大的 message 直接串流到其他地方，而不必整個放在記憶體
裡。`DataFrames` 只在你需要時才組合：

| 呼叫 | 回傳 |
|---|---|
| `frames.String()` | 串接後的 payload，型別為 `string` |
| `frames.Bytes()` | 串接後的 payload，是一份新的 `[]byte`，可自由使用 |
| `frames.ByteLen()` | 串接後的大小，單位為 **byte**，完全不配置記憶體 |

這兩個組合方法都會把所有 fragment 串接起來，所以即使多 byte 的 rune 被 frame 邊界
切開，也能完整還原。`ByteLen` 計算的是 byte 數而非字元數：`"中文字"` 會回傳 9，
而不是 3。

**提示：`unsafe` 可以省掉複製。** `frames.String()` 會複製整則 message。如果
message 只有一個 frame，`unsafe` 可以不複製就轉成 string：1 MB 的 message 只要約
2 ns，而不是 60 µs，記憶體也省一半。只有在你清楚自己在做什麼時才使用 —— 見
[不複製地讀取 text](./LISTENER_README.zh-TW.md#不複製地讀取-textunsafe)。

opcode 只存在於第一個 frame，continuation frame 的 opcode 是 0：

```go
switch frames[0].Opcode {
case wlgows.OpcodeText:   // 0x1
case wlgows.OpcodeBinary: // 0x2
case wlgows.OpcodeClose:  // 0x8：停止讀取
case wlgows.OpcodePing:   // 0x9
case wlgows.OpcodePong:   // 0xA
}
```

## Sending

傳送有獨立的說明文件：**[SENDING_README.zh-TW.md](./SENDING_README.zh-TW.md)** ——
該用哪個呼叫、streaming、記憶體管理、失敗處理與 lock。

| 你手上有的是 | 呼叫 |
|---|---|
| 一則已在記憶體中的 message | `SendText(text, chunkSize)`、`SendBinary(data, chunkSize)`、`SendData(opcode, payload, chunkSize)` |
| 一則大到放不進記憶體的 message | `StartLongDataTransmission` → `TransmitData` → `EndLongDataTransmission`，並 defer `ReleaseLongDataTransmission` |
| 一個 close、ping 或 pong | `SendClose(payload)`、`SendPing(payloadData)`、`SendPong(payloadData)` |
| 一個你自行組出的 frame | `SendFrame(frame)` |

```go
conn.SendText([]byte("hello"), 0)  // 一個 frame
conn.SendBinary(data, 4*1024)      // 每個 frame 的 payload 為 4 KB
```

`chunkSize` 是每個 frame 的 payload 大小。0 或以下代表以一個 frame 送出。

**掌控權在你手上，所以記憶體由你管理。** 大多數 package 會在建立連線時就固定
frame 大小；wlgows 則讓每一次傳送自己決定，每條連線也有自己的 write buffer。buffer
就是你傳入的 `bufio.Writer`，大小固定 —— 不會隨你送出的內容變大 —— 而
`conn.RenewWriter(w)` 隨時可以更換。見
[記憶體與 write buffer](./SENDING_README.zh-TW.md#記憶體與-write-buffer)。

**你的 payload 只會被讀取。** client 是對一份複本加上 mask，從不動你的 slice，所以
把同樣的 byte 送兩次，或送出你之後還要用的 byte，都是安全的。

**提示：`unsafe` 可以省掉複製。** `[]byte(text)` 會複製整個 string。因為 wlgows 只會
讀取你的 payload，`unsafe` 可以不複製就送出 string：1 MB 的 message 可以省下約 1 MB
與 60–90 µs。只有在你清楚自己在做什麼時才使用 —— 見
[更快、更省記憶體](./SENDING_README.zh-TW.md#更快更省記憶體用-unsafe-送出-string)。

**close 之後不送 data。** close 送出之後，data 傳送與第二個 close 都會回傳
`ErrCloseAlreadySent`（5.5.1）。ping 或 pong 仍然會送出（5.5.2）。

## Keepalive

已經斷線的對方和只是沒有傳送資料的對方，從這一端看起來完全一樣。RFC 6455 5.5.2
以 ping 發出詢問、以 pong 作為回應，所以確認對方是否還在，需要兩個部分：一是定期
發送 ping，二是判斷收到的 pong。

`StartPingLoop` 負責定期發送。它會 block，所以要在哪個 goroutine 執行由你決定：

```go
go conn.StartPingLoop(30*time.Second, nil)   // nil：不帶 payload 的 ping
```

它會自動停止：ping 送不出去時，或任一端已送出 close 之後。它不會重試，所以送失敗
的那個 ping 就是最後一個，你也不需要保存任何 handle。

`payload` 會在每次 ping 時呼叫一次，而不是只呼叫一次。5.5.2 規定對方必須原封不動
地回傳這些 byte，所以讓每次的內容不同，就能分辨收到的 pong 對應哪一個 ping：

```go
var seq atomic.Uint64
go conn.StartPingLoop(30*time.Second, func() []byte {
	return []byte(fmt.Sprintf("ping-%d", seq.Add(1)))
})
```

pong 會帶回同樣的 `ping-4`，所以 `Pong` hook 能判斷它回應的是哪一個 ping。對協定
來說 payload 只是一串 byte，可以是序號、時間戳記，或任何你能在收到時辨認的內容。

**判斷回應需由你自行處理**，這部分函式庫不會代勞。在 `Pong` hook 中記錄時間，與
你自訂的期限比較，超過就關閉連線：

```go
config.Pong = func(*wlgows.Frame) { lastPong.Store(time.Now()) }
```

記錄時間時不要比對 payload。5.5.3 允許對方主動送出 pong，5.5.2 也允許對方在有多個
ping 尚未回應時只回應最新的那一個，所以即使收到的 pong 對不上你送過的任何 ping，
也足以證明對方仍在線上。

底層的 `Loop` 也有匯出，可用於任何週期性工作：它會定期呼叫傳入的 func，直到該 func
表示要停止為止：

```go
wlgows.Loop(func(stop chan<- struct{}) {
	if done() {
		stop <- struct{}{}   // 只送一次；func 回傳後就會檢查
		return
	}
	work()
}, time.Second)
```

## Concurrency

`Conn` 持有三個 `sync.Locker`，預設都是 `*sync.Mutex`：

| Locker | 保護對象 | 使用者 |
|---|---|---|
| `writeLocker` | write buffer，同一時間一個 frame | 每個 `Send*`，持有時間為傳送一個 frame；`RenewWriter` |
| `dataFramesWriteLocker` | 同一時間只有一個 data message | `SendText`、`SendBinary`、`SendData`，以及 `Start`…`Release` |
| `readLocker` | 同一時間只有一個讀取者 | `GetNextFrame` |

**可以安全地從多個 goroutine 傳送。** 之所以用兩個 lock 而不是一個，是為了避免
pong 被卡在 10 GB 的傳輸後面：control frame 只取得 `writeLocker`，所以能插在
fragment 之間傳送，這正是 5.4 明確允許、5.5.2 所需要的。

**請只用一個 goroutine 讀取。** `readLocker` 能避免兩個讀取者同時讀取同一段
byte，但它們仍然會各自讀到不完整的 frame。

**`conn.Write` 與 `conn.Read` 會略過所有 lock**，因為它們是從內嵌的 `net.Conn`
直接繼承而來。傳送請使用 `Send*` 系列方法。

`Close` 刻意不取得任何 lock：關閉 fd 正是解除卡在已斷線對方上的讀寫動作的方法。

## Errors

每個 error 都以 `%w` 包裝一個 package 層級的 sentinel error，所以請用 `errors.Is`
比對，不要用 `==`：

```go
if _, _, err := wlgows.ServerHandShake(netConn, r, w, req); errors.Is(err, wlgows.ErrHttpMethodNotAllowed) {
	// ...
}
```

`StandardClosePayloadFor` 會將 `Listen` 回傳的 error 對應到 RFC 6455 7.4.1 規定的
close code：UTF-8 無效為 1007，message 超過上限為 1009，其他協定錯誤為 1002；
無法以 close frame 回應的情況則回傳 `nil`：

```go
if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
	conn.SendClose(payload)
}
```

`nil` 的意思是「無法判定是哪一方的問題」，而不是「請關閉連線」。你自己的 error
也會落在這一類：`PauseListen(err)` 會結束這一輪執行，`Listen` 就回傳這個 error；
如果這一輪讀取是因為關閉連線而結束，它還會和 socket 本身的 error 合併後一起回傳。
這些情況一樣會得到 `nil`，因為這個 package 無法替它不認識的規則決定 close code。
如有需要，請自行先做對應。詳見
[LISTENER_README.zh-TW.md](./LISTENER_README.zh-TW.md) 的 Errors 章節。

## Testing

```bash
go test ./...                              # 全部
go test -race ./...                        # integration test 會啟動 goroutine
go test -cover .                           # 98.3% of statements
go test -bench BenchmarkDataFramesAssembly .   # benchmark，一般的 go test 不會執行
```

每個原始檔都有對應的 `_test.go`，全部都在 package `wlgows` 內，才能存取 `di`
注入點。有兩個檔案沒有對應的原始檔：

| 檔案 | 內容 |
|---|---|
| [`Fakes_test.go`](./Fakes_test.go) | 共用的測試替身：`fakeConn`（在記憶體中運作的 `net.Conn`）、`fakeIOWriter`/`fakeIOReader`（單純的 `io.Writer`/`io.Reader` 替身）、`fakeFuncLocker`（在 `Lock` 與 `Unlock` 時執行一個 func，讓測試把它們和其他步驟記錄在一起）、`fixedRandRead`、`fakeGetFrameFromReaderBufioReader`（為 frame reader 模擬 `Peek` 與 `Discard`） |
| [`ListenerIntegration_test.go`](./ListenerIntegration_test.go) | `Listen` 在真正的 `net.Pipe` 上端對端執行，**不**替換任何 `di` |

integration test 能抓到 unit test 在結構上無法發現的串接錯誤，例如 constructor
漏填某個 `di` 欄位，或某個預設值指向錯誤的 function。

`BenchmarkDataFramesAssembly` 說明了為什麼處理 70000 byte 的 message 時應該用
`Bytes()`，而不是 `[]byte(String())`：

```
[]byte(String())   10774 ns/op   147458 B/op   2 allocs/op
Bytes()             5722 ns/op    73729 B/op   1 allocs/op
```

時間減半、記憶體也減半：中間那個 `string` 只是為了再轉換一次而存在，完全是多餘的。

### 替換相依

每個會做 I/O 或用到隨機數的 function，都是一層很薄的匯出包裝，底下實作則接收一個
`di` struct（內含 function value）；type 則把相依放在由 constructor 填入的 `di`
欄位中。這些都沒有匯出，所以只有 package 內的測試能存取。純函式（`MaskPayload`、
`DataFrames.String`、`ValidateHandShakeRequest`…）沒有注入點，直接測試即可。

```go
// package 層級的 func：傳入 di struct
netConn, req, err := dial("ws://localhost:8001", nil, dialDI{...})

// method：覆寫 constructor 設定好的欄位
conn, _ := NewConn(netConn, bufio.NewReader(netConn), bufio.NewWriter(netConn), false)
conn.di.newDataFrame = func(config NewFrameConfig) (*Frame, error) { ... }
```

固定隨機來源後，才能驗證 mask 過的輸出；否則 masked frame 的內容每次都不同，無法
寫出固定的預期值：

```go
f := NewFrame(NewFrameConfig{PayloadData: []byte("hi"), Opcode: 1, FIN: true})
prepareSendFrame(f, true, prepareSendFrameDI{
	fillMaskingKey: func(key *[4]byte) error { *key = [4]byte{1, 2, 3, 4}; return nil },
})
// f.appendSealedHeader(nil) == []byte{0x81, 0x82, 1, 2, 3, 4}
```

有一個測試刻意驗證的是「目前的行為」而非「正確的行為」，並在名稱與註解中清楚說明：

- `TestDial/an_unknown_scheme_yields_a_nil_conn_and_no_error`：`dial` 中的 scheme
  switch 沒有 default 分支。正式環境不會走到這裡，因為前面有
  `ValidateWebsocketUrl` 把關；只有使用較寬鬆的 fake 時才會觸發。

測試覆蓋率的缺口，是只把欄位綁進 `di` struct 的那些很薄的 `Conn` 包裝（`SendText`、
`SendData`、`TransmitData`、`RenewWriter`…），它們的邏輯已透過各自包裝的純函式測到；
以及 `ClientHandShake` 回傳 `Conn` 的那一行。它的 `NewConn` error 則在 `net.Pipe`
上測試，由一個 goroutine 扮演 server 回應，因為真正的 client 每次呼叫都會用
`crypto/rand` 產生新的 key。

## License

MIT
