[English](./README.md) · **繁體中文**

# WLGOWS

一個簡單、直覺、而且夠強大的 Go WebSocket library —— 上手很快，也對底下的協定
誠實。

下面 Quick Start 貼上去就是一支會跑的 echo server：一個 constructor、一個 hook，
再加上 `conn.NewStandardListener()` 幫你回好 ping、pong 和 close。等你需要更多
時，該有的全都在 —— 串出比記憶體還大的 message、自己接手每一個 data frame、照你
自己的節奏 ping，或是純手工組一個 frame 送出去。沒有任何東西被藏起來，因為它用
frame 而不是抽象層來運作：server 與 client、手動 handshake，RFC 6455 要求你負責
的每一條規則，都留在你看得到的地方。

## Features

- **WebSocket Server** — 直接跑在 raw TCP 上，搭配 HTTP handshake
- **WebSocket Client** — `ws://` 與 `wss://`
- **TLS/SSL** — 兩端都支援
- **HTTP Hijacking** — `http.Server` 或 Gin
- **Listener** — 一個 read loop，依 RFC 6455 驗證每個 frame，再送到你的 hook
- **Frame-level control** — 需要的時候，自己組 frame 自己送
- **Streaming** — 送出比記憶體還大的 message，一個 fragment 一個 fragment 送
- **Keepalive** — 定期 ping，每次的 payload 由你決定
- **Concurrent sending** — 有 lock 保護，而且 control frame 永遠不會卡在長
  message 後面

## Contents

