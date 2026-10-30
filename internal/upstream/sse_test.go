package upstream

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestSSEReaderPreservesRawBytesAndFields(t *testing.T) {
	input := ": keep-alive\r\n\r\n" +
		"event: response.output_text.delta\r\n" +
		"id: evt_1\r\n" +
		"retry: 1500\r\n" +
		"data: {\"type\":\"response.output_text.delta\",\r\n" +
		"data: \"delta\":\"Hi\"}\r\n" +
		"\r\n"

	reader := NewSSEReader(strings.NewReader(input), SSEReaderOptions{})
	defer reader.Close()

	comment, err := reader.Next()
	if err != nil {
		t.Fatalf("第一个事件失败：%v", err)
	}
	if !bytes.Equal(comment.Raw, []byte(": keep-alive\r\n\r\n")) {
		t.Fatalf("注释事件 Raw = %q", comment.Raw)
	}
	if len(comment.Comments) != 1 || string(comment.Comments[0]) != ": keep-alive" {
		t.Fatalf("Comments = %q", comment.Comments)
	}
	if len(comment.Data) != 0 || len(comment.Event) != 0 {
		t.Fatalf("注释事件不应有字段：%+v", comment)
	}

	event, err := reader.Next()
	if err != nil {
		t.Fatalf("第二个事件失败：%v", err)
	}
	if string(event.Event) != "response.output_text.delta" {
		t.Fatalf("Event = %q", event.Event)
	}
	if string(event.ID) != "evt_1" || string(event.Retry) != "1500" {
		t.Fatalf("ID/Retry = %q/%q", event.ID, event.Retry)
	}
	wantData := "{\"type\":\"response.output_text.delta\",\n\"delta\":\"Hi\"}"
	if string(event.Data) != wantData {
		t.Fatalf("Data = %q, want %q", event.Data, wantData)
	}
	wantRaw := "event: response.output_text.delta\r\n" +
		"id: evt_1\r\n" +
		"retry: 1500\r\n" +
		"data: {\"type\":\"response.output_text.delta\",\r\n" +
		"data: \"delta\":\"Hi\"}\r\n\r\n"
	if string(event.Raw) != wantRaw {
		t.Fatalf("Raw = %q, want %q", event.Raw, wantRaw)
	}

	if _, err := reader.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("流结束错误 = %v，期望 io.EOF", err)
	}
}

func TestSSEReaderHandlesLFAndUnterminatedFinalEvent(t *testing.T) {
	input := "data: first\n\n" +
		"event: message\n" +
		"data: second\n"

	reader := NewSSEReader(strings.NewReader(input), SSEReaderOptions{})
	defer reader.Close()

	first, err := reader.Next()
	if err != nil {
		t.Fatalf("第一个事件失败：%v", err)
	}
	if string(first.Data) != "first" || string(first.Raw) != "data: first\n\n" {
		t.Fatalf("第一个事件错误：%+v", first)
	}

	second, err := reader.Next()
	if err != nil {
		t.Fatalf("第二个事件失败：%v", err)
	}
	if string(second.Event) != "message" || string(second.Data) != "second" {
		t.Fatalf("第二个事件错误：%+v", second)
	}
	if string(second.Raw) != "event: message\ndata: second\n" {
		t.Fatalf("未终止事件 Raw = %q", second.Raw)
	}

	if _, err := reader.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("流结束错误 = %v，期望 io.EOF", err)
	}
}

func TestSSEReaderEmptyDataFieldIsAnEvent(t *testing.T) {
	reader := NewSSEReader(strings.NewReader("data:\n\n"), SSEReaderOptions{})
	defer reader.Close()

	event, err := reader.Next()
	if err != nil {
		t.Fatalf("Next() 失败：%v", err)
	}
	if len(event.Data) != 0 {
		t.Fatalf("空 data 值 = %q", event.Data)
	}
	if string(event.Raw) != "data:\n\n" {
		t.Fatalf("Raw = %q", event.Raw)
	}
}

func TestSSEReaderPreservesEmptyDataLines(t *testing.T) {
	reader := NewSSEReader(strings.NewReader("data:\ndata: x\ndata:\n\n"), SSEReaderOptions{})
	defer reader.Close()

	event, err := reader.Next()
	if err != nil {
		t.Fatalf("Next() 失败：%v", err)
	}
	if string(event.Data) != "\nx\n" {
		t.Fatalf("Data = %q，期望保留空 data 行的换行", event.Data)
	}
}

func TestSSEReaderRejectsOversizedEvent(t *testing.T) {
	tests := []struct {
		name  string
		input string
		limit int
	}{
		{
			name:  "单行超限",
			input: "data: " + strings.Repeat("x", 64) + "\n\n",
			limit: 32,
		},
		{
			name:  "多行累计超限",
			input: "data: 1234567890\ndata: 1234567890\ndata: 1234567890\n\n",
			limit: 40,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := NewSSEReader(strings.NewReader(test.input), SSEReaderOptions{
				MaxEventBytes: test.limit,
			})
			defer reader.Close()

			_, err := reader.Next()
			if !errors.Is(err, ErrSSEEventTooLarge) {
				t.Fatalf("Next() 错误 = %v，期望 ErrSSEEventTooLarge", err)
			}
		})
	}
}

func TestSSEReaderIdleTimeout(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()

	reader := NewSSEReader(pr, SSEReaderOptions{
		IdleTimeout: 20 * time.Millisecond,
	})
	defer reader.Close()

	_, err := reader.Next()
	if !errors.Is(err, ErrSSEIdleTimeout) {
		t.Fatalf("Next() 错误 = %v，期望 ErrSSEIdleTimeout", err)
	}
}

func TestSSEReaderIgnoresBlankLinesBetweenEvents(t *testing.T) {
	reader := NewSSEReader(strings.NewReader("\n\n\ndata: ok\n\n\n"), SSEReaderOptions{})
	defer reader.Close()

	event, err := reader.Next()
	if err != nil {
		t.Fatalf("Next() 失败：%v", err)
	}
	if string(event.Data) != "ok" || string(event.Raw) != "data: ok\n\n" {
		t.Fatalf("事件错误：%+v", event)
	}
	if _, err := reader.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("流结束错误 = %v", err)
	}
}
