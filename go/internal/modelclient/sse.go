package modelclient

import "strings"

// sseParser is a faithful port of the eventsource-parser stream parser used by
// the AI SDK (node_modules/eventsource-parser/dist/index.js). Only the parts the
// SDK exercises are kept, but the line-splitting rules are reproduced exactly,
// including:
//
//   - a leading UTF-8 BOM on the first chunk is stripped;
//   - a chunk with neither LF nor CR is buffered whole (the common case);
//   - in the no-CR fast path, a `data:` line immediately followed by LF emits the
//     event synchronously without waiting for the next line;
//   - CR, LF and CRLF all terminate a line; a trailing lone CR is buffered;
//   - multi-line `data` fields join with "\n";
//   - comment lines (`:`) and id/event/retry fields are parsed;
//   - unknown fields are ignored (onError is a no-op here).
//
// It intentionally does NOT implement maxBufferSize (the SDK does not set it).
type sseParser struct {
	isFirstChunk bool
	pending      strings.Builder
	hasPending   bool

	// event state
	id        string
	hasID     bool
	data      strings.Builder
	dataLines int
	eventType string
	hasEvent  bool
}

func newSSEParser() *sseParser {
	return &sseParser{isFirstChunk: true}
}

// sseEvent is a dispatched event: the accumulated data field plus optional id and
// event type. Only events with at least one data line are dispatched, matching
// dispatchEvent()'s `dataLines > 0` guard.
type sseEvent struct {
	Data  string
	ID    string
	Event string
}

// feed consumes one chunk and returns any events it completed. Chunks may split
// at any byte boundary.
func (p *sseParser) feed(chunk string) []sseEvent {
	if p.isFirstChunk {
		p.isFirstChunk = false
		// strip BOM
		if strings.HasPrefix(chunk, "\ufeff") {
			chunk = chunk[len("\ufeff"):]
		}
	}

	var events []sseEvent

	if !p.hasPending {
		trailing, evs := p.processLines(chunk)
		events = append(events, evs...)
		if trailing != "" {
			p.pending.WriteString(trailing)
			p.hasPending = true
		}
		return events
	}

	if !strings.ContainsAny(chunk, "\n\r") {
		// no line terminator: buffer whole chunk
		p.pending.WriteString(chunk)
		return events
	}

	input := p.pending.String() + chunk
	p.pending.Reset()
	p.hasPending = false
	trailing, evs := p.processLines(input)
	events = append(events, evs...)
	if trailing != "" {
		p.pending.WriteString(trailing)
		p.hasPending = true
	}
	return events
}

// processLines consumes complete lines and returns the unfinished trailing text
// plus dispatched events. The fast path (no CR in the chunk) mirrors the JS
// implementation, including its synchronous `data:`-then-LF shortcut.
func (p *sseParser) processLines(chunk string) (string, []sseEvent) {
	var events []sseEvent

	if !strings.ContainsRune(chunk, '\r') {
		searchIndex := 0
		for {
			rel := strings.IndexByte(chunk[searchIndex:], '\n')
			if rel == -1 {
				break
			}
			lfIndex := searchIndex + rel
			if searchIndex == lfIndex {
				// blank line dispatches
				p.dispatch(&events)
				searchIndex = lfIndex + 1
				continue
			}
			if isDataPrefix(chunk, searchIndex) {
				valueStart := searchIndex + 5
				if chunk[searchIndex+5] == ' ' {
					valueStart = searchIndex + 6
				}
				value := chunk[valueStart:lfIndex]
				// last line of the event followed immediately by LF: emit now
				if p.dataLines == 0 && lfIndex+1 < len(chunk) && chunk[lfIndex+1] == '\n' {
					events = append(events, sseEvent{Data: value, ID: p.idOrEmpty(), Event: p.eventType})
					p.resetEvent()
					searchIndex = lfIndex + 2
					continue
				}
				if p.dataLines == 0 {
					p.data.WriteString(value)
				} else {
					p.data.WriteString("\n")
					p.data.WriteString(value)
				}
				p.dataLines++
			} else if isEventPrefix(chunk, searchIndex) {
				valueStart := searchIndex + 6
				if searchIndex+6 < len(chunk) && chunk[searchIndex+6] == ' ' {
					valueStart = searchIndex + 7
				}
				t := chunk[valueStart:lfIndex]
				p.eventType = t
				p.hasEvent = t != ""
			} else {
				p.parseLine(chunk[searchIndex:lfIndex], &events)
			}
			searchIndex = lfIndex + 1
		}
		return chunk[searchIndex:], events
	}

	// general path with CR handling
	searchIndex := 0
	for searchIndex < len(chunk) {
		crIndex := strings.IndexByte(chunk[searchIndex:], '\r')
		if crIndex != -1 {
			crIndex += searchIndex
		}
		lfIndex := strings.IndexByte(chunk[searchIndex:], '\n')
		if lfIndex != -1 {
			lfIndex += searchIndex
		}
		lineEnd := -1
		switch {
		case crIndex != -1 && lfIndex != -1:
			if crIndex < lfIndex {
				lineEnd = crIndex
			} else {
				lineEnd = lfIndex
			}
		case crIndex != -1:
			if crIndex == len(chunk)-1 {
				lineEnd = -1 // trailing lone CR is buffered
			} else {
				lineEnd = crIndex
			}
		case lfIndex != -1:
			lineEnd = lfIndex
		}
		if lineEnd == -1 {
			break
		}
		p.parseLine(chunk[searchIndex:lineEnd], &events)
		searchIndex = lineEnd + 1
		if chunk[searchIndex-1] == '\r' && searchIndex < len(chunk) && chunk[searchIndex] == '\n' {
			searchIndex++
		}
	}
	return chunk[searchIndex:], events
}

