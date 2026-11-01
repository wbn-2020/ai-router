package upstream

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

const (
	// DefaultMaxSSEEventBytes 是单个 SSE 事件允许的最大原始字节数。
	//
	// 一个事件可能包含多行 data（例如 tool arguments 或长文本），上限需要
	// 足够宽松；同时必须挡住恶意的无限长事件。超过上限时解析立即失败，
	// 上层不再尝试复用该连接。
	DefaultMaxSSEEventBytes = 1 << 20
)

var (
	// ErrSSEEventTooLarge 表示单个 SSE 事件超过 MaxEventBytes。
	ErrSSEEventTooLarge = errors.New("upstream: SSE 事件超过大小上限")
	// ErrSSETruncatedEvent 表示 EOF 到来前事件没有空行分隔符。
	ErrSSETruncatedEvent = errors.New("upstream: SSE 事件未完整结束")
	// ErrSSEIdleTimeout 表示流连续超过 IdleTimeout 没有收到任何字节。
	ErrSSEIdleTimeout = errors.New("upstream: SSE 流空闲超时")
)

// SSEEvent 是一个完整的 SSE 事件。
//
// 字段保留原始字节，不把值转成 string 再重新编码：
//   - Raw 是事件从首行到分隔空行的原始字节（包含 CRLF/LF 与空行）；
//   - Event/ID/Retry 是最后一个同名字段的值（已去掉字段名、冒号与一个可选空格）；
//   - Data 按 SSE 规范用 '\n' 连接多行 data 值；
//   - Comments 保存每个注释行的原始内容（包含开头的 ':'，不含行尾换行）。
//
// Raw 足以让调用方在“首内容提交”之前原样缓冲、提交后原样转发，避免重新
// 序列化改变上游字节。
type SSEEvent struct {
	Event    []byte
	ID       []byte
	Data     []byte
	Retry    []byte
	Comments [][]byte
	Raw      []byte

	// hasLines 表示至少解析到一行非空内容（字段或注释）。只有空行的事件
	// 不会返回给调用方。
	hasLines bool
	// hasData 表示已经出现至少一个 data 字段，用于保留空 data 行之间的
	// 换行语义（例如 "data:\ndata:x" 的 Data 是 "\nx"）。
	hasData bool
}

// SSEReaderOptions 控制 SSE 解析上限与空闲超时。
type SSEReaderOptions struct {
	// MaxEventBytes 是单事件原始字节上限；<=0 时使用 DefaultMaxSSEEventBytes。
	MaxEventBytes int
	// IdleTimeout 是连续无数据的最长时间；<=0 时不启用。
	//
	// 要真正中断阻塞中的读取，底层 Reader 必须实现 io.Closer；超时后
	// SSEReader 会关闭它，并把后续读取错误映射为 ErrSSEIdleTimeout。
	IdleTimeout time.Duration
}

// SSEReader 从字节流读取 SSE 事件。
//
// 它只负责解析；不解释 Responses/Chat 语义，也不做重试。读取过程中保留
// 事件的原始字节。SSEReader 不是并发安全的，调用方应在单个 goroutine 中
// 使用它，并在结束时调用 Close 以停止空闲计时器。
type SSEReader struct {
	reader        *bufio.Reader
	maxEventBytes int
	idleTimeout   time.Duration
	closer        io.Closer

	mu       sync.Mutex
	timer    *time.Timer
	timedOut bool
	closed   bool
}

// NewSSEReader 构造 SSE 解析器。r 为 nil 时 Next 返回 io.EOF。
func NewSSEReader(r io.Reader, opts SSEReaderOptions) *SSEReader {
	maxEventBytes := opts.MaxEventBytes
	if maxEventBytes <= 0 {
		maxEventBytes = DefaultMaxSSEEventBytes
	}

	reader := &SSEReader{
		maxEventBytes: maxEventBytes,
		idleTimeout:   opts.IdleTimeout,
	}
	if r != nil {
		reader.reader = bufio.NewReader(r)
		if closer, ok := r.(io.Closer); ok {
			reader.closer = closer
		}
	}
	reader.resetIdle()
	return reader
}

