# Linenoisy

Linenoisy is a simple pure-Go line editor.
It speaks to `io.Reader` and `io.Writer` instead of pty/tty,
which makes it easy to integrate with network based applications (see [examples/ssh](https://github.com/Joker/linenoisy/blob/master/examples/ssh/main.go)).

It is inspired by [Linenoise](https://github.com/antirez/linenoise).  
Based on [Linesqueak](https://github.com/ichiban/linesqueak).

# Features

- [x] Standard Key Bindings
- [x] History
- [x] Completion
- [x] Hints

# Basic Usage

```go
e := linenoisy.NewTerminal(channel, "> ") // io.ReadWriteCloser (e.g. `ssh.Channel`)

// you may want to process multiple lines
// if so, call `LineEditor()` in a loop
for {
	line, err := e.LineEditor() // `LineEditor()` does all the editor things and returns input line
	if err != nil {
		panic(err) // TODO: proper error handling
	}
	
	log.Printf("line: %s\n", line)

	e.Raw.Write([]byte("\r\n"))
	if len(line) == 0 {
		continue
	}

	fmt.Fprintf(e, "", line)
}
```

# Similar Projects

- [Term](https://pkg.go.dev/golang.org/x/term)
- [Readline](https://github.com/chzyer/readline)
- [Liner](https://github.com/peterh/liner)

# License

See the LICENSE file for license rights and limitations (MIT).