- [Quick Start](#quick-start) — [Server](#server) · [Client](#client) · [TLS](#tls-wss) · [HTTP Hijacking](#http-hijacking)
- [Examples](#examples) — 三個 echo server、一個 client，還有 streaming 一組兩支
- [從 v3 過來](#從-v3-過來)
- [Design](#design)
- [記憶體 vs. 速度](#記憶體-vs-速度自己決定-reader-的大小) — 自己決定 reader 的大小
- [Reading](#reading) — 用 `Listener`，或一次讀一個 frame
- [Sending](#sending) — 整個 message、control frame、streaming、自組 frame
- [Keepalive](#keepalive) — 定期 ping，以及怎麼發現對方不出聲了
- [Concurrency](#concurrency) — 哪個 lock 保護什麼
- [Errors](#errors) — sentinel error 與 `StandardClosePayloadFor`
- [Testing](#testing)

`Listener` 有自己的一份說明：**[LISTENER_README.zh-TW.md](./LISTENER_README.zh-TW.md)**。

## Installation

```bash
go get github.com/weilun-shrimp/wlgows/v4
```

import path 帶著 Go 對 major version 2 以上要求的 `/v4` 後綴；package 名稱仍然是
`wlgows`，所以呼叫端寫的還是 `wlgows.Dial(...)`。

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

	"github.com/weilun-shrimp/wlgows/v4"
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

	// Accept 只負責拿 TCP 連線 —— 讀 request、跑 handshake 都是你的事，這樣
	// 才不會有任何 byte 是在你看不到的地方上線的。
	r := bufio.NewReader(netConn) // 每條連線 4096 byte；想縮小看「記憶體 vs. 速度」
	req, err := http.ReadRequest(r)
	if err != nil {
		return
	}
	conn, _, err := wlgows.ServerHandShake(netConn, r, req)
	if err != nil {
		return
	}

	// ping、pong、close 和保留 opcode 都已經回答好了，masking 也依這條連線是哪
	// 一側決定完成。
	listener := conn.NewStandardListener()

	// SetConfig 會整份換掉，所以從標準 hook 留下的那份開始改。
	config := listener.GetConfig()
	config.MaxMsgPayloadByteLen = 10 * 1024 * 1024
	config.Text = func(frames wlgows.Frames) {
		conn.SendText(frames.Bytes()) // echo
	}
	listener.SetConfig(config)

	// 心跳，跑在自己的 goroutine 上。連線結束時它會自己結束 —— 要偵測「有回
	// pong 但一直不回」的對端，看 Keepalive。
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

形狀和 server 一樣。masking 反過來 —— server 送出的 frame 不 mask —— 但這裡不
需要寫出來：`Conn` 知道自己是哪一側，`NewStandardListener` 直接沿用。

```go
package main

import (
	"bufio"
	"log"
	"time"

	"github.com/weilun-shrimp/wlgows/v4"
)

func main() {
	// Dial 只負責連線、組好 request —— 跑 handshake 是你的事，所以你還是可以
	// 在送出前替 req 加 header。
	netConn, req, err := wlgows.Dial("ws://localhost:8001", nil)
	if err != nil {
		panic(err)
	}
	defer netConn.Close()

	r := bufio.NewReader(netConn) // 每條連線 4096 byte；想縮小看「記憶體 vs. 速度」
	conn, _, err := wlgows.ClientHandShake(netConn, r, req)
	if err != nil {
		panic(err)
	}

	listener := conn.NewStandardListener()

	config := listener.GetConfig() // 標準 hook 留下的那份
	config.MaxMsgPayloadByteLen = 10 * 1024 * 1024
	config.Text = func(frames wlgows.Frames) {
		log.Println("received:", frames.String())
	}
	listener.SetConfig(config)

	go conn.StartPingLoop(30*time.Second, nil) // 心跳；會自己結束

	conn.SendText([]byte("Hello, WebSocket!"))

	if err := listener.Listen(); err != nil {
		if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
			conn.SendClose(payload)
		}
		log.Println("closing:", err)
	}
}
```

masking 兩端都不用操心：`ClientHandShake` 和 `ServerHandShake` 在組出回傳的
`Conn` 時就會設好 `maskSendFrame`，所以 client 的 `SendText`、`SendPong` 會
mask，而 server 的不會。

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
	netConn, bufReader, err := wlgows.HijackFromHttp(w) // 或 HijackFromGin(c)
	if err != nil {
		return
	}
	defer netConn.Close()

	// r 已經被 net/http 解析過了，不需要再讀一次。
	conn, _, err := wlgows.ServerHandShake(netConn, bufReader, r)
	if err != nil {
		return
	}
	// ... read and send
}
```

## Examples

六支。三個 server 是同一支 echo 程式、用三種方式接上來，所以互動式 client 可以
打任何一個，而 streaming 那組自己跑自己的：

| Example | |
|---|---|
| [`echo`](./example/echo/main.go) | 跑在 raw TCP 上的 echo server —— `wlgows.Run` 與 `Accept` |
| [`hijack_http`](./example/hijack_http/main.go) | 同一支 server，改掛在 `net/http` 後面，用 `HijackFromHttp` |
| [`hijack_gin`](./example/hijack_gin/main.go) | 同一支 server，改掛在 Gin 後面，用 `HijackFromGin`。`GET /ping` 仍然照常回 JSON，與 WebSocket 並存 |
| [`client`](./example/client/main.go) | 互動式 client。打一行就送出，`exit` 會乾淨地關閉。會問 CA 路徑，所以也講得了 `wss://` |
| [`stream_server`](./example/stream_server/main.go) | 一個 frame 一個 frame 收下 streaming message 並直接寫進磁碟 —— 用 `Data` hook，給大到放不進記憶體的 message |
| [`stream_client`](./example/stream_client/main.go) | 把一個檔案當成一個 binary message 串出去，一次一個 chunk |

```bash
go run ./example/echo      # 或 hijack_http、hijack_gin
go run ./example/client    # 開另一個終端機，然後開始打字
```

streaming 那組是自成一體的示範 —— 兩端各自印出 SHA-256，而且會一致：

```bash
go run ./example/stream_server
go run ./example/stream_client   # 另一個終端機，按兩次 enter 就會送 README.md
```

兩個 hijack server 啟動時都會問憑證與金鑰路徑 —— 按兩次 enter 就是純 `ws://`，
給路徑則會服務 `wss://`。

每一支都用了 [`Listener`](./LISTENER_README.zh-TW.md)，所以它們會回 ping、會把 close
handshake 收尾、會擋掉 RFC 6455 不允許的 frame，而這些都不會出現在 example 的程
式碼裡。每個檔案剩下來的，就是你自己本來就要寫的那部分：hook。

## 從 v3 過來

`ClientConn` 和 `ServerConn` 沒有了。現在只有一個 `Conn`，兩側共用，而且它不
帶任何 handshake 狀態 —— handshake 是一次函式呼叫，不是存在
`ClientRequest`/`ServerResponse` 裡的東西。

| v3 | v4 |
|---|---|
| `conn, err := wlgows.Dial(url, tls)` 再 `conn.HandShake()` | `netConn, req, err := wlgows.Dial(url, tls)` 再 `conn, res, err := wlgows.ClientHandShake(netConn, r, req)` |
| `conn, err := server.Accept()` 再 `conn.HandShake()` | `netConn, err := server.Accept()`，自己讀 request，再 `conn, res, err := wlgows.ServerHandShake(netConn, r, req)` |
| `sc, err := wlgows.HijackFromHttp(w, r)` 再 `sc.HandShake()` | `netConn, r, err := wlgows.HijackFromHttp(w)` 再 `conn, res, err := wlgows.ServerHandShake(netConn, r, req)` |
| `wlgows.NewClientConn(c, req)` / `wlgows.NewServerConn(c, req)` | 沒有了 —— 用上面的 handshake 函式組出 `Conn`，或直接 `wlgows.NewConn(c, r, maskSendFrame)` |
| `conn.ClientRequest` / `conn.ServerResponse` | 沒有了 —— handshake 函式直接把 response 回傳給你，不再存起來 |

拆成兩步是為了 `*bufio.Reader`：`http.ReadRequest` 和 `http.ReadResponse` 可能
會多讀進一些 header 區塊之後的 byte —— 也就是第一個 frame 的開頭 —— 所以
handshake 跟之後讀 frame 用的必須是同一個 reader。`ClientHandShake`/
`ServerHandShake` 把它收成一個參數，並用它組出回傳的 `Conn`，所以這件事再也
不會不小心弄錯。`Dial`/`Server.Accept`/`HijackFromHttp` 保持很薄 —— 連線（或
接受、或 hijack）之後就把原始材料交給你 —— 所以 handshake 什麼時候真正跑，還
是由你決定，送出前也還是能替 request 加 header。

## Design

**單一 package。** 全部都在根目錄的 `wlgows` package：

```go
import "github.com/weilun-shrimp/wlgows/v4"

netConn, req, _ := wlgows.Dial(url, nil)
s, _             := wlgows.Run(":8001")
hjConn, r, _     := wlgows.HijackFromHttp(w)
```

**`Conn` 一定要用 constructor 建。** 它帶著未匯出的相依欄位，所以手寫 struct
literal 會在第一次使用時 panic。`ClientHandShake` 和 `ServerHandShake` 已經幫
你做對了 —— 先用 `Dial`/`Server.Accept`/`HijackFromHttp` 連上（或接受、或
hijack），再跑其中一個拿到組好的 `Conn`；只有直接建構 `Conn` 時要留意：

```go
c := wlgows.NewConn(netConn, r, false) // false：server 不 mask（5.1）
```

最後那個參數就是 RFC 6455 5.1，取決於你是哪一側：client 送出的每個 frame 都要
mask，server 一個都不 mask，而對端遇到錯的那種會直接讓連線失敗。它在建構時就決
定好，所以沒有任何一個送出的呼叫能傳錯。`ClientHandShake` 和 `ServerHandShake`
會幫你填。

`Server` 匯出的欄位（`TCPAddr`、`TCPListener`）可讀可寫。

## 記憶體 vs. 速度：自己決定 reader 的大小

`bufio.NewReader(netConn)` —— 上面每個範例用的都是它 —— 每條連線預設吃 4096
byte 的 buffer。`ClientHandShake`/`ServerHandShake`/`NewConn` 不管這個大小，
你給它什麼 `*bufio.Reader` 它就用什麼。如果你想少一點點速度、換每條連線少很
多記憶體，自己決定大小就好：

```go
r := bufio.NewReaderSize(netConn, 16) // 16 是下限 —— 低於它會被 bufio 拉到 16
```

16 不是隨便挑的下限：一個 frame 的 header（2 byte）加上最長的 extended
length（8）再加上 mask key（4）是 14 byte，剛好在 16 之下 —— 所以不管用哪種
大小，frame 除了 payload 以外的部分都還是一次讀完。比 16 大能多買到的，是小
的 payload 剛好搭同一次讀進來；一旦超過 16 byte，大的 payload 不管 buffer 多
大都得自己再讀一次，訊息越大，差距就越小。

記憶體那一側，會隨著同時開著的連線數放大：

| | 16 byte | 4096 byte（預設） |
|---|---|---|
| 每條連線 | 16 B | 4096 B |
| 1,000 條連線 | 約 16 KB | 約 4 MB |
| 100,000 條連線 | 約 1.6 MB | 約 400 MB |

而讀取次數那一側，會隨 frame 大小和連續到達的數量放大：

| | 16 byte | 4096 byte（預設） |
|---|---|---|
| 一個 10 B 的 frame | 1 次讀取 | 1 次讀取 |
| 連續 100 個 10 B 的 frame（共 1000 B） | 約 63 次讀取 | 最少 1 次讀取 |
| 一個 1 MB 的 frame | payload 繞過 buffer —— 兩種讀取次數一樣 | payload 繞過 buffer —— 兩種讀取次數一樣 |

（100 個 frame 那一列假設呼叫 `Read` 時資料早就到了，這是穩定串流下的常態；
如果對方傳得很慢、斷斷續續，差距就會縮小。）

## Reading

兩種方式，看你想自己扛多少。

**`Listener`** 會讀取、依 RFC 6455 驗證、把分段的 message 組起來，然後依 opcode
呼叫對應的 hook。它從不寫出、也從不關閉 —— RFC 加在接收端身上的每一項義務，最後
都落在某個 hook 上。`SetConfig` 是整份設定用複製的方式交出去，隨時都能呼叫 ——
包含在 hook 裡面：

```go
listener := conn.NewStandardListener() // 或 wlgows.NewListener(conn)，沒有 hook

config := listener.GetConfig()
config.MaxMsgPayloadByteLen = 10 * 1024 * 1024
config.Text = func(frames wlgows.Frames) { log.Print(frames.String()) }
listener.SetConfig(config)

err := listener.Listen()
```

`NewStandardListener` 就是同一個 Listener，只是協定本身該負責的部分已經回答好了
—— ping、pong、close 和保留 opcode —— 而且 `PeerIsClient` 直接依連線是哪一側決
定。這些 hook 每一個都可以換掉。

細節見 **[LISTENER_README.zh-TW.md](./LISTENER_README.zh-TW.md)** —— hook、各種上限、暫停，
以及每個 error 的意思。

**`GetNextFrame`** 一次給你一個 frame，由你自己組：

```go
var frames wlgows.Frames
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

一次一個 frame，才有辦法把超大的 message 直接串到別的地方，而不是整個抓在手上。
`Frames` 在你要的時候才組合：

| 呼叫 | 回傳 |
|---|---|
| `frames.String()` | 接起來的 payload，型別是 `string` |
| `frames.Bytes()` | 接起來的 payload，一份屬於你的新 `[]byte` |
| `frames.ByteLen()` | 接起來的大小，單位是 **byte**，完全不配置記憶體 |

兩個組合方法都會把每個 fragment 接起來，所以跨 frame 邊界被切斷的多 byte rune 會
完整回來。`ByteLen` 算的是 byte，不是字元數：`"中文字"` 會回報 9，不是 3。

opcode 只在第一個 frame 上 —— continuation frame 帶的是 0：

```go
switch frames[0].Opcode {
case wlgows.OpcodeText:   // 0x1
case wlgows.OpcodeBinary: // 0x2
case wlgows.OpcodeClose:  // 0x8 —— 停止讀取
case wlgows.OpcodePing:   // 0x9
case wlgows.OpcodePong:   // 0xA
}
```

## Sending

四個層級。用最上面那個合用的就好。

| | 呼叫 |
|---|---|
| 一整個 message | `SendText(b)`、`SendBinary(b)` |
| Control frame | `SendClose(payload)`、`SendPing(b)`、`SendPong(b)` |
| 大到放不進記憶體 | `StartLongDataTransmission(opcode)` / `TransmitData(chunk)` / `EndLongDataTransmission()` |
| 自己組的 frame | `NewFrame(config)` 再 `SendFrame(f)` |

`SendText` 會檢查 UTF-8（5.6），無效的 byte 會用 `ErrInvalidUTF8` 拒絕，因為會驗
證的對端會回一個 close 1007。`SendBinary` 什麼都不檢查 —— 5.6 根本沒有給 binary
任何編碼規定。

**close 一送出就結束了。** 5.5.1 把 closing handshake 定成一邊各一個 close，而且
其後不得再送任何 data frame，所以只要 `SendClose` 出去過一次，之後的
`SendClose`、`SendText`、`SendBinary` 和 `StartLongDataTransmission` 都會回
`ErrCloseAlreadySent`，而且什麼都不會寫出去。每一個都是在「記錄這個 close 的同一
個 lock」底下檢查的，所以兩個 goroutine 同時搶著回應對端的 close 時，不可能兩個
都把 frame 送上線 —— 慢的那個會被告知它的 frame 不需要了。`SendFrame` 不在此
限：它照設計就什麼都不檢查，所以你自己組的 close 由你自己安排順序。

串出一個記憶體放不下的 message —— 一個 chunk 就是一個 frame，所以讀進 buffer，
想傳幾次就傳幾次：

```go
if err := conn.StartLongDataTransmission(wlgows.OpcodeBinary); err != nil {
	return err
}
defer conn.EndLongDataTransmission()

buf := make([]byte, 32*1024)
for {
	n, err := file.Read(buf)
	if n > 0 {
		if err := conn.TransmitData(buf[:n]); err != nil {
			return err
		}
	}
	if err == io.EOF {
		return nil
	}
	if err != nil {
		return err
	}
}
```

**`TransmitData` 不會留著 `buf`。** 它在回傳前就已經封好並寫出去了，所以下一次
`Read` 讀回同一個 buffer 是安全的 —— 不論 message 多大，記憶體都維持在一個 chunk
的量。

5.4 也幫你顧好了：opcode 放在第一個 frame，之後每一個都是 `OpcodeContinuation`，
而且中間不會有別的 message 插進來。`End` 會用一個帶 FIN 的空 frame 作結，這也是為
什麼四個 chunk 的 message 會送出五個 frame。

[`stream_client`](./example/stream_client/main.go) 就是這樣串一個檔案，而
[`stream_server`](./example/stream_server/main.go) 收下它卻不整個抓在手上。
`Listener` 平常會把整個 message 組好才呼叫你的 hook，所以接收那端改用 `Data`
hook —— 它一個一個 data frame 交出來，什麼都不留 —— 見
[LISTENER_README.zh-TW.md](./LISTENER_README.zh-TW.md)。

`SendFrame` 是逃生門 —— 它就把你組好的東西寫出去，除了 masking 幾乎什麼都不檢
查。動用它之前先讀它的 doc。

## Keepalive

死掉的對端，和只是安靜的對端，看起來一模一樣。RFC 6455 5.5.2 讓 ping 當問題、
pong 當答案，所以「還活著嗎」是兩半：一半定期發問，一半判斷收回來的答案。

`StartPingLoop` 是發問的那一半。它會 block，所以 goroutine 由你決定：

```go
go conn.StartPingLoop(30*time.Second, nil)   // nil：空的 ping
```

它會自己結束 —— 送不出去的 ping，或是 handshake 任一側已經送出 close 之後。它不
重試，所以送失敗的那個 ping 就是最後一個，也沒有任何 handle 要你記著。

`payload` 是每次 ping 都會呼叫一次，不是只呼叫一次。5.5.2 要求對端把那些 byte 原
封不動回傳，所以讓它每次不一樣，你才分得出回來的是哪一個 ping：

```go
var seq atomic.Uint64
go conn.StartPingLoop(30*time.Second, func() []byte {
	return []byte(fmt.Sprintf("ping-%d", seq.Add(1)))
})
```

pong 會把同一份 `ping-4` 帶回來，所以 `Pong` hook 看得出它回的是哪一個。對協定來
說 byte 就只是 byte —— 數字、時間戳、任何你在回程認得出來的東西都行。

**判斷回應是你的事**，而且只能是你的事。在 `Pong` hook 裡蓋一個時間戳，跟你自己
的 deadline 比，超過就關閉：

```go
config.Pong = func(*wlgows.Frame) { lastPong.Store(time.Now()) }
```

蓋時間戳時不要去比對 payload。5.5.3 允許沒人問就送的 pong，5.5.2 也允許在好幾個
ping 還沒回的情況下只回最新的那一個，所以一個對不上你送過的任何東西的 pong，仍然
證明對端還活著。

底下的 `Loop` 是匯出的，什麼工作都收 —— 它每隔一段時間呼叫一次那個 func，直到那
個 func 表示要停：

```go
wlgows.Loop(func(stop chan<- struct{}) {
	if done() {
		stop <- struct{}{}   // 只送一次；這個 func 一回來就會被檢查
		return
	}
	work()
}, time.Second)
```

## Concurrency

`Conn` 持有三個 `sync.Locker`，預設都是 `*sync.Mutex`：

| Locker | 保護什麼 | 誰會拿 |
|---|---|---|
| `writeLocker` | 同一時間只有一個 frame 在線上 | 每個 `Send*`，持有一個 frame 的長度 |
| `dataFramesWriteLocker` | 同一時間只有一個 data message | `SendText`、`SendBinary`，以及 `Start`…`End` |
| `readLocker` | 只有一個讀取者 | `GetNextFrame` |

**從多個 goroutine 送出是安全的。** 用兩個 lock 而不是一個，就是為了不讓一個 pong
卡在 10 GB 的傳輸後面：control frame 只拿 `writeLocker`，所以它會插在 fragment 之
間 —— 這正是 5.4 刻意允許、而 5.5.2 需要的。

**讀取請只用一個 goroutine。** `readLocker` 擋得住兩個讀取者互相搶 byte，但它們仍
然會各自拿到一部分 frame。

**`conn.Write` 和 `conn.Read` 會繞過所有 lock** —— 它們是從內嵌的 `net.Conn` 提升
上來的。請用 `Send*` 系列送出。

`Close` 刻意不拿任何 lock：關掉 fd 正是把卡在死掉對端上的讀或寫解開的方法。

## Errors

每個 error 都用 `%w` 包住一個 package 層級的 sentinel，所以請用 `errors.Is` 比
對，不要用 `==`：

```go
if _, _, err := wlgows.ServerHandShake(netConn, r, req); errors.Is(err, wlgows.ErrHttpMethodNotAllowed) {
	// ...
}
```

`StandardClosePayloadFor` 會把一個 `Listen` 的 error 對應到 RFC 6455 7.4.1 要的
close code —— UTF-8 壞掉是 1007，message 超過上限是 1009，其餘是 1002，而任何 close
frame 回答不了的東西則是 `nil`：

```go
if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
	conn.SendClose(payload)
}
```

`nil` 表示「無從歸責」，不是「請關閉連線」。你自己的 error 也會走到這裡 ——
`PauseListen(err)` 會結束一次執行，而 `Listen` 就回傳它；如果是關閉連線結束了那次
讀取，它還會和 socket 自己的 error 合併回來 —— 一樣會拿到 `nil`，因為這個 package
沒辦法替它不認識的規則作答。要的話請自己先對應。見
[LISTENER_README.zh-TW.md](./LISTENER_README.zh-TW.md) 的 Errors 章節。

## Testing

```bash
go test ./...                              # 全部
go test -race ./...                        # integration test 會開 goroutine
go test -cover .                           # 99.7% of statements
go test -bench BenchmarkFramesAssembly .   # benchmark，單純的 go test 會跳過
```

每個原始檔對應一個 `_test.go`，全部都在 package `wlgows` 裡面，這樣才碰得到 `di`
接縫。有兩個檔案沒有對應的原始檔：

| 檔案 | 內容 |
|---|---|
| [`Fakes_test.go`](./Fakes_test.go) | 共用的替身 —— `fakeConn`（記憶體版 `net.Conn`）、`fakeIOWriter`/`fakeIOReader`（單純的 `io.Writer`/`io.Reader` 替身）、`fakeLocker`（會計次，也會抓出誤用）、`fixedRandRead`、`scriptedReadFromReader` |
| [`ListenerIntegration_test.go`](./ListenerIntegration_test.go) | `Listen` 跑在真的 `net.Pipe` 上，端到端，**不**替換任何 `di` |

integration test 抓得到 unit test 結構上抓不到的接線錯誤 —— 例如 constructor 忘了
填某個 `di` 欄位，或某個預設值指到錯的 function。

`BenchmarkFramesAssembly` 說明了為什麼在 70000 byte 的 message 上要用 `Bytes()`
而不是 `[]byte(String())`：

```
[]byte(String())   10774 ns/op   147458 B/op   2 allocs/op
Bytes()             5722 ns/op    73729 B/op   1 allocs/op
```

時間一半、記憶體一半 —— 那個 `string` header 從頭到尾只是為了再被轉換掉而存在。

### 替換相依

每個會做 I/O 或用到隨機性的 function，都是一層薄薄的匯出包裝，底下是一個吃 `di`
struct（裡面是 function value）的實作；type 則把相依放在 constructor 填好的 `di`
欄位裡。這些全部未匯出，所以只有 package 內的測試碰得到。純函數
（`Frame.Seal`、`Frames.String`、`ValidateHandShakeRequest`…）沒有接縫，直接測。

```go
// package 層級的 func：傳一個 di struct 進去
netConn, req, err := dial("ws://localhost:8001", nil, dialDI{...})

// method：覆蓋掉 constructor 設好的欄位
conn := NewConn(netConn, bufio.NewReader(netConn), false)
conn.di.newDataFrame = func(config NewFrameConfig) (*Frame, error) { ... }
```

把隨機來源固定住，masked 的輸出才有辦法斷言 —— 否則一個 masked frame 的 byte 根
本釘不住：

```go
f, _ := newFrame(NewFrameConfig{PayloadData: []byte("hi"), Opcode: 1, Mask: true, FIN: true},
	newFrameDI{generateMaskingKey: func() ([]byte, error) { return []byte{1, 2, 3, 4}, nil }})
// f.Seal() == []byte{0x81, 0x82, 1, 2, 3, 4, 'h'^1, 'i'^2}
```

有一個測試刻意斷言的是「目前的行為」而不是「正確的行為」，而且在名稱和註解裡都
說清楚了：

- `TestDial/an_unknown_scheme_yields_a_nil_conn_and_no_error` —— `dial` 裡的
  scheme switch 沒有 default 分支。正式環境到不了，因為 `ValidateWebsocketUrl` 擋
  在前面；只有一個寬鬆的 fake 才能把它露出來。

覆蓋率最主要的缺口是 `ClientHandShake` 自己的成功路徑：真正的 client 每次呼叫
都會用 `crypto/rand` 挑一把新的 key，所以沒辦法用一份固定的 wire response 去對
上它，除非真的接上一條雙向的連線。它包住的 `clientHandShake`（DI 那一步）和它
成功時會用到的 `NewConn`，兩個都各自測到 100%。

## License

MIT