func (p *sseParser) parseLine(line string, events *[]sseEvent) {
	if line == "" {
		p.dispatch(events)
		return
	}
	first := line[0]
	if isDataPrefix(line, 0) {
		valueStart := 5
		if len(line) > 5 && line[5] == ' ' {
			valueStart = 6
		}
		value := line[valueStart:]
		if p.dataLines == 0 {
			p.data.WriteString(value)
		} else {
			p.data.WriteString("\n")
			p.data.WriteString(value)
		}
		p.dataLines++
		return
	}
	if isEventPrefix(line, 0) {
		valueStart := 6
		if len(line) > 6 && line[6] == ' ' {
			valueStart = 7
		}
		t := line[valueStart:]
		p.eventType = t
		p.hasEvent = t != ""
		return
	}
	if first == 'i' && len(line) >= 3 && line[1] == 'd' && line[2] == ':' {
		valueStart := 3
		if len(line) > 3 && line[3] == ' ' {
			valueStart = 4
		}
		value := line[valueStart:]
		if strings.ContainsRune(value, '\x00') {
			p.id = ""
			p.hasID = false
		} else {
			p.id = value
			p.hasID = true
		}
		return
	}
	if first == ':' {
		// comment; ignored (onComment is a no-op in the SDK pipeline)
		return
	}
	// generic field:name:value
	idx := strings.IndexByte(line, ':')
	if idx == -1 {
		p.processField(line, "", line)
		return
	}
	field := line[:idx]
	offset := 1
	if idx+1 < len(line) && line[idx+1] == ' ' {
		offset = 2
	}
	value := line[idx+offset:]
	p.processField(field, value, line)
}

func (p *sseParser) processField(field, value, _ string) {
	switch field {
	case "event":
		p.eventType = value
		p.hasEvent = value != ""
	case "data":
		if p.dataLines == 0 {
			p.data.WriteString(value)
		} else {
			p.data.WriteString("\n")
			p.data.WriteString(value)
		}
		p.dataLines++
	case "id":
		if strings.ContainsRune(value, '\x00') {
			p.id = ""
			p.hasID = false
		} else {
			p.id = value
			p.hasID = true
		}
	case "retry":
		// numeric retry; ignored (the SDK does not use reconnection delay)
	default:
		// unknown field; ignored
	}
}

func (p *sseParser) dispatch(events *[]sseEvent) {
	if p.dataLines > 0 {
		*events = append(*events, sseEvent{Data: p.data.String(), ID: p.idOrEmpty(), Event: p.eventType})
	}
	p.resetEvent()
}

func (p *sseParser) resetEvent() {
	p.id = ""
	p.hasID = false
	p.data.Reset()
	p.dataLines = 0
	p.eventType = ""
	p.hasEvent = false
}

func (p *sseParser) idOrEmpty() string {
	if !p.hasID {
		return ""
	}
	return p.id
}

func isDataPrefix(s string, i int) bool {
	return i+4 < len(s) && s[i] == 'd' && s[i+1] == 'a' && s[i+2] == 't' && s[i+3] == 'a' && s[i+4] == ':'
}

func isEventPrefix(s string, i int) bool {
	return i+5 < len(s) &&
		s[i] == 'e' && s[i+1] == 'v' && s[i+2] == 'e' && s[i+3] == 'n' && s[i+4] == 't' && s[i+5] == ':'
}