// Next 返回下一个完整事件。
//
// 注释行也属于事件内容：仅包含注释的块会作为 Comments 非空的事件返回，
// 使调用方在提交前可以原样缓冲、提交后可以原样转发 keep-alive。
// 流正常结束时返回 io.EOF；没有空行分隔的尾帧返回 ErrSSETruncatedEvent，
// 不把客户端无法派发的残帧当作完整事件。
func (r *SSEReader) Next() (SSEEvent, error) {
	if r == nil || r.reader == nil {
		return SSEEvent{}, io.EOF
	}

	var event SSEEvent
	used := 0

	for {
		line, err := r.readLine(r.maxEventBytes - used)
		if err != nil && !errors.Is(err, io.EOF) {
			return SSEEvent{}, r.mapReadError(err)
		}

		if len(line) > 0 {
			used += len(line)
			if used > r.maxEventBytes {
				return SSEEvent{}, ErrSSEEventTooLarge
			}
			event.Raw = append(event.Raw, line...)

			content := trimLineEnding(line)
			if len(content) == 0 {
				// 空行是事件分隔符。没有解析到任何字段/注释时只丢弃空行，
				// 不把连续空行当作事件返回。
				if !event.hasLines {
					event.Raw = event.Raw[:0]
				} else {
					return event, nil
				}
			} else {
				event.hasLines = true
				r.parseLine(&event, content)
			}
		}

		if err != nil {
			// 这里只会是 io.EOF：最后一个事件没有以空行结束。
			if event.hasLines {
				return SSEEvent{}, ErrSSETruncatedEvent
			}
			return SSEEvent{}, io.EOF
		}
	}
}

// parseLine 解析一行非空 SSE 内容。
func (r *SSEReader) parseLine(event *SSEEvent, content []byte) {
	if content[0] == ':' {
		comment := append([]byte(nil), content...)
		event.Comments = append(event.Comments, comment)
		return
	}

	field := content
	value := []byte(nil)
	if index := bytes.IndexByte(content, ':'); index >= 0 {
		field = content[:index]
		value = content[index+1:]
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}
	}

	switch string(field) {
	case "event":
		event.Event = append(event.Event[:0], value...)
	case "id":
		event.ID = append(event.ID[:0], value...)
	case "retry":
		event.Retry = append(event.Retry[:0], value...)
	case "data":
		if event.hasData {
			event.Data = append(event.Data, '\n')
		}
		event.Data = append(event.Data, value...)
		event.hasData = true
	default:
		// 未知字段按 SSE 规范忽略，但 Raw 已经保留原始字节。
	}
}

// readLine 读取一行，并把单行长度限制在 remaining 内。
//
// bufio.Reader.ReadSlice 在行超过内部缓冲区时返回 ErrBufferFull；这里继续
// 读取并累计，直到发现换行、EOF 或超过事件上限。这样既支持长 data 行，
// 又不会让单行无限占用内存。
func (r *SSEReader) readLine(remaining int) ([]byte, error) {
	if remaining <= 0 {
		return nil, ErrSSEEventTooLarge
	}

	var line []byte
	for {
		fragment, err := r.reader.ReadSlice('\n')
		if len(fragment) > 0 {
			if len(line)+len(fragment) > remaining {
				return nil, ErrSSEEventTooLarge
			}
			line = append(line, fragment...)
			r.resetIdle()
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return line, err
	}
}

// resetIdle 在收到数据后重置空闲计时器。
func (r *SSEReader) resetIdle() {
	if r.idleTimeout <= 0 || r.closer == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.timedOut {
		return
	}
	if r.timer == nil {
		r.timer = time.AfterFunc(r.idleTimeout, r.expire)
		return
	}
	r.timer.Reset(r.idleTimeout)
}

// expire 在空闲超时后关闭底层 Reader，使阻塞中的 Read 返回。
func (r *SSEReader) expire() {
	r.mu.Lock()
	if r.closed || r.timedOut {
		r.mu.Unlock()
		return
	}
	r.timedOut = true
	closer := r.closer
	r.mu.Unlock()

	if closer != nil {
		_ = closer.Close()
	}
}

// mapReadError 把空闲超时导致的底层关闭映射为稳定错误。
func (r *SSEReader) mapReadError(err error) error {
	r.mu.Lock()
	timedOut := r.timedOut
	r.mu.Unlock()
	if timedOut {
		return fmt.Errorf("%w: 连续 %s 未收到数据", ErrSSEIdleTimeout, r.idleTimeout)
	}
	return err
}

// Close 停止空闲计时器。它不关闭底层 Reader，底层 body 由调用方负责关闭；
// 空闲超时发生时 SSEReader 会主动关闭一次底层 Reader 以解除阻塞。
func (r *SSEReader) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	r.closed = true
	timer := r.timer
	r.timer = nil
	r.mu.Unlock()

	if timer != nil {
		timer.Stop()
	}
	return nil
}

// trimLineEnding 去掉行尾的 "\n" 或 "\r\n"，保留其余原始字节。
func trimLineEnding(line []byte) []byte {
	if len(line) > 0 && line[len(line)-1] == '\n' {
		line = line[:len(line)-1]
	}
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}
	return line
}
