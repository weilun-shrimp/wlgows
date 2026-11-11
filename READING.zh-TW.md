[English](./READING.md) · **繁體中文**

# Reading

這份說明介紹怎麼讀取對方送來的 message。

讀取分兩步：

1. `Listener` 讀取 frame，每收齊一則 message，就用 `DataFrames` 交給你。
2. `conn.DataFramesReader(frames)` 給你這則 message 的內容。message 有壓縮時，它會
   解壓縮；text message 會檢查是不是 UTF-8。

取得 `Conn` 見 [SERVER.zh-TW.md](./SERVER.zh-TW.md) 與
[CLIENT.zh-TW.md](./CLIENT.zh-TW.md)。傳送見 [SENDING.zh-TW.md](./SENDING.zh-TW.md)。
Listener 怎麼運作見 [LISTENER.zh-TW.md](./LISTENER.zh-TW.md)。

## Contents

- [Quick start](#quick-start) — 讀取一條連線的所有 message
- [DataFramesReader](#dataframesreader) — 使用這個 reader 的規則
- [限制大小](#限制大小) — `NewSizeLimitReader` 與 close code 1009
- [兩種推薦的讀法](#兩種推薦的讀法) — `io.ReadAll`、`ReadMessageInto`，以及一段一段讀（小心使用）
- [Errors](#errors) — 每個 error 代表什麼，要回哪個 close code
- [壓縮的 message](#壓縮的-message) — 對方有壓縮時，你一定要做的事
- [大型 message：Data hook](#大型-messagedata-hook) — 一次一個 frame，有沒有壓縮都可以
- [不複製地讀取](#不複製地讀取) — 進階：binary，以及用 `unsafe` 讀 text
- [不用 Listener 讀取](#不用-listener-讀取) — 不建議
- [記憶體：連線的 read buffer](#記憶體連線的-read-buffer)
- [Concurrency](#concurrency) — 只有一個讀取者，message 照順序讀

## Quick start

`conn` 是 handshake 完成後的 `*wlgows.Conn`。下面的程式讀取 text 與 binary
message，每則最多 10 MB。出任何問題就關閉連線。

```go
const maxMessageSize = 10 * 1024 * 1024 // 10 MB，以解壓縮後計算

// readMessage 把一則 message 讀進 buf，回傳 buf 裡填了資料的那一段。
func readMessage(conn *wlgows.Conn, frames wlgows.DataFrames, buf []byte) ([]byte, error) {
	reader, err := conn.DataFramesReader(frames)
	if err != nil {
		return nil, err
	}
	defer reader.Close() // 一定要 Close

	n, err := wlgows.ReadMessageInto(
		wlgows.NewSizeLimitReader(reader, maxMessageSize),
		buf,
	)
	if err != nil {
		return nil, err // ErrSizeLimitExceeded、ErrInvalidUTF8 等
	}
	return buf[:n], nil
}

func handleConn(conn *wlgows.Conn) {
	defer conn.Close()

	listener := conn.NewStandardListener() // 替你回應 ping 與 close

	// 這條連線的所有 message 共用一個 buffer。message 只留到下一則進來為止：
	// 要保留的部分請自己複製。
	buf := make([]byte, maxMessageSize)

	config := listener.GetConfig()
	config.MaxDataFramesSize = maxMessageSize // 收到的 byte 數
	config.MaxFrameSize = maxMessageSize
	config.MaxDataFrameCount = maxMessageSize / 256
	config.Text = func(frames wlgows.DataFrames) {
		message, err := readMessage(conn, frames, buf)
		if err != nil {
			listener.PauseListen(err) // Listen 會回傳 err
			return
		}
		log.Printf("text: %s", message)
	}
	config.Binary = func(frames wlgows.DataFrames) {
		message, err := readMessage(conn, frames, buf)
		if err != nil {
			listener.PauseListen(err)
			return
		}
		log.Printf("binary: %d bytes", len(message))
	}
	listener.SetConfig(config)

	err := listener.Listen()
	if err == nil {
		return // 對方關閉了連線
	}
	// 對方造成的 error 會有對應的 close code，其他的是 nil。
	if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
		conn.SendClose(payload)
	}
	log.Println("closing:", err)
}
```

`Listen` 所有的 error 見
[LISTENER.zh-TW.md](./LISTENER.zh-TW.md#errors)。

## DataFramesReader

`conn.DataFramesReader(frames)` 回傳一個 `io.ReadCloser`。使用時請遵守：

- **一直讀到 `io.EOF`。** 讀到 `io.EOF`，message 才算完整、檢查完畢。
- **一定要呼叫 `Close`**，`Read` 回傳 error 之後也一樣。用 `defer` 最簡單。
- `Read` 還在執行時，不要呼叫 `Close`。
- `Close` 之後，不要再呼叫 `Read`。
- `Close` 之前，不要改動 frame。reader 直接讀 frame 裡的 payload，不會複製。

每種 message 都可以用這個 reader：

| message 是 | `Read` 給你的是 |
|---|---|
| 沒有壓縮 | 各個 payload，一個接一個 |
| 有壓縮（[permessage-deflate](./PERMESSAGE_DEFLATE.zh-TW.md)） | 解壓縮後的 byte |
| text | 一樣的 byte，並檢查是不是 UTF-8 |

你不用知道是哪一種。reader 會從 frame 自己判斷。

## 限制大小

Listener 的 `MaxDataFramesSize` 算的是收到的 byte 數。壓縮過的 message 解壓縮後可能
大很多：1 KB 的壓縮資料，可能變成好幾 MB。

所以要限制你讀出來的 byte 數。把 reader 包進 `wlgows.NewSizeLimitReader`：

```go
reader, err := conn.DataFramesReader(frames)
if err != nil {
	return err
}
defer reader.Close() // 一定要 Close

sizeLimitReader := wlgows.NewSizeLimitReader(reader, maxMessageSize)
```

size limit reader 會：

- 給你 message 的內容，最多 `maxMessageSize` byte；
- message 超過時，`Read` 回傳 `ErrSizeLimitExceeded`。它會多讀 1 byte 來判斷，但
  這 1 byte 不會給你；
- 只有整則 message 都在限制內，才回傳 `io.EOF`。

[`ReadMessageInto`](#readmessageinto) 也有一樣的限制：`len(buf)`。

收到 `ErrSizeLimitExceeded` 時：

1. 交給 `listener.PauseListen(err)`，`Listen` 就會回傳它。
2. `StandardClosePayloadFor(err)` 會給你 close code 1009（RFC 6455 7.4.1）。
3. 送出這個 close，然後關閉連線。

[Quick start](#quick-start) 三件事都有做。

## 兩種推薦的讀法

三種讀法都從 `sizeLimitReader` 讀，做法見[限制大小](#限制大小)。

| 讀法 | 配置記憶體 | 記憶體用量 | 拿到 message 的方式 |
|---|---|---|---|
| `io.ReadAll` | 好幾次，邊讀邊變大 | 大約是 message 的大小 | 整則，檢查完才拿到 |
| `ReadMessageInto` | 一次 | 不論 message 多大，都是 `maxMessageSize` | 整則，檢查完才拿到 |
| 一段一段讀（小心使用） | 一次 | 只有你的 buffer，比 message 小 | 分段拿到，還沒檢查完 |

### `io.ReadAll`

```go
sizeLimitReader := wlgows.NewSizeLimitReader(reader, maxMessageSize)
message, err := io.ReadAll(sizeLimitReader)
if err != nil {
	return err // ErrSizeLimitExceeded、ErrInvalidUTF8 等
}
```

最簡單的讀法。但 `io.ReadAll` 事先不知道 message 多大：message 大的話，它的 buffer
每滿一次，就要重新配置一次。

### `ReadMessageInto`

```go
sizeLimitReader := wlgows.NewSizeLimitReader(reader, maxMessageSize)
buf := make([]byte, maxMessageSize)
n, err := wlgows.ReadMessageInto(sizeLimitReader, buf)
if err != nil {
	return err // ErrSizeLimitExceeded、ErrInvalidUTF8 等
}
message := buf[:n] // 整則 message
```

只配置一次。`buf` 可以每則 message 重複使用，就像 [Quick start](#quick-start)
那樣。但它一定佔用 `maxMessageSize` byte，就算 message 只有 10 byte 也一樣。

`encoding/json` 的 `json.Unmarshal` 會把解出來的內容複製一份。所以它一回傳，`buf`
就可以拿去讀下一則 message。其他 decoder 可能會繼續用到 `buf`，使用前請先確認。

回傳 `nil` 代表整則 message 都放進了 `buf`。有 error 時不要用 `buf[:n]`：內容可能
不完整，或沒通過檢查。

### 小心：一段一段讀進比 message 小的 buffer

**只有在你完全清楚自己在做什麼時才使用。** 你會在 message 檢查完之前就拿到內容。
你處理了幾段之後，它仍可能失敗：太大、不是 UTF-8（這要到最後才知道），或無法
解壓縮。所以只有在你能丟掉已處理的內容時，才用這種讀法。例如刪掉寫到一半的檔案。

```go
sizeLimitReader := wlgows.NewSizeLimitReader(reader, maxMessageSize)
buf := make([]byte, 32*1024) // 32 KB，每一段都重複使用
for {
	n, err := sizeLimitReader.Read(buf)
	handle(buf[:n]) // message 的下一段
	if err == io.EOF {
		return nil // 整則 message，沒有超過大小
	}
	if err != nil {
		return err // ErrSizeLimitExceeded：在這裡停下
	}
}
```

不論 message 多大，記憶體都只有一個 buffer。內容一到就拿到，message 一超過大小就
停下。

先處理 `buf[:n]`，再檢查 `err`。`Read` 可能同時給你資料和 error，`io.EOF` 也是。

`io.Copy(file, sizeLimitReader)` 會替你跑同樣的迴圈。message 太大時，它回傳
`ErrSizeLimitExceeded`，檔案裡只有前 `maxMessageSize` byte。

## Errors

讀取時的 error：

| Error | 代表 | Close code |
|---|---|---|
| `ErrInvalidUTF8` | text message 不是合法的 UTF-8。它會取代 `io.EOF` 回傳 | 1007 |
| `ErrInflateDataInvalid` | 壓縮過的 message 無法解壓縮 | 1007 |
| `ErrSizeLimitExceeded` | message 超過你允許的大小。來自 `ReadMessageInto` 或 `NewSizeLimitReader` | 1009 |

這三個都可以用 `StandardClosePayloadFor(err)` 取得 close payload。

`DataFramesReader` 本身的 error：

| Error | 代表 |
|---|---|
| `ErrDataFramesNotEnded` | 沒有 frame，或最後一個 frame 沒有 FIN |
| `ErrContinuationFrameWithoutMsg`、`ErrDataFrameDuringMsg` | 這些 frame 不是一則 message |
| `ErrReservedBitsSet` | 在不允許的地方設了 RSV1 |

Listener 的 `Text` 或 `Binary` hook 給你的 frame 不會出現這些 error。只有你自己
收集 frame 時才會遇到。

## 壓縮的 message

handshake 談定 [permessage-deflate](./PERMESSAGE_DEFLATE.zh-TW.md) 時，對方可能會
壓縮它的 message。`DataFramesReader` 會替你解壓縮。

Listener 不會解壓縮。所以壓縮過的 text message：

- `Text` hook 拿到時**沒有**檢查 UTF-8；
- `DataFramesReader` 解壓縮後才檢查。

每則 message 都用 `DataFramesReader` 讀，每則 text message 就都會檢查到。

### 對方保留 context 時

對方壓縮每則 message 時，可以參考之前的 message。這叫 context takeover
（RFC 7692 7.1.1），由對方在 handshake 時決定。這時：

- **每則壓縮過的 message 都要照收到的順序交給 `DataFramesReader`。** 跳過一則，
  下一則就會壞掉。
- **每則都要讀到 `io.EOF`。** 還沒讀到 `io.EOF` 就 `Close` 的話，下一則壓縮過的
  message 可能解出錯的內容，而且沒有任何 error。
- **一則讀完再讀下一則。** 對下一則壓縮過的 message 呼叫 `DataFramesReader` 時，它會
  等待。等到前一個 reader 回傳 error（包括 `io.EOF`）或被關閉，它才會繼續。如果在
  同一個 goroutine 裡提早呼叫，它會永遠等下去。

像 [Quick start](#quick-start) 那樣在 `Text` 與 `Binary` hook 裡讀，就符合這三條
規則。

如果對方同意 no context takeover，每則 message 都各自獨立，提早 `Close` 就沒問題。

## 大型 message：Data hook

`Text` 與 `Binary` hook 拿到的是整則 message，所以它必須放得進記憶體。message 大到
放不下時，請設定 `Data` hook。每個 data frame 一到就交給它，Listener 一個都不保留。

這時有些事要你自己做：

- 注意 `frame.FIN`。只有它會告訴你 message 結束了。
- continuation frame 沒有類型。請用 `listener.GetCurrentMsgOpcode()` 取得。
- text message 必須是合法的 UTF-8（RFC 6455 5.6）。單看一個 frame 無法檢查，因為
  一個字可能被切在兩個 frame 裡。請自己檢查。
- `MaxDataFramesSize` 現在算的是整個串流。也請設定 `MaxFrameSize`，讓單一 frame
  不會太大。

### 沒有壓縮

下面直接把 binary message 寫進檔案。不論 message 多大，用到的記憶體都很少：

```go
out, err := os.Create("./received.bin")
if err != nil {
	return err
}
defer out.Close()

listener := conn.NewStandardListener()
config := listener.GetConfig()
config.MaxDataFramesSize = 2 * 1024 * 1024 * 1024 // 整個串流
config.MaxFrameSize = 64 * 1024                    // 單一 frame
config.MaxDataFrameCount = 0                       // 不限制：frame 不會被保留
config.Data = func(frame *wlgows.Frame) {
	if listener.GetCurrentMsgOpcode() != wlgows.OpcodeBinary {
		listener.PauseListen(errors.New("peer sent text"))
		return
	}
	if listener.IsCurrentMsgDeflated() {
		listener.PauseListen(errors.New("peer sent a compressed message"))
		return
	}
	if _, err := out.Write(frame.PayloadData); err != nil {
		listener.PauseListen(err)
		return
	}
	if frame.FIN {
		log.Println("message complete")
	}
}
listener.SetConfig(config)

// Listen 在 FIN frame 之前就回傳的話，out 裡只有 message 的一部分。
// 請丟掉它，例如刪掉檔案。
return listener.Listen()
```

[`stream_server`](./example/stream_server/main.go) 用真的檔案做這件事：一個 frame
一個 frame 地接收檔案，並計算 hash。

### 有壓縮

壓縮過的 message 分四步解壓縮：

1. 第一個 frame 時，`token := conn.StartInflateDataFrames()`。
2. 每個 frame 都 `conn.PutDeflatedDataFrame(token, frame)`。
3. 用 `conn.PullInflatedBytes(token, buf)` 取得解壓縮後的 byte，直到 `io.EOF`。
4. 最後 `conn.EndInflateDataFrames(token)`。一定要呼叫，出錯之後也一樣。

在另一個 goroutine 裡 pull，frame 同時繼續進來。每個 frame 解壓縮完就會釋放，所以
用到的記憶體很少。`PullInflatedBytes` 需要更多資料時，會等下一個 frame。

下面把壓縮過的 binary message 寫進檔案：

```go
var token int64
// pullResult 告訴 hook pull 怎麼結束：整則 message 都寫完是 nil，否則是停下的
// 原因。收到值也代表 pull 已經回傳，可以安全呼叫 EndInflateDataFrames。
// 沒有進行中的 message 時，channel 本身是 nil。
var pullResult chan error

config.Data = func(frame *wlgows.Frame) {
	if !listener.IsCurrentMsgDeflated() || listener.GetCurrentMsgOpcode() != wlgows.OpcodeBinary {
		listener.PauseListen(errors.New("expected a compressed binary message"))
		return
	}
	if frame.Opcode != wlgows.OpcodeContinuation { // 第一個 frame
		token = conn.StartInflateDataFrames()
		pullResult = make(chan error, 1) // 留 1 格：送出時不會卡住
		go func(token int64, result chan<- error) {
			result <- writeInflated(conn, token, out)
		}(token, pullResult)
	}
	if err := conn.PutDeflatedDataFrame(token, frame); err != nil {
		listener.PauseListen(err)
		return
	}

	if !frame.FIN {
		select {
		case err := <-pullResult: // pull 提早停了：太大，或資料不合法
			conn.EndInflateDataFrames(token)
			pullResult = nil
			listener.PauseListen(err)
		default:
		}
		return
	}

	err := <-pullResult // 整則 message 都放進去了：等 pull 結束
	conn.EndInflateDataFrames(token)
	pullResult = nil
	if err != nil {
		listener.PauseListen(err)
	}
}
listener.SetConfig(config)

err := listener.Listen()
if pullResult != nil { // 在 message 中途停下
	conn.PutDeflatedDataFrame(token, &wlgows.Frame{FIN: true}) // pull 會回傳
	<-pullResult
	conn.EndInflateDataFrames(token)
}
return err
```

```go
// writeInflated 把解壓縮後的 message pull 到 out，最多 maxMessageSize。
func writeInflated(conn *wlgows.Conn, token int64, out io.Writer) error {
	buf := make([]byte, 32*1024)
	written := 0
	for {
		n, err := conn.PullInflatedBytes(token, buf)
		written += n
		if written > maxMessageSize {
			return wlgows.ErrSizeLimitExceeded
		}
		if _, writeErr := out.Write(buf[:n]); writeErr != nil {
			return writeErr
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			// ErrInflateDataInvalid：用 1007 關閉。
			// 有任何 error，或 message 在 FIN 之前就停下時，out 裡只有 message 的
			// 一部分：請丟掉它，例如刪掉檔案。
			return err
		}
	}
}
```

**在 message 中途停下。** `Listen` 在 FIN frame 之前就回傳時，pull 還在等下一個
frame。放一個沒有 payload 的 FIN frame 進去，pull 就會回傳。等它回傳後，再呼叫
`EndInflateDataFrames`。pull 寫出來的不是整則 message。

[對方保留 context 時](#對方保留-context-時)的規則這裡也適用。還沒到 `io.EOF` 就
結束一則 message 的話，下一則壓縮過的 message 可能解出錯的內容。

另一則 message 還沒結束時，`StartInflateDataFrames` 會等待。所以下一則開始前，
請先結束前一則。

## 不複製地讀取

**進階用法。只有在你完全清楚自己在做什麼時才使用。** 不確定的話，請用
`DataFramesReader`。

**只有一個 frame、沒有壓縮**的 message，內容已經在記憶體裡了，就是那個 frame 的
payload。wlgows 讀完後從不寫入 payload，而且每個 frame 有自己的 payload。所以你可以
不用 `DataFramesReader`、不複製，直接使用 payload。

其他 message（有好幾個 frame，或有壓縮）一定要用 `DataFramesReader` 讀。

### Binary

```go
config.Binary = func(frames wlgows.DataFrames) {
	if len(frames) != 1 || frames[0].RSV1 {
		message, err := readMessage(conn, frames, buf) // Quick start 裡的函式
		if err != nil {
			listener.PauseListen(err)
			return
		}
		handle(message)
		return
	}
	handle(frames[0].PayloadData) // 整則 message，沒有複製
}
```

不需要 `unsafe`：payload 本來就是 `[]byte`。不要寫入它。

### Text：`unsafe`

[傳送說明](./SENDING.zh-TW.md#更快更省記憶體用-unsafe-送出-string)裡的警告這裡同樣
適用。

text message 可以不複製就轉成 string：

```go
config.Text = func(frames wlgows.DataFrames) {
	if len(frames) != 1 || frames[0].RSV1 {
		message, err := readMessage(conn, frames, buf) // Quick start 裡的函式
		if err != nil {
			listener.PauseListen(err)
			return
		}
		handle(string(message))
		return
	}
	payload := frames[0].PayloadData
	handle(unsafe.String(unsafe.SliceData(payload), len(payload)))
}
```

這樣 message 在記憶體裡只有一份，不是兩份。

- **之後絕對不要寫入 `payload`。** 不然 string 也會跟著變。Go 假設 string 不會變，
  用它當 key 的 map 就會出錯。
- **這個 string 會讓整個 payload 留在記憶體。** 只想留大 message 的一小段時，請用
  `strings.Clone` 複製。
- 在 `Text` 執行之前，Listener 已經檢查過這些 byte 是 UTF-8。這只在 message
  沒有壓縮時成立。

## 不用 Listener 讀取

**不建議這麼做。** 不用 Listener 的話，RFC 6455 對讀取端的每一條規則都要你自己負責。

讀一個 frame 分兩步：

1. `conn.GetNextFrameHeader()` 回傳只有 header、沒有 payload 的 frame。你可以在配置
   任何記憶體之前，先檢查它的大小。
2. `conn.ReadFramePayload(frame)` 讀取 payload。

這兩步都要在持有 `conn.LockRead()` 時進行，其他讀取者才不會在中間拿走 byte：

```go
var frames wlgows.DataFrames
for {
	conn.LockRead()
	frame, err := conn.GetNextFrameHeader()
	if err == nil && frame.GetDeclaredPayloadSize() > 10*1024*1024 {
		err = errors.New("frame too large") // 還沒配置任何記憶體
	}
	if err == nil {
		err = conn.ReadFramePayload(frame)
	}
	conn.UnlockRead()
	if err != nil {
		return err
	}
	if wlgows.IsDataOpcode(frame.Opcode) {
		frames = append(frames, frame)
		if frame.FIN {
			break
		}
	} else {
		// control frame 或保留的 opcode：由你處理。
	}
}

message, err := readMessage(conn, frames, buf) // Quick start 裡的函式
```

## 記憶體：連線的 read buffer

每條連線都透過一個 `*bufio.Reader` 讀取 frame，也就是交給 handshake 的那個
reader。它的大小，就是每條連線為了讀取所佔用的記憶體。

- **Server：** 我們建議用 net/http 在 `Hijack` 時給的 reader。它的大小由 net/http
  決定，不是 wlgows。見 [SERVER.zh-TW.md](./SERVER.zh-TW.md#http-hijacking)。
- **Client：** reader 由你自己建立。見 [CLIENT.zh-TW.md](./CLIENT.zh-TW.md)。

任何大小都可以。小一點能省記憶體，只會慢一點點：

```go
connReader := bufio.NewReaderSize(netConn, 16) // 16 是下限：小於 16 會被 bufio 調整為 16
```

16 就夠了。最長的 frame header 是 14 byte（2 + 8 長度 + 4 mask），仍能一次讀完。
更大的 buffer 只是讓小 payload 能在同一次讀進來。大 payload 不論 buffer 多大，都要
另外讀。

記憶體隨連線數增加：

| | 16 byte | 4096 byte（預設） |
|---|---|---|
| 每條連線 | 16 B | 4096 B |
| 1,000 條連線 | 約 16 KB | 約 4 MB |
| 100,000 條連線 | 約 1.6 MB | 約 400 MB |

讀取次數隨連續到達的 frame 數增加：

| | 16 byte | 4096 byte（預設） |
|---|---|---|
| 一個 10 B 的 frame | 1 次讀取 | 1 次讀取 |
| client 連續送來 100 個 10 B 的 frame<br>（加上 header 每個 16 B，共 1600 B） | 約 100 次讀取 | 最少 1 次讀取 |
| 一個 1 MB 的 frame | payload 不經過 buffer | payload 不經過 buffer |

100 個 frame 那一列假設資料都已經到了。對方慢的話，差距會變小。

一整條閒置連線最少要多少記憶體，見[最小的連線](./SENDING.zh-TW.md#最小的連線)。

## Concurrency

**只用一個 goroutine 讀取 frame。** lock 能避免兩個讀取者混到 byte，但它們還是會各自
拿到零散的 frame。Listener 就是一個讀取者，所以它執行時，不要自己讀取 frame。

`conn.Read` 來自內嵌的 `net.Conn`，會略過 lock。請透過 Listener 讀取 frame，或在
`LockRead` 之下用 `GetNextFrameHeader` 與 `ReadFramePayload` 讀取。

**照 message 收到的順序呼叫 `DataFramesReader`。** 對方保留 context 時，一定要照這個
順序（見[對方保留 context 時](#對方保留-context-時)）。如果你把 message 交給其他
goroutine，請在那裡照順序一則一則讀。最簡單的做法：直接在 hook 裡讀。
